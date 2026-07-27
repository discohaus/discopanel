package provisioner

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/indexers"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

type checksum struct {
	algo  string // "sha1" | "sha256" | "sha512" | "md5"
	value string
}

func newHasher(algo string) hash.Hash {
	switch algo {
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	case "md5":
		return md5.New()
	default:
		return nil
	}
}

var downloadClient = &http.Client{
	// No global timeout, large modpack downloads run long
	Timeout: 0,
}

// Reports throttled byte progress through report
type progressWriter struct {
	total  int64
	done   int64
	report func(done, total int64)
	last   time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.done += int64(len(b))
	if time.Since(w.last) >= 3*time.Second {
		w.last = time.Now()
		w.report(w.done, w.total)
	}
	return len(b), nil
}

// Builds a progress callback emitting console lines
func (p *Provisioner) reporter(server *v1.Server, label string) func(done, total int64) {
	return func(done, total int64) {
		if total > 0 {
			p.progress(server, "downloading %s: %d%% (%.1f/%.1f MB)",
				label, done*100/total, float64(done)/1024/1024, float64(total)/1024/1024)
		} else {
			p.progress(server, "downloading %s: %.1f MB", label, float64(done)/1024/1024)
		}
	}
}

// Terminal HTTP failure, retrying cannot help
type httpStatusError struct {
	url    string
	status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("download failed for %s: status %d", e.url, e.status)
}

// Deterministic content mismatch, upstream serves the wrong bytes
type checksumError struct {
	url, algo, want, got string
}

func (e *checksumError) Error() string {
	return fmt.Sprintf("checksum mismatch for %s: expected %s %s, got %s", e.url, e.algo, e.want, e.got)
}

// Network hiccups and server-side failures retry, the rest never
func isTransient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.status == http.StatusRequestTimeout ||
			se.status == http.StatusTooManyRequests || se.status >= 500
	}
	var ce *checksumError
	return !errors.As(err, &ce)
}

// Retries fn up to three times with backoff on transient errors
func retryTransient(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		if err = fn(); err == nil {
			return nil
		}
		if !isTransient(err) {
			return err
		}
	}
	return err
}

// Fetches url into dest with transient retry baked in
func (p *Provisioner) download(ctx context.Context, rawURL, dest string, sum *checksum, headers map[string]string, report func(done, total int64)) error {
	return retryTransient(ctx, func() error {
		return p.downloadOnce(ctx, rawURL, dest, sum, headers, report)
	})
}

// Fetches url into dest atomically, verifying checksum
func (p *Provisioner) downloadOnce(ctx context.Context, rawURL, dest string, sum *checksum, headers map[string]string, report func(done, total int64)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	if p.casGet(dest, sum) {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", p.cfg.Server.UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("download failed for %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{url: rawURL, status: resp.StatusCode}
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	writers := []io.Writer{tmp}
	var hasher hash.Hash
	if sum != nil {
		hasher = newHasher(sum.algo)
		if hasher != nil {
			writers = append(writers, hasher)
		}
	}
	var pw *progressWriter
	if report != nil {
		pw = &progressWriter{total: resp.ContentLength, report: report, last: time.Now()}
		writers = append(writers, pw)
	}

	if _, err := io.Copy(io.MultiWriter(writers...), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("download interrupted for %s: %w", rawURL, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if pw != nil {
		report(pw.done, pw.total)
	}

	if hasher != nil {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, sum.value) {
			return &checksumError{url: rawURL, algo: sum.algo, want: sum.value, got: got}
		}
		p.casPut(tmpPath, sum)
	}

	return os.Rename(tmpPath, dest)
}

// Fetches JSON through the shared resilience client
func (p *Provisioner) getJSON(ctx context.Context, rawURL string, dest any) error {
	return p.metaClient(rawURL).DoJSON(ctx, rawURL, dest)
}

// Resilience client with pacing and etag state per host
func (p *Provisioner) metaClient(rawURL string) *indexers.HTTPClient {
	host := "distro-meta"
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return indexers.NewHTTPClient(host, p.cfg.Server.UserAgent, nil)
}

// Fetches a small text document, e.g. checksum sidecars
func (p *Provisioner) getText(ctx context.Context, rawURL string) (string, error) {
	body, err := p.metaClient(rawURL).DoBytes(ctx, rawURL)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Scratch directory for downloaded installers
func installerDir(dataPath string) string {
	return filepath.Join(dataPath, ".discopanel", "installers")
}

// Joins a relative path onto the data dir safely
func joinData(dataPath string, rel string) string {
	joined := filepath.Join(dataPath, filepath.FromSlash(rel))
	clean := filepath.Clean(joined)
	root := filepath.Clean(dataPath)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		// Traversal attempt, collapse to a name inside data dir
		return filepath.Join(root, filepath.Base(clean))
	}
	return clean
}

// Normalizes ways a CF modpack can be referenced
func parseCurseForgeRef(pageURL, slug, fileID string) (string, string) {
	if pageURL != "" {
		if u, err := url.Parse(pageURL); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			// Matches trailing path modpacks/{slug}/files/{fileID}
			for i, part := range parts {
				if part == "modpacks" && i+1 < len(parts) {
					slug = parts[i+1]
				}
				if part == "files" && i+1 < len(parts) {
					fileID = parts[i+1]
				}
			}
		}
	}
	return slug, fileID
}

// Normalizes ways a Modrinth modpack can be referenced
func parseModrinthRef(modpack, version string) (string, string) {
	project := modpack
	if strings.Contains(modpack, "://") {
		if u, err := url.Parse(modpack); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			// Matches path modrinth.com/modpack/{slug}/version/{version}
			for i, part := range parts {
				if (part == "modpack" || part == "mod" || part == "project") && i+1 < len(parts) {
					project = parts[i+1]
				}
				if part == "version" && i+1 < len(parts) {
					version = parts[i+1]
				}
			}
		}
	} else if idx := strings.Index(modpack, ":"); idx > 0 {
		project = modpack[:idx]
		if version == "" {
			version = modpack[idx+1:]
		}
	}
	return project, version
}
