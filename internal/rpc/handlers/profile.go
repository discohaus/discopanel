package handlers

import (
	"fmt"
	"net/http"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/discohaus/discopanel/internal/auth"
	"github.com/discohaus/discopanel/pkg/logger"
)

// Serves a heap profile to admins, gc=1 collects first
func NewHeapProfileHandler(authManager *auth.Manager, log *logger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, err := authManager.AuthenticateFromHeader(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !holdsAdminRole(user.Roles) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if r.URL.Query().Get("gc") != "" {
			runtime.GC()
		}

		filename := fmt.Sprintf("discopanel-heap-%s.pprof", time.Now().UTC().Format("20060102-150405"))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		if err := pprof.Lookup("heap").WriteTo(w, 0); err != nil {
			log.Error("Failed to write heap profile: %v", err)
		}
	})
}

// True when the role list carries the admin role
func holdsAdminRole(roles []string) bool {
	for _, role := range roles {
		if strings.EqualFold(role, "admin") {
			return true
		}
	}
	return false
}
