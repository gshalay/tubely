package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

const (
	MEDIA_JPEG = "image/jpeg"
	MEDIA_PNG  = "image/png"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// Max of 10 MB: Equivalent to 10 * 1024 * 1024.
	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
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

	if mediaType != MEDIA_JPEG && mediaType != MEDIA_PNG {
		log.Printf("error: Invalid mime type")
		respondWithError(w, http.StatusBadRequest, "Invalid mime type", err)
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

	key := make([]byte, 32)
	rand.Read(key)
	enc := base64.RawURLEncoding.EncodeToString(key)

	filename := filepath.Join(cfg.assetsRoot, fmt.Sprintf("%s.%s", enc, strings.Split(mediaType, "/")[1]))

	thumbFile, err := os.Create(filename)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to parse image data", err)
		return
	}

	io.Copy(thumbFile, file)

	thumbUrl := fmt.Sprintf("http://localhost:%s/%s", cfg.port, filename)
	vid.ThumbnailURL = &thumbUrl

	err = cfg.db.UpdateVideo(vid)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to update video thumbnail", err)
		return
	}

	respondWithJSON(w, http.StatusOK, vid)
}
