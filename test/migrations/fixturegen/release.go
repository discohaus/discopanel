// Fetches release binaries, building from source when none shipped
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Serializes fetches so parallel jobs share one download
var fetchLocks sync.Map

// Binary for one tag from cache, release, or source
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
	err := downloadRelease(ctx, opt.repo, tag, bin)
	if err == nil {
		return bin, "release", nil
	}
	if !opt.buildMissing || !isNotFound(err) {
		return "", "", err
	}
	if err := buildFromSource(ctx, opt.repoDir, tag, bin); err != nil {
		return "", "", fmt.Errorf("no release asset and source build failed: %w", err)
	}
	return bin, "source", nil
}

// Error for a release asset that does not exist
type notFoundError struct{ url string }

func (e notFoundError) Error() string { return "no release asset at " + e.url }

func isNotFound(err error) bool {
	_, ok := err.(notFoundError)
	return ok
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
		return notFoundError{url: url}
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

// Compiles the tag's tree with a stub frontend embedded
func buildFromSource(ctx context.Context, repoDir, tag, bin string) error {
	src := filepath.Join(filepath.Dir(bin), "src")
	if err := os.RemoveAll(src); err != nil {
		return err
	}
	if err := os.MkdirAll(src, 0755); err != nil {
		return err
	}
	archive := exec.CommandContext(ctx, "git", "-C", repoDir, "archive", "--format=tar", tag)
	extract := exec.CommandContext(ctx, "tar", "-x", "-C", src)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	extract.Stdin = pipe
	if err := archive.Start(); err != nil {
		return err
	}
	if err := extract.Run(); err != nil {
		return fmt.Errorf("extract %s: %w", tag, err)
	}
	if err := archive.Wait(); err != nil {
		return fmt.Errorf("archive %s: %w", tag, err)
	}

	// Embedded frontend only needs to exist for the build
	stub := filepath.Join(src, "web", "discopanel", "build")
	if err := os.MkdirAll(stub, 0755); err != nil {
		return err
	}
	index := filepath.Join(stub, "index.html")
	if !exists(index) {
		if err := os.WriteFile(index, []byte("<!doctype html><title>fixture</title>"), 0644); err != nil {
			return err
		}
	}

	logPath := filepath.Join(filepath.Dir(bin), "build.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	if err := generateProtos(ctx, src, logFile); err != nil {
		return fmt.Errorf("proto generation %s: %v, see %s", tag, err, logPath)
	}
	out, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/discopanel")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=1", "GOFLAGS=-mod=mod", "GOWORK=off", "GOTOOLCHAIN=auto")
	build.Stdout = logFile
	build.Stderr = logFile
	if err := build.Run(); err != nil {
		return fmt.Errorf("go build %s: %v, see %s", tag, err, logPath)
	}
	return nil
}

// Remote plugins rate limit by address, one generation at a time
var protoGenLock sync.Mutex

// Generates a tag's proto code the way its own makefile did
// Trees without protos have nothing to generate
func generateProtos(ctx context.Context, src string, logs io.Writer) error {
	if !exists(filepath.Join(src, "buf.gen.yaml")) {
		return nil
	}
	protoGenLock.Lock()
	defer protoGenLock.Unlock()

	// Old lock files predate deps the tag declares
	if err := bufCmd(ctx, src, logs, "dep", "update"); err != nil {
		return fmt.Errorf("buf dep update: %w", err)
	}
	makefile, err := os.ReadFile(filepath.Join(src, "Makefile"))
	if err == nil && strings.Contains(string(makefile), "\nproto:") {
		err := withBackoff(ctx, logs, func(out io.Writer) error {
			cmd := exec.CommandContext(ctx, "make", "proto")
			cmd.Dir = src
			cmd.Stdout = out
			cmd.Stderr = out
			return cmd.Run()
		})
		if err == nil {
			return nil
		}
		fmt.Fprintln(logs, "make proto failed, falling back to buf generate")
	}
	return bufCmd(ctx, src, logs, "generate")
}

// Runs buf from the host or the official image
func bufCmd(ctx context.Context, src string, logs io.Writer, args ...string) error {
	return withBackoff(ctx, logs, func(out io.Writer) error {
		var cmd *exec.Cmd
		if buf, err := exec.LookPath("buf"); err == nil {
			cmd = exec.CommandContext(ctx, buf, args...)
		} else {
			abs, err := filepath.Abs(src)
			if err != nil {
				return err
			}
			dockerArgs := []string{"run", "--rm",
				"--volume", abs + ":/workspace", "--workdir", "/workspace",
				"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--env", "HOME=/tmp",
				"bufbuild/buf:latest"}
			cmd = exec.CommandContext(ctx, "docker", append(dockerArgs, args...)...)
		}
		cmd.Dir = src
		cmd.Stdout = out
		cmd.Stderr = out
		return cmd.Run()
	})
}

// Reruns a registry call while it reports rate limiting
func withBackoff(ctx context.Context, logs io.Writer, run func(out io.Writer) error) error {
	var err error
	for attempt := 1; attempt <= 6; attempt++ {
		var captured bytes.Buffer
		err = run(io.MultiWriter(logs, &captured))
		if err == nil || !strings.Contains(captured.String(), "too many requests") {
			return err
		}
		wait := time.Duration(attempt) * 30 * time.Second
		fmt.Fprintf(logs, "registry rate limited, retrying in %s\n", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
