package handlers

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/transfer"
)

// Handles GET download streaming with range resume
func NewDownloadStreamHandler(downloadManager *transfer.DownloadManager, log *logger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract session ID from path
		sessionID := strings.TrimPrefix(r.URL.Path, "/api/v1/download/")
		if sessionID == "" || strings.Contains(sessionID, "/") {
			http.Error(w, "invalid session_id", http.StatusBadRequest)
			return
		}

		// Random expiring session id from an authed rpc grants access
		session, err := downloadManager.GetSession(sessionID)
		if err != nil {
			http.Error(w, "download session not found or expired", http.StatusNotFound)
			return
		}

		// Open the temp file
		file, err := os.Open(session.FilePath)
		if err != nil {
			log.Error("Failed to open download file %s: %v", session.Filename, err)
			http.Error(w, "download file not available", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		stat, err := file.Stat()
		if err != nil {
			log.Error("Failed to stat download file %s: %v", session.Filename, err)
			http.Error(w, "download file not available", http.StatusInternalServerError)
			return
		}

		// Extend servers write timeout
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(30 * time.Minute)); err != nil {
			log.Warn("Failed to set write deadline: %v", err)
		}

		// Set download headers
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, session.Filename))

		// Handles range headers, conditional requests, and Content-Length
		http.ServeContent(w, r, session.Filename, stat.ModTime(), file)
	})
}
