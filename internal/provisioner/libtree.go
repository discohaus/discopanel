package provisioner

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Caches maven library tree, deterministic per loader version

// Names the archive for one loader version
func libTreeKey(vendor, mc, version string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.' || r == '-' || r == '_':
				return r
			}
			return '-'
		}, s)
	}
	return fmt.Sprintf("%s-%s-%s.tar", clean(vendor), clean(mc), clean(version))
}

func (p *Provisioner) libTreePath(key string) string {
	return filepath.Join(p.cacheRoot(), "libtrees", key)
}

// Unpacks cached libraries so installer skips downloads
func (p *Provisioner) restoreLibTree(server *v1.Server, key string) {
	archive := p.libTreePath(key)
	f, err := os.Open(archive)
	if err != nil {
		return
	}
	defer f.Close()

	p.progress(server, "pre-seeding installer libraries from cache...")
	if err := untarInto(f, server.DataPath); err != nil {
		p.log.Warn("provisioner: library cache restore failed (%v), installer does a full run", err)
		return
	}
	touchNow(archive)
}

// Archives the installed libraries tree for future installs
func (p *Provisioner) saveLibTree(server *v1.Server, key string) {
	archive := p.libTreePath(key)
	if _, err := os.Stat(archive); err == nil {
		return
	}
	libDir := filepath.Join(server.DataPath, "libraries")
	if _, err := os.Stat(libDir); err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(archive), 0755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(archive), ".libtree-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tarDir(tmp, libDir, "libraries"); err != nil {
		tmp.Close()
		p.log.Warn("provisioner: library cache save failed: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmpPath, archive); err == nil {
		p.progress(server, "cached installer libraries for future servers")
	}
}

// Writes dir into w under prefix, files and dirs
func tarDir(w io.Writer, dir, prefix string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(prefix, rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(info.Mode().Perm()),
			})
		case info.Mode().IsRegular():
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeReg, Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(),
			}); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Extracts archive under root, rejecting path escapes
func untarInto(r io.Reader, root string) error {
	tr := tar.NewReader(r)
	cleanRoot := filepath.Clean(root)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		dest := filepath.Join(cleanRoot, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(filepath.Clean(dest), cleanRoot+string(os.PathSeparator)) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, os.FileMode(hdr.Mode).Perm()|0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if _, err := os.Stat(dest); err == nil {
				if _, err := io.Copy(io.Discard, tr); err != nil {
					return err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}

// Refreshes an archive mtime so pruning tracks usage
func touchNow(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}
