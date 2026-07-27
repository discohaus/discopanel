package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/transfer"
	utils "github.com/discohaus/discopanel/pkg/utils"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Compile-time check that ModService implements the interface
var _ discopanelv1connect.ModServiceHandler = (*ModService)(nil)

// ModService implements the Mod service
type ModService struct {
	store         *storage.Store
	docker        *docker.Client
	config        *config.Config
	rec           *metrics.Recorder
	log           *logger.Logger
	uploadManager *transfer.UploadManager

	cfNamesMu sync.Mutex
	cfNames   map[string]string
	cfSweeps  map[string]bool
}

// NewModService creates a new mod service
func NewModService(store *storage.Store, docker *docker.Client, cfg *config.Config, uploadManager *transfer.UploadManager, rec *metrics.Recorder, log *logger.Logger) *ModService {
	return &ModService{
		store:         store,
		docker:        docker,
		config:        cfg,
		rec:           rec,
		log:           log,
		uploadManager: uploadManager,
		cfNames:       map[string]string{},
		cfSweeps:      map[string]bool{},
	}
}

// Stable mod id from server and file name
func modEntryID(serverID, fileName string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(serverID+fileName)).String()
}

// Display name from a jar file name
func modDisplayName(fileName string) string {
	if ext := filepath.Ext(fileName); ext != "" {
		return fileName[:len(fileName)-len(ext)]
	}
	return fileName
}

// Builds mod entries for every jar in one directory
func scanModDir(serverID, dir string, loader v1.ModLoader, enabled bool) []*v1.Mod {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var mods []*v1.Mod
	for _, file := range entries {
		if file.IsDir() || !minecraft.IsValidModFile(file.Name(), loader) {
			continue
		}
		info, err := file.Info()
		if err != nil {
			continue
		}
		mod := &v1.Mod{
			Id:          modEntryID(serverID, file.Name()),
			ServerId:    serverID,
			FileName:    file.Name(),
			DisplayName: modDisplayName(file.Name()),
			Enabled:     enabled,
			FileSize:    info.Size(),
			UploadedAt:  timestamppb.New(info.ModTime()),
		}
		if meta, err := minecraft.ReadModJar(filepath.Join(dir, file.Name())); err == nil {
			for i := range meta.Mods {
				if meta.Mods[i].Declared {
					mod.ModId = meta.Mods[i].ID
					mod.Version = meta.Mods[i].Version
					break
				}
			}
		}
		mods = append(mods, mod)
	}
	return mods
}

func (s *ModService) ListMods(ctx context.Context, req *connect.Request[v1.ListModsRequest]) (*connect.Response[v1.ListModsResponse], error) {
	msg := req.Msg

	// Get server to find data path and mod loader
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Get the mods directory path
	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	mods := scanModDir(msg.ServerId, modsDir, server.ModLoader, true)
	mods = append(mods, scanModDir(msg.ServerId, modsDir+"_disabled", server.ModLoader, false)...)
	s.applyCFNames(ctx, msg.ServerId, modsDir, mods)

	return connect.NewResponse(&v1.ListModsResponse{
		Mods: mods,
	}), nil
}

// Cache key ties an identity verdict to one file state
func cfNameKey(path string, size int64) string {
	return fmt.Sprintf("%s|%d", path, size)
}

// Applies cached CurseForge names and sweeps unknown jars once
func (s *ModService) applyCFNames(ctx context.Context, serverID, modsDir string, mods []*v1.Mod) {
	apiKey := ""
	if global, _, err := s.store.GetGlobalSettings(ctx); err == nil && global != nil && global.CfApiKey != nil {
		apiKey = *global.CfApiKey
	}
	if apiKey == "" {
		return
	}

	dirFor := func(m *v1.Mod) string {
		if m.Enabled {
			return modsDir
		}
		return modsDir + "_disabled"
	}

	var unknown []*v1.Mod
	s.cfNamesMu.Lock()
	for _, m := range mods {
		key := cfNameKey(filepath.Join(dirFor(m), m.FileName), m.FileSize)
		if name, ok := s.cfNames[key]; ok {
			if name != "" {
				m.DisplayName = name
			}
		} else {
			unknown = append(unknown, m)
		}
	}
	sweeping := s.cfSweeps[serverID]
	if len(unknown) > 0 && !sweeping {
		s.cfSweeps[serverID] = true
	}
	s.cfNamesMu.Unlock()

	if len(unknown) == 0 || sweeping {
		return
	}
	paths := make(map[uint32]string, len(unknown))
	files := make([]struct {
		path string
		size int64
	}, 0, len(unknown))
	for _, m := range unknown {
		files = append(files, struct {
			path string
			size int64
		}{filepath.Join(dirFor(m), m.FileName), m.FileSize})
	}
	go s.sweepCFNames(serverID, apiKey, files, paths)
}

// Fingerprints jars and records their CurseForge project names
func (s *ModService) sweepCFNames(serverID, apiKey string, files []struct {
	path string
	size int64
}, paths map[uint32]string) {
	defer func() {
		s.cfNamesMu.Lock()
		delete(s.cfSweeps, serverID)
		s.cfNamesMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	prints := make([]uint32, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		fp := utils.CFFingerprint(data)
		paths[fp] = cfNameKey(f.path, f.size)
		prints = append(prints, fp)
	}
	if len(prints) == 0 {
		return
	}

	client := fuego.NewClient(apiKey, s.config.Server.UserAgent)
	matches, err := client.GetFingerprintMatches(ctx, prints)
	if err != nil {
		s.log.Debug("CF fingerprint sweep failed: %v", err)
		return
	}
	modByKey := map[string]int{}
	modIDs := make([]int, 0, len(matches))
	for _, m := range matches {
		key := paths[uint32(m.File.FileFingerprint)]
		if key == "" {
			continue
		}
		modByKey[key] = m.File.ModID
		modIDs = append(modIDs, m.File.ModID)
	}
	names := map[int]string{}
	if len(modIDs) > 0 {
		if mods, err := client.GetModsByIDs(ctx, modIDs); err == nil {
			for i := range mods {
				names[mods[i].ID] = mods[i].Name
			}
		}
	}

	s.cfNamesMu.Lock()
	for _, key := range paths {
		s.cfNames[key] = names[modByKey[key]]
	}
	s.cfNamesMu.Unlock()
}

// GetMod gets a specific mod
func (s *ModService) GetMod(ctx context.Context, req *connect.Request[v1.GetModRequest]) (*connect.Response[v1.GetModResponse], error) {
	msg := req.Msg

	// Get server to validate and find mod
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Get the mods directory path
	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	// Try to find the mod file in active directory
	if files, err := os.ReadDir(modsDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				// Generate the same ID as in ListMods to match
				fileID := modEntryID(msg.ServerId, file.Name())
				if fileID == msg.ModId {
					info, _ := file.Info()

					displayName := file.Name()
					if ext := filepath.Ext(displayName); ext != "" {
						displayName = displayName[:len(displayName)-len(ext)]
					}

					return connect.NewResponse(&v1.GetModResponse{
						Mod: &v1.Mod{
							Id:          fileID,
							ServerId:    msg.ServerId,
							FileName:    file.Name(),
							DisplayName: displayName,
							Enabled:     true,
							FileSize:    info.Size(),
							UploadedAt:  timestamppb.New(info.ModTime()),
						},
					}), nil
				}
			}
		}
	}

	// Try disabled directory
	disabledDir := modsDir + "_disabled"
	if files, err := os.ReadDir(disabledDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				fileID := modEntryID(msg.ServerId, file.Name())
				if fileID == msg.ModId {
					info, _ := file.Info()

					displayName := file.Name()
					if ext := filepath.Ext(displayName); ext != "" {
						displayName = displayName[:len(displayName)-len(ext)]
					}

					return connect.NewResponse(&v1.GetModResponse{
						Mod: &v1.Mod{
							Id:          fileID,
							ServerId:    msg.ServerId,
							FileName:    file.Name(),
							DisplayName: displayName,
							Enabled:     false,
							FileSize:    info.Size(),
							UploadedAt:  timestamppb.New(info.ModTime()),
						},
					}), nil
				}
			}
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, errors.New("mod not found"))
}

// ImportUploadedMod imports a mod
func (s *ModService) ImportUploadedMod(ctx context.Context, req *connect.Request[v1.ImportUploadedModRequest]) (*connect.Response[v1.ImportUploadedModResponse], error) {
	msg := req.Msg

	// Validate upload session
	if msg.UploadSessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_session_id is required"))
	}

	// Get server to find data path and mod loader
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Get temp file path and original filename from upload manager
	tempPath, originalFilename, err := s.uploadManager.GetTempPath(msg.UploadSessionId)
	if err != nil {
		s.log.Error("Failed to get upload session: %v", err)
		return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found or not completed"))
	}

	// Validate file is appropriate for this mod loader
	if !minecraft.IsValidModFile(originalFilename, server.ModLoader) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid file type for this mod loader"))
	}

	// Get the correct mods directory based on mod loader
	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	// Create mods directory if needed
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		s.log.Error("Failed to create mods directory: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create mods directory"))
	}

	// Move file from temp location to mods dir
	modPath := filepath.Join(modsDir, originalFilename)
	if err := os.Rename(tempPath, modPath); err != nil {
		if err := files.CopyFile(tempPath, modPath); err != nil {
			s.log.Error("Failed to move mod file: %v", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save mod"))
		}
		os.Remove(tempPath)
	}

	// Cleanup the upload session
	s.uploadManager.CleanupSession(msg.UploadSessionId)

	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_INSTALL, metrics.Attrs{"file": originalFilename}, "installed mod %s", originalFilename)

	// Get file info for the response
	info, err := os.Stat(modPath)
	if err != nil {
		s.log.Error("Failed to stat mod file: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get mod info"))
	}

	// Create mod record
	mod := &v1.Mod{
		Id:          modEntryID(msg.ServerId, originalFilename),
		ServerId:    msg.ServerId,
		FileName:    originalFilename,
		DisplayName: modDisplayName(originalFilename),
		Enabled:     true,
		FileSize:    info.Size(),
		UploadedAt:  timestamppb.New(info.ModTime()),
	}

	return connect.NewResponse(&v1.ImportUploadedModResponse{
		Mod:     mod,
		Message: "Mod uploaded successfully",
	}), nil
}

// UpdateMod updates a mod
func (s *ModService) UpdateMod(ctx context.Context, req *connect.Request[v1.UpdateModRequest]) (*connect.Response[v1.UpdateModResponse], error) {
	msg := req.Msg

	// Get server to find mod path
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	disabledDir := modsDir + "_disabled"

	// Find the mod file
	var modFileName string
	var currentlyEnabled bool
	var modInfo os.FileInfo

	// First, scan the mods directory
	if files, err := os.ReadDir(modsDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				// Generate the same ID as in ListMods to match
				fileID := modEntryID(msg.ServerId, file.Name())
				if fileID == msg.ModId {
					modFileName = file.Name()
					currentlyEnabled = true
					modInfo, _ = file.Info()
					break
				}
			}
		}
	}

	// If not found, check disabled directory
	if modFileName == "" {
		if files, err := os.ReadDir(disabledDir); err == nil {
			for _, file := range files {
				if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
					fileID := modEntryID(msg.ServerId, file.Name())
					if fileID == msg.ModId {
						modFileName = file.Name()
						currentlyEnabled = false
						modInfo, _ = file.Info()
						break
					}
				}
			}
		}
	}

	if modFileName == "" {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("mod not found"))
	}

	// Handle enabling/disabling
	finalEnabled := currentlyEnabled
	if msg.Enabled != nil && *msg.Enabled != currentlyEnabled {
		if *msg.Enabled {
			// Move from disabled to mods directory
			oldPath := filepath.Join(disabledDir, modFileName)
			newPath := filepath.Join(modsDir, modFileName)
			if err := os.Rename(oldPath, newPath); err != nil {
				s.log.Error("Failed to enable mod: %v", err)
				return nil, connect.NewError(connect.CodeInternal, errors.New("failed to enable mod"))
			}
			finalEnabled = true
			s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_ENABLE, metrics.Attrs{"file": modFileName}, "enabled mod %s", modFileName)
		} else {
			// Move from mods to disabled directory
			os.MkdirAll(disabledDir, 0755)
			oldPath := filepath.Join(modsDir, modFileName)
			newPath := filepath.Join(disabledDir, modFileName)
			if err := os.Rename(oldPath, newPath); err != nil {
				s.log.Error("Failed to disable mod: %v", err)
				return nil, connect.NewError(connect.CodeInternal, errors.New("failed to disable mod"))
			}
			finalEnabled = false
			s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_DISABLE, metrics.Attrs{"file": modFileName}, "disabled mod %s", modFileName)
		}
	}

	// Build response
	displayName := modFileName
	if ext := filepath.Ext(displayName); ext != "" {
		displayName = displayName[:len(displayName)-len(ext)]
	}

	return connect.NewResponse(&v1.UpdateModResponse{
		Mod: &v1.Mod{
			Id:          msg.ModId,
			ServerId:    msg.ServerId,
			FileName:    modFileName,
			DisplayName: displayName,
			Enabled:     finalEnabled,
			FileSize:    modInfo.Size(),
			UploadedAt:  timestamppb.New(modInfo.ModTime()),
			UpdatedAt:   timestamppb.Now(),
		},
	}), nil
}

// DeleteMod deletes a mod
func (s *ModService) DeleteMod(ctx context.Context, req *connect.Request[v1.DeleteModRequest]) (*connect.Response[v1.DeleteModResponse], error) {
	msg := req.Msg

	// Get server to find file path
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	// Try to find and delete the mod file
	deletedName := ""

	// Check active directory
	if files, err := os.ReadDir(modsDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				fileID := modEntryID(msg.ServerId, file.Name())
				if fileID == msg.ModId {
					modPath := filepath.Join(modsDir, file.Name())
					if err := os.Remove(modPath); err != nil {
						s.log.Error("Failed to delete mod file: %v", err)
						return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete mod file"))
					}
					deletedName = file.Name()
					break
				}
			}
		}
	}

	// Check disabled directory
	if deletedName == "" {
		disabledDir := modsDir + "_disabled"
		if files, err := os.ReadDir(disabledDir); err == nil {
			for _, file := range files {
				if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
					fileID := modEntryID(msg.ServerId, file.Name())
					if fileID == msg.ModId {
						modPath := filepath.Join(disabledDir, file.Name())
						if err := os.Remove(modPath); err != nil {
							s.log.Error("Failed to delete mod file: %v", err)
							return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete mod file"))
						}
						deletedName = file.Name()
						break
					}
				}
			}
		}
	}

	if deletedName == "" {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("mod not found"))
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_DELETE, metrics.Attrs{"file": deletedName}, "deleted mod %s", deletedName)

	return connect.NewResponse(&v1.DeleteModResponse{
		Message: "Mod deleted successfully",
	}), nil
}
