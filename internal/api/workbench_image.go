package api

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
)

func (a *server) workbenchImage(w http.ResponseWriter, r *http.Request, client *gitlab.Client, projectID int64, secret, filename string) {
	decoded, err := hex.DecodeString(secret)
	if err != nil || len(decoded) != 16 || !validUploadFilename(filename) {
		problem(w, http.StatusBadRequest, "图片地址无效")
		return
	}
	data, contentType, err := client.DownloadUploadImage(r.Context(), projectID, secret, filename)
	if err != nil {
		if errors.Is(err, gitlab.ErrUnsupportedUploadImage) {
			problem(w, http.StatusUnsupportedMediaType, "GitLab 附件不是可预览的图片")
			return
		}
		workbenchError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func validUploadFilename(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
