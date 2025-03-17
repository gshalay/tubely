package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

const (
	MEDIA_MP4 = "video/mp4"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading video: ", videoID, "by user", userID)

	// Max of 10 GB: Equivalent to 10 * 1024 * 1024 * 1024.
	const maxMemory = 10 << 30
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("video")
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Couldn't parse mime type", err)
		return
	}

	vid, err := cfg.db.GetVideo(videoID)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to get video", err)
		return
	}

	if vid.UserID != userID {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusUnauthorized, "Unable to parse image data", err)
		return
	}

	if mediaType != MEDIA_MP4 {
		log.Printf("error: Invalid mime type")
		respondWithError(w, http.StatusBadRequest, "Invalid mime type", err)
		return
	}

	key := make([]byte, 32)
	rand.Read(key)
	enc := base64.RawURLEncoding.EncodeToString(key)

	vidFilename := fmt.Sprintf("%s.%s", enc, strings.Split(mediaType, "/")[1])

	vidTempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to parse image data", err)
		return
	}

	// Defer is LIFO
	defer os.Remove(vidTempFile.Name())
	defer vidTempFile.Close()

	io.Copy(vidTempFile, file)
	vidTempFile.Seek(0, io.SeekStart)

	_, err = cfg.s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &vidFilename,
		Body:        vidTempFile,
		ContentType: &mediaType,
	})

	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to put object to s3", err)
		return
	}

	vidUrl := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, vidFilename)
	vid.VideoURL = &vidUrl

	err = cfg.db.UpdateVideo(vid)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to update video file", err)
		return
	}

	respondWithJSON(w, http.StatusOK, vid)
}
