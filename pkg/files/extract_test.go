package files

import (
	"archive/zip"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name string
	body string
	mode fs.FileMode
}

func writeZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0644
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

func TestExtractArchiveSkipsSymlinks(t *testing.T) {
	archive := writeZip(t, []zipEntry{
		{name: "world/level.dat", body: "data"},
		{name: "evil", body: "/etc", mode: fs.ModeSymlink | 0777},
	})
	dest := t.TempDir()
	n, err := ExtractArchive(context.Background(), archive, dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("extracted %d files, want 1", n)
	}
	if _, err := os.Lstat(filepath.Join(dest, "evil")); !os.IsNotExist(err) {
		t.Fatalf("symlink entry was materialised: %v", err)
	}
}

func TestExtractArchiveStripsSpecialBits(t *testing.T) {
	archive := writeZip(t, []zipEntry{{name: "run.sh", body: "#!/bin/sh", mode: fs.ModeSetuid | 0755}})
	dest := t.TempDir()
	if _, err := ExtractArchive(context.Background(), archive, dest, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSetuid != 0 {
		t.Fatalf("setuid bit survived extraction: %v", info.Mode())
	}
}

func TestExtractArchiveByteLimit(t *testing.T) {
	old := ExtractMaxBytes
	ExtractMaxBytes = 1000
	defer func() { ExtractMaxBytes = old }()

	archive := writeZip(t, []zipEntry{
		{name: "a", body: strings.Repeat("x", 600)},
		{name: "b", body: strings.Repeat("y", 600)},
	})
	_, err := ExtractArchive(context.Background(), archive, t.TempDir(), nil)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("err = %v, want ErrArchiveTooLarge", err)
	}
}

func TestExtractArchiveFileLimit(t *testing.T) {
	old := ExtractMaxFiles
	ExtractMaxFiles = 2
	defer func() { ExtractMaxFiles = old }()

	archive := writeZip(t, []zipEntry{{name: "a"}, {name: "b"}, {name: "c"}})
	_, err := ExtractArchive(context.Background(), archive, t.TempDir(), nil)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("err = %v, want ErrArchiveTooLarge", err)
	}
}

func TestExtractArchiveExactBudgetFits(t *testing.T) {
	old := ExtractMaxBytes
	ExtractMaxBytes = 1200
	defer func() { ExtractMaxBytes = old }()

	archive := writeZip(t, []zipEntry{
		{name: "a", body: strings.Repeat("x", 600)},
		{name: "b", body: strings.Repeat("y", 600)},
	})
	if _, err := ExtractArchive(context.Background(), archive, t.TempDir(), nil); err != nil {
		t.Fatalf("archive at exactly the limit rejected: %v", err)
	}
}
