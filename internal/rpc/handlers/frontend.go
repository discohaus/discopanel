package handlers

import (
	"io/fs"
	"mime"
	"net/http"
	"strings"
)

func init() {
	if err := mime.AddExtensionType(".webmanifest", "application/manifest+json"); err != nil {
		panic(err)
	}
}

var apiPrefixes = []string{"/api/", "/discopanel.v1.", "/discopanel.agent.", "/grpc.reflection.", "/connect."}

func NewFrontendHandler(build fs.FS) http.Handler {
	files := http.FS(build)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, prefix := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				http.NotFound(w, r)
				return
			}
		}
		if !serveFile(w, r, files, r.URL.Path) && !serveFile(w, r, files, "/index.html") {
			http.NotFound(w, r)
		}
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, files http.FileSystem, name string) bool {
	f, err := files.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}
	w.Header().Set("Cache-Control", cachePolicy(name))
	http.ServeContent(w, r, name, stat.ModTime(), f)
	return true
}

func cachePolicy(name string) string {
	if strings.HasPrefix(name, "/_app/immutable/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}
