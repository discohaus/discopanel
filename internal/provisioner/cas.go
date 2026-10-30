package provisioner

import (
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Max age before untouched cache entries get pruned
const casMaxAge = 45 * 24 * time.Hour

// Cache root shared by every server
func (p *Provisioner) cacheRoot() string {
	return filepath.Join(p.cfg.Storage.DataDir, "cache")
}

// Entry path shards by hash prefix to keep directories small
func casPath(root, algo, sum string) string {
	sum = strings.ToLower(sum)
	return filepath.Join(root, "cas", algo, sum[:2], sum)
}

// Rejects unkeyed and weak checksums as cache identity
func casCacheable(sum *checksum) bool {
	if sum == nil || sum.value == "" || len(sum.value) < 8 {
		return false
	}
	switch sum.algo {
	case "sha1", "sha256", "sha512":
		return true
	}
	return false
}

// Materializes cached artifact at dest, false on miss
func (p *Provisioner) casGet(dest string, sum *checksum) bool {
	if !casCacheable(sum) {
		return false
	}
	entry := casPath(p.cacheRoot(), sum.algo, sum.value)
	if _, err := os.Stat(entry); err != nil {
		return false
	}
	if !verifyChecksum(entry, sum) {
		p.log.Warn("provisioner: cache entry %s failed verification, dropping it", entry)
		_ = os.Remove(entry)
		return false
	}
	if err := cloneOrCopy(entry, dest); err != nil {
		p.log.Warn("provisioner: cache read failed for %s: %v", entry, err)
		return false
	}
	now := time.Now()
	_ = os.Chtimes(entry, now, now)
	return true
}

// Recomputes the hash of a file against its claimed checksum
func verifyChecksum(path string, sum *checksum) bool {
	hasher := newHasher(sum.algo)
	if hasher == nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := io.Copy(hasher, f); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), sum.value)
}

// Admits a checksum verified file, best effort
func (p *Provisioner) casPut(src string, sum *checksum) {
	if !casCacheable(sum) {
		return
	}
	entry := casPath(p.cacheRoot(), sum.algo, sum.value)
	if _, err := os.Stat(entry); err == nil {
		return
	}
	if err := cloneOrCopy(src, entry); err != nil {
		p.log.Warn("provisioner: cache admit failed for %s: %v", entry, err)
	}
}

// Places src at dst atomically, reflinks when supported
func cloneOrCopy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".cas-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if !tryReflink(tmp, in) {
		if _, err := io.Copy(tmp, in); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

// Limits cache pruning to once an hour per process
var pruneGate atomic.Int64

// Drops cache artifacts and library trees nobody used lately
func (p *Provisioner) pruneCaches() {
	now := time.Now()
	last := pruneGate.Load()
	if now.Unix()-last < 3600 || !pruneGate.CompareAndSwap(last, now.Unix()) {
		return
	}
	go func() {
		root := p.cacheRoot()
		cutoff := now.Add(-casMaxAge)
		removed := 0
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().After(cutoff) {
				return nil
			}
			if os.Remove(path) == nil {
				removed++
			}
			return nil
		})
		removeEmptyDirs(root)
		if removed > 0 {
			p.log.Info("provisioner: pruned %d stale cache entries", removed)
		}
	}()
}

// Deepest first so emptied parents fall too
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}
