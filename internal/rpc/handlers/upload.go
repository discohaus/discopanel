package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/discohaus/discopanel/internal/auth"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/transfer"
	"google.golang.org/protobuf/encoding/protojson"
)

// Writes an UploadChunkResponse as protojson
func writeChunkResponse(w http.ResponseWriter, status int, resp *v1.UploadChunkResponse) {
	data, err := protojson.Marshal(resp)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// Handles PUT upload streaming with bearer auth and resume offset
func NewUploadStreamHandler(uploadManager *transfer.UploadManager, authManager *auth.Manager, enforcer *rbac.Enforcer, log *logger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract session ID from path
		sessionID := strings.TrimPrefix(r.URL.Path, "/api/v1/upload/")
		if sessionID == "" || strings.Contains(sessionID, "/") {
			http.Error(w, "invalid session_id", http.StatusBadRequest)
			return
		}

		// Authenticate using the shared auth logic
		user, err := authManager.AuthenticateFromHeader(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Check RBAC uploads create permission
		allowed, rbacErr := enforcer.Enforce(user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_UPLOADS, optionsv1.ActionType_ACTION_TYPE_CREATE, "*")
		if rbacErr != nil || !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Parse resume offset
		offset := int64(0)
		if offsetStr := r.Header.Get("X-Upload-Offset"); offsetStr != "" {
			offset, err = strconv.ParseInt(offsetStr, 10, 64)
			if err != nil || offset < 0 {
				http.Error(w, "invalid X-Upload-Offset", http.StatusBadRequest)
				return
			}
		}

		// Extend server read/write timeout
		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Now().Add(30 * time.Minute)); err != nil {
			log.Warn("Failed to set read deadline: %v", err)
		}
		if err := rc.SetWriteDeadline(time.Now().Add(30 * time.Minute)); err != nil {
			log.Warn("Failed to set write deadline: %v", err)
		}

		// Stream request body into the upload session file
		bytesWritten, completed, err := uploadManager.WriteStream(sessionID, r.Body, offset)
		if err != nil {
			log.Error("Stream upload error for session %s: %v", sessionID, err)
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, transfer.ErrSessionNotFound), errors.Is(err, transfer.ErrSessionExpired):
				status = http.StatusNotFound
			case errors.Is(err, transfer.ErrSessionCompleted):
				status = http.StatusConflict
			case errors.Is(err, transfer.ErrInvalidOffset), errors.Is(err, transfer.ErrFileTooLarge):
				status = http.StatusBadRequest
			}
			writeChunkResponse(w, status, &v1.UploadChunkResponse{
				SessionId:     sessionID,
				BytesReceived: offset + bytesWritten,
			})
			return
		}

		writeChunkResponse(w, http.StatusOK, &v1.UploadChunkResponse{
			SessionId:     sessionID,
			BytesReceived: offset + bytesWritten,
			Completed:     completed,
		})
	})
}
