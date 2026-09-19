package diagnostics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/discohaus/discopanel/pkg/files"
)

// Disk thresholds
const (
	diskFailBelow = int64(1 << 30)
	diskWarnBelow = int64(5 << 30)
	diskWarnUsage = 0.9
)

// Creates and removes a temp file to prove write access
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".discopanel-diag-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// Owner summary like uid 1000 gid 1000 mode 755
func describeOwner(p string) string {
	info, err := os.Stat(p)
	if err != nil {
		return ""
	}
	uid, gid, ok := files.Owner(p)
	if !ok {
		return fmt.Sprintf("mode %o", info.Mode().Perm())
	}
	return fmt.Sprintf("uid %d gid %d mode %o", uid, gid, info.Mode().Perm())
}

// Fix line for a folder the panel cannot write
func chownRemedy(dirs []string) string {
	if runtime.GOOS == "windows" {
		return "Grant the account running DiscoPanel Modify rights on: " + strings.Join(dirs, ", ")
	}
	return fmt.Sprintf("DiscoPanel runs as uid %d gid %d. Fix ownership with: sudo chown -R %d:%d %s (or chmod so that user can write). In compose, the container runs as root unless you set user:, so check the host folder instead.",
		os.Getuid(), os.Getgid(), os.Getuid(), os.Getgid(), strings.Join(dirs, " "))
}

// Verifies every panel directory exists and accepts writes
func (r *Runner) checkDirectories(ctx context.Context, c *check) {
	type dirCheck struct{ label, path string }
	dirs := []dirCheck{
		{"data", r.cfg.Storage.DataDir},
		{"backups", r.cfg.Storage.BackupDir},
		{"temp", r.cfg.Storage.TempDir},
		{"database", filepath.Dir(r.cfg.Database.Path)},
	}
	if r.cfg.Logging.Enabled && r.cfg.Logging.FilePath != "" {
		dirs = append(dirs, dirCheck{"logs", filepath.Dir(r.cfg.Logging.FilePath)})
	}

	seen := map[string]bool{}
	var broken []string
	for _, d := range dirs {
		if seen[d.path] {
			continue
		}
		seen[d.path] = true
		info, err := os.Stat(d.path)
		switch {
		case err != nil:
			c.note("%s: %s missing (%v)", d.label, d.path, err)
			broken = append(broken, d.path)
			continue
		case !info.IsDir():
			c.note("%s: %s is not a directory", d.label, d.path)
			broken = append(broken, d.path)
			continue
		}
		if err := probeWritable(d.path); err != nil {
			c.note("%s: %s not writable (%s), %v", d.label, d.path, describeOwner(d.path), err)
			broken = append(broken, d.path)
			continue
		}
		c.note("%s: %s ok (%s)", d.label, d.path, describeOwner(d.path))
	}
	if len(broken) > 0 {
		c.fail("%s not writable: %s", plural(len(broken), "directory", "directories"), abbreviate(broken, 3))
		c.fix(chownRemedy(broken), docsTroubleshooting)
		return
	}
	c.pass("All %d panel directories are writable", len(seen))
}

// Verifies the panel process can write every server folder
func (r *Runner) checkServerDirs(ctx context.Context, c *check) {
	servers, err := r.store.ListServers(ctx)
	if err != nil {
		c.skip("Could not list servers: %v", err)
		return
	}
	if len(servers) == 0 {
		c.skip("No servers yet")
		return
	}

	var broken, missing []string
	present := 0
	for _, s := range servers {
		if s.DataPath == "" {
			continue
		}
		if _, err := os.Stat(s.DataPath); err != nil {
			missing = append(missing, s.Name)
			c.note("%s: %s missing, recreated on next start", s.Name, s.DataPath)
			continue
		}
		present++
		if err := probeWritable(s.DataPath); err != nil {
			broken = append(broken, s.Name)
			c.note("%s: panel cannot write %s (%s)", s.Name, s.DataPath, describeOwner(s.DataPath))
			continue
		}
		c.note("%s: ok (%s)", s.Name, describeOwner(s.DataPath))
	}

	switch {
	case len(broken) > 0:
		c.fail("Panel cannot write %s: %s", plural(len(broken), "server folder", "server folders"), abbreviate(broken, 4))
		c.fix(chownRemedy([]string{filepath.Join(r.cfg.Storage.DataDir, "servers")}), docsTroubleshooting)
	case present == 0 && len(missing) > 0:
		c.info("%d server folders are missing and will be recreated on start", len(missing))
	default:
		c.pass("Panel can write all %d server folders, docker.server_write covers the containers", present)
	}
}

// Human size in GiB with one decimal
func gib(n int64) string {
	return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
}

// Human size picking the largest fitting unit
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return gib(n)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Warns before the data volume fills up
func (r *Runner) checkDiskSpace(ctx context.Context, c *check) {
	total, used, err := files.GetDiskSpace(r.cfg.Storage.DataDir)
	if err != nil {
		c.skip("Could not read disk usage for %s: %v", r.cfg.Storage.DataDir, err)
		return
	}
	free := total - used
	c.fact("data_total", gib(total))
	c.fact("data_used", gib(used))
	c.fact("data_free", gib(free))
	usage := 0.0
	if total > 0 {
		usage = float64(used) / float64(total)
	}
	c.fact("data_usage", fmt.Sprintf("%.0f%%", usage*100))

	// Backups often live on another volume
	if bt, bu, err := files.GetDiskSpace(r.cfg.Storage.BackupDir); err == nil && bt != total {
		c.fact("backup_free", gib(bt-bu))
	}

	switch {
	case free < diskFailBelow:
		c.fail("Only %s free on the data volume", gib(free))
		c.fix("Free space now. Prune old backups in each server's Tasks tab, remove unused Docker images (docker image prune), or move the data directory to a bigger disk.", "")
	case free < diskWarnBelow || usage > diskWarnUsage:
		c.warn("Data volume is %.0f%% full, %s free", usage*100, gib(free))
		c.fix("Modpack installs and world growth need headroom. Prune backups, run docker image prune, or expand the volume.", "")
	default:
		c.pass("%s free on the data volume (%.0f%% used)", gib(free), usage*100)
	}
}

// Integrity and journal facts for the sqlite database
func (r *Runner) checkDatabase(ctx context.Context, c *check) {
	c.fact("path", r.cfg.Database.Path)
	if info, err := os.Stat(r.cfg.Database.Path); err == nil {
		c.fact("size", humanBytes(info.Size()))
	}
	db := r.store.DB().WithContext(ctx)

	var mode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err == nil {
		c.fact("journal_mode", mode)
	}
	var quick string
	if err := db.Raw("PRAGMA quick_check").Scan(&quick).Error; err != nil {
		c.fail("Integrity check could not run: %v", err)
		c.fix("The database file may be locked or corrupt. Stop the panel, back up discopanel.db, and run sqlite3 discopanel.db 'PRAGMA integrity_check'.", "")
		return
	}
	if quick != "ok" {
		c.fail("Integrity check reported: %s", quick)
		c.fix("Stop the panel, keep a copy of discopanel.db, then restore the automatic backup written beside it or run .recover with the sqlite3 shell.", "")
		return
	}
	if drift := r.store.SchemaDrift(); len(drift) > 0 {
		for _, line := range drift {
			c.note("%s", line)
		}
		c.warn("Schema differs from the models in %d places", len(drift))
		c.fix("A downgrade or a hand edited database leaves drift behind. Upgrading to the newest release usually reconciles it, otherwise report it with a support bundle.", docsFAQ)
		return
	}
	if err := probeWritable(filepath.Dir(r.cfg.Database.Path)); err != nil {
		c.fail("Database directory is not writable: %v", err)
		c.fix(chownRemedy([]string{filepath.Dir(r.cfg.Database.Path)}), docsTroubleshooting)
		return
	}
	c.pass("Database is healthy (%s journal)", mode)
}
