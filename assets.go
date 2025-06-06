package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (cfg apiConfig) ensureAssetsDir() error {
	if _, err := os.Stat(cfg.assetsRoot); os.IsNotExist(err) {
		return os.Mkdir(cfg.assetsRoot, 0755)
	}
	return nil
}

func getAssetPath(videoID string, mediaType string) string {
	ext := mediaTypeToExt(mediaType)
	return fmt.Sprintf("%s%s", videoID, ext)
}

func (cfg apiConfig) getAssetDiskPath(assetPath string) string {
	return filepath.Join(cfg.assetsRoot, assetPath)
}

func (cfg apiConfig) getAssetURL(assetPath string) string {
	return fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, assetPath)
}

func mediaTypeToExt(mediaType string) string {
	parts := strings.Split(mediaType, "/")
	if len(parts) != 2 {
		return ".bin"
	}
	return "." + parts[1]
}

type FFProbeOutput struct {
	Streams []StreamInfo `json:"streams"`
}
type StreamInfo struct {
	CodecType string `json:"codec_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

func (cfg apiConfig) getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		filePath,
	)
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer // To capture stderr for more detailed error messages
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	// Run the command.
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf(
			"ffprobe execution failed for '%s': %w. Stderr: %s",
			filePath,
			err,
			errBuf.String(),
		)
	}
	var ffprobeData FFProbeOutput
	if err := json.Unmarshal(outBuf.Bytes(), &ffprobeData); err != nil {
		return "", fmt.Errorf(
			"failed to unmarshal ffprobe JSON output for '%s': %w. Output: %s",
			filePath,
			err,
			outBuf.String(),
		)
	}
	var videoWidth, videoHeight int
	foundVideoStream := false

	// Iterate through streams to find the first video stream.
	for _, stream := range ffprobeData.Streams {
		if stream.CodecType == "video" {
			videoWidth = stream.Width
			videoHeight = stream.Height
			foundVideoStream = true
			break // Use the first video stream found
		}
	}
	if !foundVideoStream {
		return "", fmt.Errorf("no video stream found in '%s'", filePath)
	}
	if videoWidth <= 0 || videoHeight <= 0 {
		return "", fmt.Errorf(
			"video stream in '%s' has invalid dimensions: width=%d, height=%d",
			filePath,
			videoWidth,
			videoHeight,
		)
	}
	ratio := float64(videoWidth) / float64(videoHeight)
	const epsilon = 0.02
	sixteenNineRatio := 16.0 / 9.0
	if math.Abs(ratio-sixteenNineRatio) < epsilon {
		return "16:9", nil
	}
	nineSixteenRatio := 9.0 / 16.0
	if math.Abs(ratio-nineSixteenRatio) < epsilon {
		return "9:16", nil
	}

	// If it's neither of the common ratios, classify as "other".
	return "other", nil
}

func (cfg apiConfig) processVideoForFastStart(filePath string) (string, error) {

	outputPath := filePath + ".faststart.mp4"
	cmd := exec.Command("ffmpeg",
		"-i", filePath,
		"-movflags", "faststart",
		"-c:v", "copy",
		"-c:a", "copy",
		outputPath,
	)
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer // To capture stderr for more detailed error messages
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		// Clean up the temporary file if it exists
		os.Remove(outputPath)
		return "", fmt.Errorf(
			"ffmpeg execution failed for '%s': %w. Stderr: %s",
			filePath,
			err,
			errBuf.String(),
		)
	}

	// Replace original file with processed file
	err = os.Remove(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to remove original file: %w", err)
	}

	err = os.Rename(outputPath, filePath)
	if err != nil {
		return "", fmt.Errorf("failed to rename processed file: %w", err)
	}

	return filePath, nil
}
