package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/files"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Archives server data, pausing world saves for consistency
func (s *Scheduler) executeBackupTask(ctx context.Context, server *v1.Server, task *v1.ScheduledTask) (string, error) {
	config := task.GetBackupConfig()

	if s.appConfig == nil || s.appConfig.Storage.BackupDir == "" {
		return "", fmt.Errorf("backup directory is not configured")
	}
	if server.DataPath == "" {
		return "", fmt.Errorf("server has no data directory")
	}

	paths, missing, err := resolveBackupPaths(server.DataPath, config.Paths)
	if err != nil {
		return "", err
	}

	// Groups backups per server by data directory name
	destDir := filepath.Join(s.appConfig.Storage.BackupDir, filepath.Base(server.DataPath))
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	backupName := config.BackupName
	if backupName == "" {
		backupName = task.Name
	}
	prefix := files.SanitizePathName(backupName)
	destPath := filepath.Join(destDir, fmt.Sprintf("%s_%s.zip", prefix, time.Now().UTC().Format("20060102-150405")))

	resumeSaves := s.pauseWorldSaves(ctx, server)
	defer resumeSaves()

	start := time.Now()
	count, err := files.CreateZipArchive(paths, server.DataPath, destPath, config.Compress)
	if err != nil {
		return "", fmt.Errorf("failed to create backup archive: %w", err)
	}

	var size int64
	if info, err := os.Stat(destPath); err == nil {
		size = info.Size()
	}

	pruned, pruneErr := pruneBackups(destDir, prefix+"_", int(config.RetentionDays), int(config.MinBackups), int(config.MaxBackups))

	output := fmt.Sprintf("backup created: %s (%d files, %s, took %s)",
		filepath.Base(destPath), count, formatBytes(size), time.Since(start).Round(time.Millisecond))
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_BACKUP_CREATE,
		metrics.Attrs{"file": filepath.Base(destPath), "size": formatBytes(size), "task": task.Name},
		"backed up %s (%d files, %s, task %q)",
		filepath.Base(destPath), count, formatBytes(size), task.Name)
	if len(missing) > 0 {
		output += fmt.Sprintf("; skipped missing paths: %s", strings.Join(missing, ", "))
	}
	if pruned > 0 {
		output += fmt.Sprintf("; pruned %d old backup(s)", pruned)
	}
	if pruneErr != nil {
		output += fmt.Sprintf("; prune warning: %v", pruneErr)
	}
	return output, nil
}

// Validates requested paths, empty means the world directory
func resolveBackupPaths(dataPath string, requested []string) (paths []string, missing []string, err error) {
	cleaned := make([]string, 0, len(requested))
	for _, p := range requested {
		rel := filepath.Clean(strings.TrimSpace(p))
		if rel == "" || rel == "." {
			continue
		}
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("invalid backup path %q: must be relative to the server directory", p)
		}
		cleaned = append(cleaned, rel)
	}

	if len(cleaned) == 0 {
		// Default archives the world directory and dimension siblings
		worldDirs, err := files.FindWorldDirs(dataPath)
		if err != nil {
			return nil, nil, fmt.Errorf("no world directory found to back up (configure explicit paths for a custom layout): %w", err)
		}
		for _, dir := range worldDirs {
			rel, err := filepath.Rel(dataPath, dir)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to resolve world directory: %w", err)
			}
			paths = append(paths, rel)
		}
		return paths, nil, nil
	}

	for _, rel := range cleaned {
		if _, err := os.Stat(filepath.Join(dataPath, rel)); err != nil {
			missing = append(missing, rel)
			continue
		}
		paths = append(paths, rel)
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("none of the configured backup paths exist in the server directory")
	}
	return paths, missing, nil
}

// Pauses and flushes world saves, returns the resume function
func (s *Scheduler) pauseWorldSaves(ctx context.Context, server *v1.Server) func() {
	if server.Status != v1.ServerStatus_SERVER_STATUS_RUNNING || server.ContainerId == "" {
		return func() {}
	}

	if _, err := s.sender.SendCommand(ctx, server.Id, "save-off"); err != nil {
		s.log.Warn("Backup: failed to disable world saves on server %s (continuing anyway): %v", server.Name, err)
		return func() {}
	}

	if _, err := s.sender.SendCommand(ctx, server.Id, "save-all flush"); err != nil {
		s.log.Warn("Backup: failed to flush world saves on server %s: %v", server.Name, err)
	} else {
		// Gives the server a moment to flush chunks
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}

	return func() {
		// Fresh context re-enables saves even after cancellation
		resumeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.sender.SendCommand(resumeCtx, server.Id, "save-on"); err != nil {
			s.log.Error("Backup: failed to re-enable world saves on server %s: %v", server.Name, err)
		}
	}
}

// Prunes old backups honoring retention, min, and max caps
// Returns the number of backups removed.
func pruneBackups(dir, prefix string, retentionDays, minBackups, maxBackups int) (int, error) {
	if retentionDays <= 0 && maxBackups <= 0 {
		return 0, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("failed to read backup directory: %w", err)
	}

	type backupFile struct {
		name    string
		modTime time.Time
	}
	var backups []backupFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupFile{name: entry.Name(), modTime: info.ModTime()})
	}

	// Newest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].modTime.After(backups[j].modTime)
	})

	toDelete := make(map[string]bool)
	if maxBackups > 0 {
		for _, b := range backups[min(maxBackups, len(backups)):] {
			toDelete[b.name] = true
		}
	}
	if retentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -retentionDays)
		// Keeps the newest backups regardless of age
		protected := max(minBackups, 1)
		for _, b := range backups[min(protected, len(backups)):] {
			if b.modTime.Before(cutoff) {
				toDelete[b.name] = true
			}
		}
	}

	pruned := 0
	var firstErr error
	for name := range toDelete {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		pruned++
	}
	return pruned, firstErr
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
