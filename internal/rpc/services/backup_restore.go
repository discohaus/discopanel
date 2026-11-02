package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/discohaus/discopanel/pkg/files"
)

// How many pre-restore safety snapshots are kept per server
const keepPreRestoreSnapshots = 3

const preRestorePrefix = "pre-restore_"

// restoreArchiveExact restores a backup so that every world it contains
// is replaced wholesale by the archive's copy, instead of extracted on top.
//
// Extracting over a live world left stale files behind (region files,
// player data and DIM folders newer than the backup survived), so the
// "restored" world was a mix of two points in time. A top-level directory
// counts as a world when the archive holds its level.dat; anything else in
// the archive (e.g. a backup of only config/ or world/region) is merged
// file by file as before, since the archive may hold only part of it.
// Paths the archive does not mention are left untouched.
//
// The archive is extracted into a staging dir inside dataPath first, so a
// corrupt or oversized backup fails before anything is changed, and the
// swap is a series of same-filesystem renames that roll back on error.
func restoreArchiveExact(ctx context.Context, archivePath, dataPath string) error {
	staging, err := os.MkdirTemp(dataPath, ".restore-*")
	if err != nil {
		return fmt.Errorf("failed to create restore staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	extracted := filepath.Join(staging, "new")
	displaced := filepath.Join(staging, "old")
	if _, err := files.ExtractArchive(ctx, archivePath, extracted, nil); err != nil {
		return fmt.Errorf("failed to extract backup: %w", err)
	}
	if err := os.MkdirAll(displaced, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(extracted)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("backup archive is empty")
	}

	type swap struct {
		name   string
		hadOld bool
	}
	var swapped []swap
	rollback := func() {
		for i := len(swapped) - 1; i >= 0; i-- {
			sw := swapped[i]
			target := filepath.Join(dataPath, sw.name)
			os.RemoveAll(target)
			if sw.hadOld {
				os.Rename(filepath.Join(displaced, sw.name), target)
			}
		}
	}

	var merges []string
	for _, e := range entries {
		name := e.Name()
		// Never let a backup replace panel-internal state
		if name == ".discopanel" || strings.HasPrefix(name, ".restore-") {
			continue
		}
		src := filepath.Join(extracted, name)
		if !e.IsDir() || !isRegularFile(filepath.Join(src, "level.dat")) {
			merges = append(merges, name)
			continue
		}
		target := filepath.Join(dataPath, name)
		sw := swap{name: name}
		if _, err := os.Lstat(target); err == nil {
			if err := os.Rename(target, filepath.Join(displaced, name)); err != nil {
				// A world dir that is its own mount (docker volume override)
				// cannot be renamed. Replace its contents in place instead;
				// the pre-restore snapshot is the undo for this path.
				if err := replaceContents(src, target); err != nil {
					rollback()
					return fmt.Errorf("failed to restore %s in place: %w", name, err)
				}
				continue
			}
			sw.hadOld = true
		}
		if err := os.Rename(src, target); err != nil {
			if sw.hadOld {
				os.Rename(filepath.Join(displaced, name), target)
			}
			rollback()
			return fmt.Errorf("failed to place restored %s: %w", name, err)
		}
		swapped = append(swapped, sw)
	}

	// Partial paths: overwrite file by file, keep everything else. On
	// failure the swapped worlds are put back; otherwise the deferred
	// staging cleanup would delete the only copy of the displaced worlds.
	for _, name := range merges {
		if err := mergeMove(filepath.Join(extracted, name), filepath.Join(dataPath, name)); err != nil {
			rollback()
			return fmt.Errorf("failed to restore %s: %w", name, err)
		}
	}
	return nil
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// replaceContents empties dst (keeping dst itself, which may be a mount
// point) and copies src's children into it.
func replaceContents(src, dst string) error {
	children, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, c := range children {
		if err := os.RemoveAll(filepath.Join(dst, c.Name())); err != nil {
			return err
		}
	}
	news, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, c := range news {
		from, to := filepath.Join(src, c.Name()), filepath.Join(dst, c.Name())
		if c.IsDir() {
			err = files.CopyDir(from, to)
		} else {
			err = files.CopyFile(from, to)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// moveFile renames, falling back to copy+remove across filesystems
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := files.CopyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// mergeMove moves src into dst, replacing files that exist in both and
// keeping files only dst has. A symlinked dst directory is followed, not
// replaced, so links users set up on purpose survive.
func mergeMove(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if di, err := os.Stat(dst); err == nil && di.IsDir() {
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return moveFile(src, dst)
	}
	if di, err := os.Stat(dst); err == nil && !di.IsDir() {
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	children, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, c := range children {
		if err := mergeMove(filepath.Join(src, c.Name()), filepath.Join(dst, c.Name())); err != nil {
			return err
		}
	}
	return nil
}

// prunePreRestoreSnapshots keeps the newest few pre-restore archives. The
// scheduler's retention only matches its own task prefix, so these used to
// pile up forever, one per restore.
func prunePreRestoreSnapshots(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), preRestorePrefix) && strings.HasSuffix(e.Name(), ".zip") {
			names = append(names, e.Name())
		}
	}
	// Timestamped names (YYYYMMDD-HHMMSS) sort chronologically
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	var firstErr error
	for i, n := range names {
		if i < keep {
			continue
		}
		if err := os.Remove(filepath.Join(dir, n)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
