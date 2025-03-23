package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

const (
	LANDSCAPE_RATIO    = "16:9"
	PORTRAIT_RATIO     = "9:16"
	MEDIA_MP4          = "video/mp4"
	LANDSCAPE_PREFIX   = "/landscape/"
	PORTRAIT_PREFIX    = "/portrait/"
	OTHER_PREFIX       = "/other/"
	NINE_SIXTEEN_RATIO = 0.5625
	SIXTEEN_NINE_RATIO = 1.78
	RATIO_TOLERANCE    = 0.10
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

	fullPath, err := filepath.Abs(vidTempFile.Name())
	if err != nil {
		log.Printf("Couldn't get video file path")
		respondWithError(w, http.StatusBadRequest, "Unable to get video path", err)
		return
	}

	aspectRatio, err := getVideoAspectRatio(fullPath)
	if err != nil {
		log.Printf("Couldn't get video aspect ratio: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to get video aspect ratio", err)
		return
	}

	vidFilename, err = processVideoForFastStart(fullPath, vidFilename)
	if err != nil {
		log.Printf("Couldn't set video for fast processing: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to encode video for fast processing: ", err)
		return
	}

	objKey := ""
	if aspectRatio == LANDSCAPE_RATIO {
		objKey = LANDSCAPE_PREFIX + vidFilename
	} else if aspectRatio == PORTRAIT_RATIO {
		objKey = PORTRAIT_PREFIX + vidFilename
	} else {
		objKey = OTHER_PREFIX + vidFilename
	}

	fastFile, err := os.Open(vidFilename)
	if err != nil {
		log.Fatalf("failed to open file: %v", err)
	}

	_, err = cfg.s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &objKey,
		Body:        fastFile,
		ContentType: &mediaType,
	})

	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to put object to s3", err)
		return
	}

	vidUrl := fmt.Sprintf("%s,%s", cfg.s3Bucket, objKey)
	log.Printf("vurl - %s", vidUrl)

	vid.VideoURL = &vidUrl

	err = cfg.db.UpdateVideo(vid)
	if err != nil {
		log.Printf("error: %v", err)
		respondWithError(w, http.StatusBadRequest, "Unable to update video file", err)
		return
	}

	fastFile.Close()
	os.Remove(fastFile.Name())

	w.Header().Set("Content-Type", "video/mp4")
	respondWithJSON(w, http.StatusOK, vid)
}

func getVideoAspectRatio(filePath string) (string, error) {
	type Stream struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}

	type FFProbeOutput struct {
		Streams []Stream `json:"streams"`
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	cmd.Run()

	var probeOutput FFProbeOutput

	err := json.Unmarshal(buf.Bytes(), &probeOutput)
	if err != nil {
		log.Printf("couldn't get video aspect ratio: %v", err)
		return "", err
	}

	if len(probeOutput.Streams) > 0 {
		ratio := float64(probeOutput.Streams[0].Width) / float64(probeOutput.Streams[0].Height)
		aspectRatio := ""

		if (NINE_SIXTEEN_RATIO-RATIO_TOLERANCE) <= ratio && ratio <= (NINE_SIXTEEN_RATIO+RATIO_TOLERANCE) {
			aspectRatio = "9:16"
		} else if (SIXTEEN_NINE_RATIO-RATIO_TOLERANCE) <= ratio && ratio <= (SIXTEEN_NINE_RATIO+RATIO_TOLERANCE) {
			aspectRatio = "16:9"
		} else {
			aspectRatio = "other"
		}

		return aspectRatio, nil
	} else {
		return "", fmt.Errorf("length of probe stream output is 0")
	}
}

func processVideoForFastStart(filepath, outPath string) (string, error) {
	outPath = outPath + ".processing"

	log.Printf("p - %s", outPath)

	cmd := exec.Command("ffmpeg", "-i", filepath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outPath)

	// Capture standard output and error
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		log.Printf("ffmpeg processing failed: %v, stderr: %s, stdout: %s", err, errBuf.String(), outBuf.String())
		return "", err
	}

	return outPath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	objectInput := &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}

	presignClient := s3.NewPresignClient(s3Client)
	presignedReq, err := presignClient.PresignGetObject(context.TODO(), objectInput, s3.WithPresignExpires(expireTime))
	if err != nil {
		log.Printf("Couldn't presign s3 object.")
		return "", err
	}

	return presignedReq.URL, nil
}
