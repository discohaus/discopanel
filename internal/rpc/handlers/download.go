package handlers

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/discohaus/discopanel/internal/auth"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	"github.com/discohaus/discopanel/pkg/transfer"
)

// Handles GET download streaming with auth and range resume
func NewDownloadStreamHandler(downloadManager *transfer.DownloadManager, authManager *auth.Manager, enforcer *rbac.Enforcer, log *logger.Logger) http.Handler {
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

		// Get auth header, fall back to ?token= query param
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			if token := r.URL.Query().Get("token"); token != "" {
				authHeader = "Bearer " + token
			}
		}

		user, err := authManager.AuthenticateFromHeader(r.Context(), authHeader)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Check RBAC files read permission
		allowed, rbacErr := enforcer.Enforce(user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_FILES, optionsv1.ActionType_ACTION_TYPE_READ, "*")
		if rbacErr != nil || !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Look up download session
		session, err := downloadManager.GetSession(sessionID)
		if err != nil {
			http.Error(w, "download session not found or expired", http.StatusNotFound)
			return
		}

		// Open the temp file
		file, err := os.Open(session.FilePath)
		if err != nil {
			log.Error("Failed to open download file for session %s: %v", sessionID, err)
			http.Error(w, "download file not available", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		stat, err := file.Stat()
		if err != nil {
			log.Error("Failed to stat download file for session %s: %v", sessionID, err)
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
