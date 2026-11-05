package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/provisioner"
	"github.com/discohaus/discopanel/pkg/files"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/google/uuid"
)

// Staged import trees live beside server dirs so placement is a rename
const importStagingDir = ".imports"

// Unclaimed staged imports older than this are swept
const importStagingTTL = 24 * time.Hour

// Archive names a server import accepts
var importArchiveExts = []string{".zip", ".tar.gz", ".tgz", ".tar"}

// Facts kept beside an extracted import tree
type importMeta struct {
	ArchiveName string `json:"archive_name"`
	FileCount   int    `json:"file_count"`
}

// One extracted server archive awaiting create
type stagedImport struct {
	dir     string // Staging dir, removed once placed
	root    string // Wrapper stripped server tree
	wrapper string
	meta    importMeta
}

func (s *ServerService) importStagingRoot() string {
	return filepath.Join(s.config.Storage.DataDir, "servers", importStagingDir)
}

// Staging dir for one upload session, ids are uuids so never escape
func (s *ServerService) importStagingPath(sessionID string) (string, error) {
	if _, err := uuid.Parse(sessionID); err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("invalid upload session id"))
	}
	return filepath.Join(s.importStagingRoot(), sessionID), nil
}

func hasImportArchiveExt(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range importArchiveExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// Reserves one upload session for a single analyze or create at a time
func (s *ServerService) claimServerImport(sessionID string) (func(), error) {
	if _, err := s.importStagingPath(sessionID); err != nil {
		return nil, err
	}
	if _, busy := s.importsBusy.LoadOrStore(sessionID, struct{}{}); busy {
		return nil, connect.NewError(connect.CodeAborted, errors.New("this upload is already being imported"))
	}
	return func() { s.importsBusy.Delete(sessionID) }, nil
}

// Extracts an upload once, later calls reuse the staged tree
// Callers hold the session claim
func (s *ServerService) stageServerImport(ctx context.Context, sessionID string) (*stagedImport, error) {
	dir, err := s.importStagingPath(sessionID)
	if err != nil {
		return nil, err
	}

	filesDir := filepath.Join(dir, "files")
	if info, err := os.Stat(filesDir); err == nil && info.IsDir() {
		// Reuse keeps an actively analyzed import out of the sweep
		now := time.Now()
		_ = os.Chtimes(dir, now, now)
		return loadStagedImport(dir)
	}
	s.sweepStaleImports(importStagingTTL)

	archivePath, filename, err := s.uploadManager.GetTempPath(sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found or not completed"))
	}
	// Cancel drops the session and its temp file whatever happens next
	defer s.uploadManager.Cancel(sessionID)
	if !hasImportArchiveExt(filename) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server archive must be a .zip, .tar.gz, .tgz or .tar file"))
	}

	partial := filepath.Join(dir, "partial")
	if err := os.RemoveAll(partial); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to prepare import staging"))
	}
	if err := os.MkdirAll(partial, 0755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to prepare import staging"))
	}

	// Large servers outlive a dropped client, extraction finishes anyway
	extractCtx, cancel := context.WithTimeout(detach(ctx), 30*time.Minute)
	defer cancel()
	count, err := files.ExtractArchive(extractCtx, archivePath, partial, nil)
	if err != nil {
		os.RemoveAll(dir)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("could not extract server archive: %w", err))
	}
	if count == 0 {
		os.RemoveAll(dir)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server archive is empty"))
	}

	meta := importMeta{ArchiveName: filepath.Base(filename), FileCount: count}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644); err != nil {
		os.RemoveAll(dir)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to record import staging"))
	}
	// Rename marks extraction complete for later calls
	if err := os.Rename(partial, filesDir); err != nil {
		os.RemoveAll(dir)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to finish import staging"))
	}
	s.log.Info("Staged server import %s (%s, %d files)", sessionID, meta.ArchiveName, count)
	return loadStagedImport(dir)
}

func loadStagedImport(dir string) (*stagedImport, error) {
	staged := &stagedImport{dir: dir}
	if data, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
		_ = json.Unmarshal(data, &staged.meta)
	}
	root, wrapper, err := provisioner.ImportRoot(filepath.Join(dir, "files"))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to read staged import"))
	}
	staged.root, staged.wrapper = root, wrapper
	return staged, nil
}

// Removes staged imports nobody created a server from
// Each entry is claimed before deletion so in-use trees are never swept
func (s *ServerService) sweepStaleImports(maxAge time.Duration) {
	root := s.importStagingRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Unfinished extractions never complete once their call is gone
		incomplete := e.IsDir() && !dirHas(filepath.Join(root, e.Name()), "files")
		if time.Since(info.ModTime()) < maxAge && !incomplete {
			continue
		}
		if _, busy := s.importsBusy.LoadOrStore(e.Name(), struct{}{}); busy {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err == nil {
			s.log.Info("Swept stale server import %s", e.Name())
		}
		s.importsBusy.Delete(e.Name())
	}
}

func dirHas(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && info.IsDir()
}

// Sweeps imports a previous run left behind, nothing is in use yet
func (s *ServerService) SweepImportsAtStartup() {
	if s.config == nil || s.config.Storage.DataDir == "" {
		return
	}
	s.sweepStaleImports(importStagingTTL)
}

// Name suggestion from the wrapping dir or the archive name
func (st *stagedImport) suggestedName() string {
	if st.wrapper != "" {
		return st.wrapper
	}
	name := st.meta.ArchiveName
	for _, ext := range importArchiveExts {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

// Builds the RPC summary for a detection
func importAnalysis(st *stagedImport, det *provisioner.ImportDetection) *v1.ServerImportAnalysis {
	props := provisioner.ApplyImportedProperties(&v1.ServerProperties{}, det.Properties)
	maxPlayers := 0
	if v, err := strconv.Atoi(strings.TrimSpace(det.Properties["max-players"])); err == nil && v > 0 {
		maxPlayers = v
	}
	return &v1.ServerImportAnalysis{
		SuggestedName: st.suggestedName(),
		ModLoader:     det.Loader,
		LoaderVersion: det.LoaderVersion,
		McVersion:     det.McVersion,
		LaunchTarget:  det.LaunchTarget(),
		LevelName:     det.LevelName,
		HasWorld:      det.HasWorld,
		Motd:          props["motd"],
		MaxPlayers:    int32(maxPlayers),
		Properties:    props,
		FileCount:     int32(st.meta.FileCount),
		ModCount:      int32(det.ModCount),
		PluginCount:   int32(det.PluginCount),
		Warnings:      det.Warnings,
		ArchiveName:   st.meta.ArchiveName,
	}
}

// Extracts an uploaded server archive and reports what it holds
func (s *ServerService) AnalyzeServerImport(ctx context.Context, req *connect.Request[v1.AnalyzeServerImportRequest]) (*connect.Response[v1.AnalyzeServerImportResponse], error) {
	if req.Msg.UploadSessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_session_id is required"))
	}
	release, err := s.claimServerImport(req.Msg.UploadSessionId)
	if err != nil {
		return nil, err
	}
	defer release()
	staged, err := s.stageServerImport(ctx, req.Msg.UploadSessionId)
	if err != nil {
		return nil, err
	}
	det := provisioner.DetectImportedServer(staged.root)
	return connect.NewResponse(&v1.AnalyzeServerImportResponse{
		Analysis: importAnalysis(staged, det),
	}), nil
}

// Moves the staged tree into a fresh server data dir
func placeServerImport(staged *stagedImport, dataPath string) error {
	if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("server directory is not empty")
	}
	if err := os.MkdirAll(filepath.Dir(dataPath), 0755); err != nil {
		return err
	}
	if err := os.Rename(staged.root, dataPath); err != nil {
		// Overridden data dirs may sit on another filesystem
		if err := files.CopyDir(staged.root, dataPath); err != nil {
			return fmt.Errorf("could not place imported files: %w", err)
		}
	}
	return os.RemoveAll(staged.dir)
}

// Adopts imported properties and drops inherited reinstall triggers
func applyImportSettings(cfg *v1.ServerProperties, server *v1.Server, det *provisioner.ImportDetection) {
	provisioner.ApplyImportedProperties(cfg, det.Properties)
	clearPackSelection(cfg)
	// Pins the shipped loader so a forced reprovision keeps it
	cfg.ForgeVersion = nil
	switch server.ModLoader {
	case v1.ModLoader_MOD_LOADER_FORGE, v1.ModLoader_MOD_LOADER_NEOFORGE:
		if server.ModLoader == det.Loader && det.LoaderVersion != "" {
			version := det.LoaderVersion
			cfg.ForgeVersion = &version
		}
	}
}

// Deletes a staged import and its pending upload, idempotent
func (s *ServerService) DiscardServerImport(ctx context.Context, req *connect.Request[v1.DiscardServerImportRequest]) (*connect.Response[v1.DiscardServerImportResponse], error) {
	sessionID := req.Msg.UploadSessionId
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_session_id is required"))
	}
	release, err := s.claimServerImport(sessionID)
	if err != nil {
		return nil, err
	}
	defer release()
	dir, _ := s.importStagingPath(sessionID)
	if err := os.RemoveAll(dir); err != nil {
		s.log.Error("Failed to discard server import %s: %v", sessionID, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to discard staged import"))
	}
	_ = s.uploadManager.Cancel(sessionID)
	return connect.NewResponse(&v1.DiscardServerImportResponse{}), nil
}
