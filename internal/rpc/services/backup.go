package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Backup directory for one server, empty when unconfigured
func (s *ServerService) backupDirFor(server *v1.Server) string {
	if s.config == nil || s.config.Storage.BackupDir == "" || server.DataPath == "" {
		return ""
	}
	return filepath.Join(s.config.Storage.BackupDir, filepath.Base(server.DataPath))
}

// ListBackups returns the server's archived snapshots newest first
func (s *ServerService) ListBackups(ctx context.Context, req *connect.Request[v1.ListBackupsRequest]) (*connect.Response[v1.ListBackupsResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	dir := s.backupDirFor(server)
	if dir == "" {
		return connect.NewResponse(&v1.ListBackupsResponse{}), nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return connect.NewResponse(&v1.ListBackupsResponse{}), nil
	}
	backups := make([]*v1.Backup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, &v1.Backup{
			FileName:  entry.Name(),
			Size:      info.Size(),
			CreatedAt: timestamppb.New(info.ModTime()),
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.AsTime().After(backups[j].CreatedAt.AsTime())
	})
	return connect.NewResponse(&v1.ListBackupsResponse{Backups: backups}), nil
}

// Stages an uploaded world zip into the data dir
// Level.dat testimony sets the world name and MC version
func (s *ServerService) importUploadedWorld(server *v1.Server, sessionID string) (string, error) {
	archivePath, _, err := s.uploadManager.GetTempPath(sessionID)
	if err != nil {
		return "", fmt.Errorf("upload session not found or not completed")
	}
	defer s.uploadManager.CleanupSession(sessionID)

	staging, err := os.MkdirTemp(server.DataPath, ".world-import-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if _, err := files.ExtractArchive(ctx, archivePath, staging, nil); err != nil {
		return "", fmt.Errorf("could not extract world archive: %w", err)
	}

	worldRoot, err := findLevelDatRoot(staging)
	if err != nil {
		return "", err
	}

	levelName := "world"
	if info, err := minecraft.ReadLevelDat(filepath.Join(worldRoot, "level.dat")); err == nil {
		if info.LevelName != "" {
			levelName = files.SanitizePathName(info.LevelName)
		}
		if info.VersionName != "" {
			server.McVersion = info.VersionName
		}
	}

	dest := filepath.Join(server.DataPath, levelName)
	if err := os.Rename(worldRoot, dest); err != nil {
		if err := files.CopyDir(worldRoot, dest); err != nil {
			return "", fmt.Errorf("could not place world directory: %w", err)
		}
	}
	return levelName, nil
}

// Finds the directory holding level.dat, root or one level deep
func findLevelDatRoot(staging string) (string, error) {
	if _, err := os.Stat(filepath.Join(staging, "level.dat")); err == nil {
		return staging, nil
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(staging, entry.Name())
		if _, err := os.Stat(filepath.Join(candidate, "level.dat")); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("archive holds no level.dat")
}

// RestoreBackup rewinds world files from a snapshot, stopped servers only
func (s *ServerService) RestoreBackup(ctx context.Context, req *connect.Request[v1.RestoreBackupRequest]) (*connect.Response[v1.RestoreBackupResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	dir := s.backupDirFor(server)
	if dir == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("backups are not configured"))
	}

	switch server.Status {
	case v1.ServerStatus_SERVER_STATUS_STOPPED, v1.ServerStatus_SERVER_STATUS_ERROR:
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("stop the server before restoring"))
	}

	archivePath, err := files.ResolveUnder(dir, req.Msg.FileName)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid backup name"))
	}
	if _, err := os.Stat(archivePath); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("backup not found"))
	}

	// Snapshot current world first so a restore is reversible
	if worldDirs, err := files.FindWorldDirs(server.DataPath); err == nil && len(worldDirs) > 0 {
		rels := make([]string, 0, len(worldDirs))
		for _, w := range worldDirs {
			if rel, err := filepath.Rel(server.DataPath, w); err == nil {
				rels = append(rels, rel)
			}
		}
		safety := filepath.Join(dir, fmt.Sprintf("pre-restore_%s.zip", time.Now().UTC().Format("20060102-150405")))
		if _, err := files.CreateZipArchive(rels, server.DataPath, safety, true); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to snapshot current world: %w", err))
		}
	}

	if _, err := files.ExtractArchive(ctx, archivePath, server.DataPath, nil); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to restore backup: %w", err))
	}

	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_BACKUP_RESTORE, metrics.Attrs{"file": req.Msg.FileName}, "restored backup %s", req.Msg.FileName)
	return connect.NewResponse(&v1.RestoreBackupResponse{
		Message: fmt.Sprintf("Restored %s", req.Msg.FileName),
	}), nil
}
