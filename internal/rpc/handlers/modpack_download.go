package handlers

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
)

// Both pack formats are zips
const modpackContentType = "application/zip"

// Resolves archives of packs marked public
type PublicModpackSource interface {
	PublicModpackArchive(ctx context.Context, modpackID string) (path, filename string, err error)
}

// Unauthenticated GET for pack archives explicitly marked public
func NewPublicModpackDownloadHandler(source PublicModpackSource, notPublic error, log *logger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		modpackID := r.PathValue("id")
		if modpackID == "" {
			http.Error(w, "invalid modpack id", http.StatusBadRequest)
			return
		}

		// Missing and private packs read the same so ids cannot be probed
		path, filename, err := source.PublicModpackArchive(r.Context(), modpackID)
		if err != nil {
			if !errors.Is(err, notPublic) {
				log.Error("Failed to resolve public modpack %s: %v", modpackID, err)
			}
			http.Error(w, "modpack not found", http.StatusNotFound)
			return
		}

		file, err := os.Open(path)
		if err != nil {
			log.Error("Failed to open public modpack %s: %v", modpackID, err)
			http.Error(w, "modpack not found", http.StatusNotFound)
			return
		}
		defer file.Close()

		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			log.Error("Public modpack %s archive is not a regular file: %v", modpackID, err)
			http.Error(w, "modpack not found", http.StatusNotFound)
			return
		}

		// Extend servers write timeout
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(30 * time.Minute)); err != nil {
			log.Warn("Failed to set write deadline: %v", err)
		}

		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filename)})
		if disposition == "" {
			disposition = "attachment"
		}
		w.Header().Set("Content-Disposition", disposition)
		// Packs are zips, a fixed type stops sniffing uploads as html on the panel origin
		w.Header().Set("Content-Type", modpackContentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Revalidate so turning sharing off takes effect at once
		w.Header().Set("Cache-Control", "no-cache")

		// Handles range headers, conditional requests, and Content-Length
		http.ServeContent(w, r, filename, stat.ModTime(), file)
	})
}
