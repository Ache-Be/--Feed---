package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUploadSize = 200 << 20

type uploadResponse struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Kind     string `json:"kind"`
}

func UploadMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 1, Msg: "method not allowed"})
		return
	}
	if _, err := requireCurrentUsername(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, apiResponse{Code: 1, Msg: "login required"})
		return
	}

	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "video" && kind != "cover" && kind != "avatar" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "invalid upload kind"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "parse multipart form failed"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "missing file"})
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedUploadExt(kind, ext) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 1, Msg: "unsupported file type"})
		return
	}

	dir := filepath.Join(uploadRootDir(), kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "create upload dir failed"})
		return
	}

	filename := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	dstPath := filepath.Join(dir, filename)
	dst, err := os.Create(dstPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "create upload file failed"})
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Code: 2, Msg: "save upload file failed"})
		return
	}

	urlPath := "/uploads/" + kind + "/" + filename
	writeJSON(w, http.StatusOK, apiResponse{
		Code: 0,
		Msg:  "upload success",
		Data: uploadResponse{
			URL:      urlPath,
			Filename: filename,
			Kind:     kind,
		},
	})
}

func UploadRootDir() string {
	return uploadRootDir()
}

func uploadRootDir() string {
	return filepath.Join(".run", "uploads")
}

func allowedUploadExt(kind, ext string) bool {
	if kind == "video" {
		switch ext {
		case ".mp4", ".webm", ".ogg":
			return true
		}
		return false
	}

	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	}
	return false
}
