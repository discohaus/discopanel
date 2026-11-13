package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/indexers"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Unauthenticated pack download path, the {id} segment is the pack id
const PublicModpackDownloadPattern = "/api/v1/modpacks/{id}/download"

// Reported for packs that are missing, private or have no local archive
var ErrModpackNotPublic = errors.New("modpack is not publicly downloadable")

// Both pack formats are zips, served with a fixed type
const modpackArchiveContentType = "application/zip"

// Directory that holds every stored pack archive
func ModpackArchiveDir(dataDir string) string {
	return filepath.Join(dataDir, "modpacks")
}

// Public download path for one pack
func publicModpackDownloadPath(modpackID string) string {
	return strings.Replace(PublicModpackDownloadPattern, "{id}", url.PathEscape(modpackID), 1)
}

// Archive chosen for a pack download, local or remote
type modpackArchive struct {
	path     string // Local file, empty when remote
	url      string // Indexer download URL, empty when local
	filename string
	size     int64
}

// Picks the requested file, else the latest, else the newest row
func pickModpackFile(packFiles []*v1.IndexedModpackFile, fileID, latestID string) *v1.IndexedModpackFile {
	if fileID != "" {
		for _, f := range packFiles {
			if f.Id == fileID {
				return f
			}
		}
		return nil
	}
	for _, f := range packFiles {
		if latestID != "" && f.Id == latestID {
			return f
		}
	}
	if len(packFiles) > 0 {
		return packFiles[0]
	}
	return nil
}

// Resolves a stored archive path, which must stay under the pack archive dir
func localModpackArchive(dataDir, stored string) (string, os.FileInfo, error) {
	base, err := filepath.Abs(ModpackArchiveDir(dataDir))
	if err != nil {
		return "", nil, err
	}
	full, err := filepath.Abs(stored)
	if err != nil {
		return "", nil, err
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == "." {
		return "", nil, fmt.Errorf("archive path outside modpacks dir: %s", stored)
	}
	resolved, err := files.ResolveUnder(base, rel)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("archive is not a regular file: %s", stored)
	}
	return resolved, info, nil
}

// Download filename from the file row, falling back to the stored name
func modpackArchiveFilename(file *v1.IndexedModpackFile, fallback string) string {
	name := filepath.Base(filepath.Clean("/" + strings.ReplaceAll(file.FileName, "\\", "/")))
	if name == "" || name == "/" || name == "." {
		name = filepath.Base(fallback)
	}
	return name
}

// Finds the archive behind a pack file without proxying remote ones
func (s *ModpackService) resolveModpackArchive(ctx context.Context, modpack *v1.IndexedModpack, fileID string) (*modpackArchive, error) {
	packFiles, err := s.store.GetIndexedModpackFiles(ctx, modpack.Id)
	if err != nil {
		s.log.Error("Failed to get modpack files for %s: %v", modpack.Id, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get modpack files"))
	}
	file := pickModpackFile(packFiles, fileID, modpack.LatestFileId)

	// Indexed packs rarely have synced rows, ask the indexer like GetModpackVersions
	if file == nil && modpack.Indexer != indexers.ManualIndexer {
		indexerClient, err := s.getIndexer(ctx, modpack.Indexer)
		if err != nil {
			return nil, err
		}
		remote, err := indexerClient.GetModpackFiles(ctx, modpack.IndexerId, "", "")
		if err != nil {
			s.log.Error("Failed to get modpack files from %s: %v", modpack.Indexer, err)
			return nil, mapIndexerError(err, "failed to get modpack files")
		}
		file = pickModpackFile(remote, fileID, modpack.LatestFileId)
	}
	if file == nil {
		if modpack.Indexer == indexers.ManualIndexer {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("modpack has no stored archive"))
		}
		return nil, connect.NewError(connect.CodeNotFound, errors.New("modpack file not found"))
	}

	source := strings.TrimSpace(file.DownloadUrl)
	if source == "" {
		// Authors can opt out of third party downloads on some platforms
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("modpack file has no direct download, get it from the modpack page"))
	}

	// Remote archives go straight to the client
	if strings.Contains(source, "://") {
		u, err := url.Parse(source)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("modpack file has an unsupported download URL"))
		}
		return &modpackArchive{
			url:      u.String(),
			filename: modpackArchiveFilename(file, u.Path),
			size:     file.FileLength,
		}, nil
	}

	path, info, err := localModpackArchive(s.config.Storage.DataDir, source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("modpack archive is missing from disk"))
		}
		s.log.Error("Rejected modpack archive path for %s: %v", modpack.Id, err)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("modpack archive is not stored on this panel"))
	}
	return &modpackArchive{
		path:     path,
		filename: modpackArchiveFilename(file, path),
		size:     info.Size(),
	}, nil
}

// Creates download session for a pack archive, remote archives return their URL
func (s *ModpackService) InitModpackDownload(ctx context.Context, req *connect.Request[v1.InitModpackDownloadRequest]) (*connect.Response[v1.InitModpackDownloadResponse], error) {
	modpack, err := s.store.GetIndexedModpack(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("modpack not found"))
	}

	archive, err := s.resolveModpackArchive(ctx, modpack, req.Msg.FileId)
	if err != nil {
		return nil, err
	}

	if archive.url != "" {
		return connect.NewResponse(&v1.InitModpackDownloadResponse{
			DownloadUrl: archive.url,
			Filename:    archive.filename,
			TotalSize:   archive.size,
			Remote:      true,
		}), nil
	}

	// Point session at the stored archive, never deleted on expiry
	session := s.downloadManager.InitTypedSession(archive.path, archive.filename, modpackArchiveContentType, archive.size, false)

	return connect.NewResponse(&v1.InitModpackDownloadResponse{
		SessionId:   session.ID,
		DownloadUrl: "/api/v1/download/" + session.ID,
		Filename:    archive.filename,
		TotalSize:   archive.size,
	}), nil
}

// Opts an uploaded pack in or out of its public download link
func (s *ModpackService) SetModpackPublicDownload(ctx context.Context, req *connect.Request[v1.SetModpackPublicDownloadRequest]) (*connect.Response[v1.SetModpackPublicDownloadResponse], error) {
	modpack, err := s.store.GetIndexedModpack(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("modpack not found"))
	}

	if req.Msg.Enabled {
		// Only archives hosted here can be shared, indexed packs link their own pages
		if modpack.Indexer != indexers.ManualIndexer {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("only uploaded modpacks can be shared publicly"))
		}
		if _, err := s.resolveModpackArchive(ctx, modpack, ""); err != nil {
			return nil, err
		}
	}

	if err := s.store.SetModpackPublicDownload(ctx, modpack.Id, req.Msg.Enabled); err != nil {
		s.log.Error("Failed to set public download for %s: %v", modpack.Id, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update public download"))
	}
	s.log.Info("Public download %s for modpack %s (%s)", map[bool]string{true: "enabled", false: "disabled"}[req.Msg.Enabled], modpack.Name, modpack.Id)

	resp := &v1.SetModpackPublicDownloadResponse{PublicDownload: req.Msg.Enabled}
	if req.Msg.Enabled {
		resp.PublicPath = publicModpackDownloadPath(modpack.Id)
	}
	return connect.NewResponse(resp), nil
}

// Local archive for a pack explicitly marked public, for the unauthenticated route
func (s *ModpackService) PublicModpackArchive(ctx context.Context, modpackID string) (string, string, error) {
	public, err := s.store.IsModpackPublicDownload(ctx, modpackID)
	if err != nil {
		return "", "", err
	}
	if !public {
		return "", "", ErrModpackNotPublic
	}
	modpack, err := s.store.GetIndexedModpack(ctx, modpackID)
	if err != nil || modpack.Indexer != indexers.ManualIndexer {
		return "", "", ErrModpackNotPublic
	}
	archive, err := s.resolveModpackArchive(ctx, modpack, "")
	if err != nil || archive.path == "" {
		if err != nil {
			s.log.Warn("Public modpack %s has no servable archive: %v", modpackID, err)
		}
		return "", "", ErrModpackNotPublic
	}
	return archive.path, archive.filename, nil
}

// Flags public download rows in place
func (s *ModpackService) markPublicDownloads(ctx context.Context, modpacks []*v1.IndexedModpack) {
	public, err := s.store.PublicDownloadModpackIDs(ctx)
	if err != nil {
		s.log.Warn("Failed to load public download modpacks: %v", err)
		return
	}
	for _, modpack := range modpacks {
		modpack.PublicDownload = public[modpack.Id]
	}
}
