package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt" // For constructing the S3 URL string
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth" // Assuming this path is correct
	"github.com/google/uuid"
)

// Assuming apiConfig and other helper functions (respondWithError, respondWithJSON) are defined elsewhere

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Assignment: Set an upload limit of 1 GB (1 << 30 bytes)
	const maxUploadSizeBodyBytes = 1 << 30
	// For ParseMultipartForm, max memory for non-file parts or small files in memory
	const maxParseFormMemoryBytes = int64(32 << 20) // 32MB

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSizeBodyBytes)
	err = r.ParseMultipartForm(maxParseFormMemoryBytes)
	if err != nil {
		if err.Error() == "http: request body too large" {
			respondWithError(
				w,
				http.StatusRequestEntityTooLarge,
				"Request body too large, limit is 1GB",
				err,
			)
			return
		}
		respondWithError(
			w,
			http.StatusBadRequest,
			"Unable to parse multipart form",
			err,
		)
		return
	}

	video, err := cfg.db.GetVideo(videoID) // Assuming GetVideo takes uuid.UUID
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't find video",
			err,
		)
		return
	}
	if video.UserID != userID { // Assuming video.UserID is comparable to userID
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Not authorized to update this video",
			nil,
		)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file 'video'", err)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		respondWithError(w, http.StatusBadRequest, "Missing Content-Type for video", nil)
		return
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type format", err)
		return
	}

	// Assignment: Validate the uploaded file to ensure it's an MP4 video
	if mediaType != "video/mp4" {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid Content-Type for video, only 'video/mp4' is allowed",
			nil,
		)
		return
	}

	// Assignment: Save the uploaded file to a temporary file on disk.
	// Use os.CreateTemp with an empty string for the directory and a pattern.
	tmpFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Unable to create temporary file on server",
			err,
		)
		return
	}
	// Defer LIFO: tmpFile.Close() runs before os.Remove()
	defer tmpFile.Close()
	defer os.Remove(tmpFile.Name()) // Clean up temp file

	if _, err = io.Copy(tmpFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error saving uploaded file to temp file", err)
		return
	}

	optimizedPath, err := cfg.processVideoForFastStart(tmpFile.Name())
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Error optimizing video for streaming",
			err,
		)
		return
	}

	// Reopen the optimized file for reading since the original file has been replaced
	tmpFile.Close()
	tmpFile, err = os.Open(optimizedPath)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Error opening optimized video file",
			err,
		)
		return
	}

	// Reset the tempFile's file pointer to the beginning
	if _, err = tmpFile.Seek(0, io.SeekStart); err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Unable to seek in temp file",
			err,
		)
		return
	}

	// Assignment: File key <random-32-byte-hex>.ext
	randomBytes := make([]byte, 16) // 16 bytes = 32 hex characters
	if _, err := rand.Read(randomBytes); err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Failed to generate secure file key",
			err,
		)
		return
	}
	// check orienation of the video file
	ratio, err := cfg.getVideoAspectRatio(tmpFile.Name())
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Error getting video aspect ratio",
			err,
		)
	}
	var prefix = "other"
	if ratio == "16:9" {
		prefix = "landscape"
	} else if ratio == "9:16" {
		prefix = "portrait"
	}
	s3FileKey := prefix + "/" + hex.EncodeToString(randomBytes) + ".mp4"

	// Put the object into S3
	_, err = cfg.s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(s3FileKey),
		Body:        tmpFile,
		ContentType: aws.String(contentType), // This should be "video/mp4"
	})
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Error uploading video to S3",
			err,
		)
		return
	}

	// Assignment: Update VideoURL with S3 bucket and key.
	// Format: https://<bucket-name>.s3.<region>.amazonaws.com/<key>
	s3VideoURL := fmt.Sprintf(
		"https://%s/%s",
		cfg.s3CfDistribution,
		s3FileKey,
	)
	video.VideoURL = aws.String(s3VideoURL) // Assuming video.VideoURL is *string

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video record in DB", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
