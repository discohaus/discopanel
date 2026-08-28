// Fetches release binaries into the local cache
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Serializes fetches so parallel jobs share one download
var fetchLocks sync.Map

// Binary for one tag from cache or the github release
func fetchBinary(ctx context.Context, opt options, tag string) (string, string, error) {
	bin := filepath.Join(opt.cache, tag, "discopanel")
	lock, _ := fetchLocks.LoadOrStore(tag, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	if exists(bin) {
		return bin, "cache", nil
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
		return "", "", err
	}
	if err := downloadRelease(ctx, opt.repo, tag, bin); err != nil {
		return "", "", err
	}
	return bin, "release", nil
}

// Downloads and unpacks one release tarball
func downloadRelease(ctx context.Context, repo, tag, bin string) error {
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/discopanel-%s-%s.tar.gz", repo, tag, runtime.GOOS, runtime.GOARCH)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no release asset at %s", url)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(filepath.Base(hdr.Name), "discopanel") {
			continue
		}
		return writeAtomic(bin, tr)
	}
	return fmt.Errorf("archive %s holds no binary", url)
}

// Writes through a sibling temp file then renames
func writeAtomic(path string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".partial-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
