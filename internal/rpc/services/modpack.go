package services

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/indexers"
	_ "github.com/discohaus/discopanel/pkg/indexers/all"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/transfer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Compile-time check that ModpackService implements the interface
var _ discopanelv1connect.ModpackServiceHandler = (*ModpackService)(nil)

// Implements the Modpack service
type ModpackService struct {
	store         *storage.Store
	config        *config.Config
	log           *logger.Logger
	uploadManager *transfer.UploadManager
}

// Creates a new modpack service
func NewModpackService(store *storage.Store, cfg *config.Config, uploadManager *transfer.UploadManager, log *logger.Logger) *ModpackService {
	return &ModpackService{
		store:         store,
		config:        cfg,
		log:           log,
		uploadManager: uploadManager,
	}
}

// Creates indexer by name with its declared credential
func (s *ModpackService) getIndexer(ctx context.Context, name string) (indexers.ModpackIndexer, error) {
	apiKey := ""
	if info, ok := indexers.LookupIndexer(name); ok && info.CredentialProperty != "" {
		globalSettings, _, err := s.store.GetGlobalSettings(ctx)
		if err != nil || globalSettings == nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get global settings"))
		}
		apiKey = propertyValueByKey(globalSettings, info.CredentialProperty)
		if apiKey == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("indexer %q requires the %s setting", name, info.CredentialProperty))
		}
	}
	idx, err := indexers.NewIndexer(name, apiKey, s.config.Server.UserAgent)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return idx, nil
}

// Maps IndexerError kinds to connect error codes
func mapIndexerError(err error, msg string) *connect.Error {
	var ie *indexers.IndexerError
	if errors.As(err, &ie) {
		switch ie.Kind {
		case indexers.ErrRateLimit:
			return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%s: %w", msg, err))
		case indexers.ErrAuth:
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s: %w", msg, err))
		case indexers.ErrNotFound:
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: %w", msg, err))
		case indexers.ErrNetwork:
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s: %w", msg, err))
		}
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", msg, err))
}

// Searches for modpacks
func (s *ModpackService) SearchModpacks(ctx context.Context, req *connect.Request[v1.SearchModpacksRequest]) (*connect.Response[v1.SearchModpacksResponse], error) {
	msg := req.Msg

	// Default pagination
	page := int(msg.Page)
	if page < 1 {
		page = 1
	}
	pageSize := int(msg.PageSize)
	if pageSize <= 0 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	// Search in local database
	modpacks, total, err := s.store.SearchIndexedModpacks(ctx, msg.Query, msg.GameVersion, msg.ModLoader, msg.Indexer, offset, pageSize)
	if err != nil {
		s.log.Error("Failed to search modpacks: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to search modpacks: %w", err))
	}

	// Flag favorited rows
	favorites, _ := s.store.FavoriteModpackIDs(ctx)
	for _, modpack := range modpacks {
		modpack.IsFavorited = favorites[modpack.Id]
	}

	return connect.NewResponse(&v1.SearchModpacksResponse{
		Modpacks: modpacks,
		Total:    int32(total),
		Page:     int32(page),
		PageSize: int32(pageSize),
	}), nil
}

// Gets a specific modpack
func (s *ModpackService) GetModpack(ctx context.Context, req *connect.Request[v1.GetModpackRequest]) (*connect.Response[v1.GetModpackResponse], error) {
	modpack, err := s.store.GetIndexedModpack(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("modpack not found"))
	}

	// Check if favorited
	modpack.IsFavorited, _ = s.store.IsModpackFavorited(ctx, req.Msg.Id)

	return connect.NewResponse(&v1.GetModpackResponse{
		Modpack: modpack,
	}), nil
}

// Gets a modpack by its website URL
func (s *ModpackService) GetModpackByURL(ctx context.Context, req *connect.Request[v1.GetModpackByURLRequest]) (*connect.Response[v1.GetModpackByURLResponse], error) {
	modpack, err := s.store.GetModpackByWebsiteURL(ctx, req.Msg.Url)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to lookup modpack"))
	}

	if modpack == nil {
		return connect.NewResponse(&v1.GetModpackByURLResponse{}), nil
	}

	return connect.NewResponse(&v1.GetModpackByURLResponse{
		Modpack: modpack,
	}), nil
}

// Gets modpack configuration
func (s *ModpackService) GetModpackConfig(ctx context.Context, req *connect.Request[v1.GetModpackConfigRequest]) (*connect.Response[v1.GetModpackConfigResponse], error) {
	modpack, err := s.store.GetIndexedModpack(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("modpack not found"))
	}

	modLoader := ""
	if loader, ok := minecraft.LoaderForPackSource(indexers.PackSourceFor(modpack.Indexer)); ok {
		modLoader = protometa.Name(loader)
	}

	config := map[string]string{
		"name":         modpack.Name,
		"description":  modpack.Summary,
		"mod_loader":   modLoader,
		"mc_version":   modpack.McVersion,
		"memory":       strconv.Itoa(int(modpack.RecommendedRam)),
		"docker_image": modpack.DockerImage,
		"modpack_file": modpack.LatestFileId, // For manual modpacks, this is the file ID
	}

	return connect.NewResponse(&v1.GetModpackConfigResponse{
		Config: config,
	}), nil
}

// Gets modpack versions
func (s *ModpackService) GetModpackVersions(ctx context.Context, req *connect.Request[v1.GetModpackVersionsRequest]) (*connect.Response[v1.GetModpackVersionsResponse], error) {
	// Get the modpack to determine its type
	modpack, err := s.store.GetIndexedModpack(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("modpack not found"))
	}

	// Manual modpacks have no remote versions
	if modpack.Indexer == indexers.ManualIndexer {
		return connect.NewResponse(&v1.GetModpackVersionsResponse{
			Versions: []*v1.Version{},
		}), nil
	}

	indexerClient, err := s.getIndexer(ctx, modpack.Indexer)
	if err != nil {
		return nil, err
	}

	// Get files from the indexer, honoring the request filters
	files, err := indexerClient.GetModpackFiles(ctx, modpack.IndexerId, req.Msg.GameVersion, req.Msg.ModLoader)
	if err != nil {
		s.log.Error("Failed to get modpack files from %s: %v", modpack.Indexer, err)
		return nil, mapIndexerError(err, "failed to get modpack versions")
	}

	// Convert files to versions, adapter order is newest first
	versions := make([]*v1.Version, 0, len(files))
	for i, file := range files {
		versions = append(versions, &v1.Version{
			Id:          file.Id,
			DisplayName: file.DisplayName,
			ReleaseType: file.ReleaseType,
			FileDate:    file.FileDate,
			SortIndex:   int32(i),
		})
	}

	return connect.NewResponse(&v1.GetModpackVersionsResponse{
		Versions: versions,
	}), nil
}

// Syncs modpacks
func (s *ModpackService) SyncModpacks(ctx context.Context, req *connect.Request[v1.SyncModpacksRequest]) (*connect.Response[v1.SyncModpacksResponse], error) {
	msg := req.Msg

	// Default to fuego if no indexer specified
	indexer := msg.Indexer
	if indexer == "" {
		indexer = "fuego"
	}

	indexerClient, err := s.getIndexer(ctx, indexer)
	if err != nil {
		return nil, err
	}

	// Search modpacks using the indexer
	searchResp, err := indexerClient.SearchModpacks(ctx, msg.Query, msg.GameVersion, msg.ModLoader, 0, 50)
	if err != nil {
		s.log.Error("Failed to search %s: %v", indexer, err)
		return nil, mapIndexerError(err, fmt.Sprintf("failed to search %s", indexer))
	}

	// Store modpacks in database
	synced := 0
	for _, modpack := range searchResp.Modpacks {
		// Finds most recent Minecraft version from game versions
		mcVersion := minecraft.FindMostRecentMinecraftVersion(modpack.GameVersions)

		// Computed fields
		modpack.McVersion = mcVersion
		modpack.JavaVersion = int32(docker.RequiredJavaMajor(mcVersion))
		modpack.DockerImage = docker.OptimalRuntimeTag(mcVersion)
		modpack.RecommendedRam = 6144 // 6GB for modpacks

		if err := s.store.UpsertIndexedModpack(ctx, modpack); err != nil {
			s.log.Error("Failed to store modpack %s: %v", modpack.Id, err)
			continue
		}
		synced++
	}

	return connect.NewResponse(&v1.SyncModpacksResponse{
		SyncedCount: int32(synced),
		Message:     fmt.Sprintf("Synced %d of %d modpacks", synced, searchResp.Total),
	}), nil
}

// Imports a modpack from a completed chunked upload session
func (s *ModpackService) ImportUploadedModpack(ctx context.Context, req *connect.Request[v1.ImportUploadedModpackRequest]) (*connect.Response[v1.ImportUploadedModpackResponse], error) {
	msg := req.Msg

	s.log.Info("Starting modpack upload import with sessionId: %s", msg.UploadSessionId)

	// Validate upload session
	if msg.UploadSessionId == "" {
		s.log.Error("Error during modpack upload import: upload_session_id is empty")
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upload_session_id is required"))
	}

	// Get temp file path and original filename from upload manager
	tempPath, originalFilename, err := s.uploadManager.GetTempPath(msg.UploadSessionId)
	if err != nil {
		s.log.Error("Failed to get upload session %s: %v", msg.UploadSessionId, err)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("upload session not found or not completed"))
	}

	var modpackID string

	cleanupOnError := func() {
		if tempPath != "" { // Remove tmp file just in case session wiped by goroutine
			if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
				s.log.Error("Failed to remove temp file %s: %v", tempPath, err)
			}
		}
		s.uploadManager.Cancel(msg.UploadSessionId)
		if modpackID != "" {
			if err := s.store.DeleteIndexedModpack(ctx, modpackID); err != nil {
				s.log.Error("Failed to cleanup modpack %s from database: %v", modpackID, err)
			}
		}
	}

	// Validate file extension
	if !strings.HasSuffix(strings.ToLower(originalFilename), ".zip") {
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("modpack must be a ZIP file"))
	}

	// Open zip file for reading
	zipReader, err := zip.OpenReader(tempPath)
	if err != nil {
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid ZIP file"))
	}
	defer zipReader.Close()

	// Look for manifest.json to determine modpack type
	var manifestFile *zip.File
	for _, f := range zipReader.File {
		if f.Name == "manifest.json" || strings.HasSuffix(f.Name, "/manifest.json") {
			manifestFile = f
			break
		}
	}

	if manifestFile == nil {
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no manifest.json found in modpack"))
	}

	// Read manifest
	manifestReader, err := manifestFile.Open()
	if err != nil {
		s.log.Error("Failed to open manifest: %v", err)
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read manifest"))
	}
	defer manifestReader.Close()

	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Author      string `json:"author"`
		Description string `json:"description,omitempty"`
		Minecraft   struct {
			Version    string `json:"version"`
			ModLoaders []struct {
				ID      string `json:"id"`
				Primary bool   `json:"primary"`
			} `json:"modLoaders"`
		} `json:"minecraft"`
	}

	if err := json.NewDecoder(manifestReader).Decode(&manifest); err != nil {
		s.log.Error("Failed to parse manifest: %v", err)
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid manifest.json"))
	}

	// Generate ID for the uploaded modpack
	modpackID = uuid.New().String()

	// Determine mod loader from manifest
	var modLoader v1.ModLoader
	loaderID := ""
	for _, ml := range manifest.Minecraft.ModLoaders {
		if loaderID == "" || ml.Primary {
			loaderID = ml.ID
		}
	}
	if loader, _, ok := minecraft.CutPackLoaderID(loaderID); ok {
		modLoader = loader
	}

	if modLoader == v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		modLoader = v1.ModLoader_MOD_LOADER_CUSTOM
	}

	javaVersion := docker.RequiredJavaMajor(manifest.Minecraft.Version)
	dockerImage := docker.OptimalRuntimeTag(manifest.Minecraft.Version)

	dbModpack := &v1.IndexedModpack{
		Id:             modpackID,
		IndexerId:      modpackID,
		Indexer:        indexers.ManualIndexer,
		Name:           manifest.Name,
		Slug:           strings.ToLower(strings.ReplaceAll(manifest.Name, " ", "-")),
		Summary:        fmt.Sprintf("Version %s by %s", manifest.Version, manifest.Author),
		Description:    manifest.Description,
		LogoUrl:        "", // No logo for manual uploads
		WebsiteUrl:     "",
		DownloadCount:  0,
		GameVersions:   []string{manifest.Minecraft.Version},
		ModLoaders:     []string{protometa.Name(modLoader)},
		LatestFileId:   modpackID,
		DateCreated:    timestamppb.Now(),
		DateModified:   timestamppb.Now(),
		DateReleased:   timestamppb.Now(),
		McVersion:      manifest.Minecraft.Version,
		JavaVersion:    int32(javaVersion),
		DockerImage:    dockerImage,
		RecommendedRam: 6144, // 6GB for modpacks
	}

	if err := s.store.UpsertIndexedModpack(ctx, dbModpack); err != nil {
		s.log.Error("Failed to store modpack: %v", err)
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store modpack"))
	}

	// Creates manual modpacks dir, stores zip there
	manualDir := filepath.Join(s.config.Storage.DataDir, "modpacks", "manual")
	if err := os.MkdirAll(manualDir, 0755); err != nil {
		s.log.Error("Failed to create manual modpacks directory: %v", err)
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store modpack file"))
	}

	// Get file size before moving
	fileInfo, err := os.Stat(tempPath)
	if err != nil {
		s.log.Error("Failed to stat temp file: %v", err)
		cleanupOnError()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to process upload"))
	}
	fileSize := fileInfo.Size()

	// Move file from temp location to storage
	destPath := filepath.Join(manualDir, modpackID+".zip")
	if err := os.Rename(tempPath, destPath); err != nil {
		// If rename fails (cross-device), fall back to copy
		if err := files.CopyFile(tempPath, destPath); err != nil {
			s.log.Error("Failed to move modpack file: %v", err)
			cleanupOnError()
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store modpack file"))
		}
		os.Remove(tempPath)
	}

	// Cleanup the upload session
	s.uploadManager.CleanupSession(msg.UploadSessionId)

	// Create a file entry for the uploaded modpack
	dbFile := &v1.IndexedModpackFile{
		Id:               modpackID,
		ModpackId:        modpackID,
		DisplayName:      originalFilename,
		FileName:         originalFilename,
		FileDate:         timestamppb.Now(),
		FileLength:       fileSize,
		ReleaseType:      v1.ReleaseType_RELEASE_TYPE_RELEASE,
		DownloadUrl:      destPath, // Store local path
		GameVersions:     []string{manifest.Minecraft.Version},
		ModLoader:        protometa.Name(modLoader),
		ServerPackFileId: nil,
	}

	if err := s.store.UpsertIndexedModpackFile(ctx, dbFile); err != nil {
		s.log.Error("Failed to store modpack file entry: %v", err)
		// Don't fail the upload, just log the error
	}

	s.log.Info("Successfully imported uploaded modpack '%s' (id: %s) from session %s", manifest.Name, modpackID, msg.UploadSessionId)

	return connect.NewResponse(&v1.ImportUploadedModpackResponse{
		Modpack: dbModpack,
		Message: fmt.Sprintf("Modpack '%s' v%s by %s uploaded successfully", manifest.Name, manifest.Version, manifest.Author),
	}), nil
}

// Deletes a modpack
func (s *ModpackService) DeleteModpack(ctx context.Context, req *connect.Request[v1.DeleteModpackRequest]) (*connect.Response[v1.DeleteModpackResponse], error) {
	modpackID := req.Msg.Id

	// Verifies modpack exists and is manual
	modpack, err := s.store.GetIndexedModpack(ctx, modpackID)
	if err != nil {
		s.log.Error("Failed to get modpack: %v", err)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("modpack not found"))
	}

	// Only allow deletion of manual modpacks
	if modpack.Indexer != indexers.ManualIndexer {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("only custom uploaded modpacks can be deleted"))
	}

	// Check if any servers are using this modpack
	serversInUse, err := s.store.CheckModpackInUse(ctx, modpackID)
	if err != nil {
		s.log.Error("Failed to check modpack usage: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check modpack usage"))
	}

	if len(serversInUse) > 0 {
		serverNames := make([]string, len(serversInUse))
		for i, srv := range serversInUse {
			serverNames[i] = srv.Name
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("cannot delete modpack: currently in use by servers: %s", strings.Join(serverNames, ", ")))
	}

	// Get modpack files
	files, err := s.store.GetIndexedModpackFiles(ctx, modpackID)
	if err != nil {
		s.log.Error("Failed to get modpack files: %v", err)
	}

	// Delete the physical ZIP file
	if len(files) > 0 && files[0].DownloadUrl != "" {
		filePath := files[0].DownloadUrl
		if err := os.Remove(filePath); err != nil {
			s.log.Error("Failed to delete modpack file %s: %v", filePath, err)
			// Continue with database deletion even if file deletion fails
		} else {
			s.log.Info("Deleted modpack file: %s", filePath)
		}
	}

	// Delete from database
	if err := s.store.DeleteIndexedModpack(ctx, modpackID); err != nil {
		s.log.Error("Failed to delete modpack from database: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete modpack"))
	}

	s.log.Info("Successfully deleted modpack %s (%s)", modpack.Name, modpackID)
	return connect.NewResponse(&v1.DeleteModpackResponse{
		Message: fmt.Sprintf("Modpack '%s' deleted successfully", modpack.Name),
	}), nil
}

// Toggles modpack favorite status
func (s *ModpackService) ToggleFavorite(ctx context.Context, req *connect.Request[v1.ToggleFavoriteRequest]) (*connect.Response[v1.ToggleFavoriteResponse], error) {
	modpackID := req.Msg.Id

	// Check if already favorited
	isFavorited, err := s.store.IsModpackFavorited(ctx, modpackID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check favorite status"))
	}

	if isFavorited {
		// Remove favorite
		if err := s.store.RemoveModpackFavorite(ctx, modpackID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to remove favorite"))
		}
	} else {
		// Add favorite
		if err := s.store.CreateModpackFavorite(ctx, &v1.ModpackFavorite{ModpackId: modpackID}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to add favorite"))
		}
	}

	return connect.NewResponse(&v1.ToggleFavoriteResponse{
		IsFavorited: !isFavorited,
		Message:     fmt.Sprintf("Modpack %s", map[bool]string{true: "unfavorited", false: "favorited"}[isFavorited]),
	}), nil
}

// Lists favorite modpacks
func (s *ModpackService) ListFavorites(ctx context.Context, req *connect.Request[v1.ListFavoritesRequest]) (*connect.Response[v1.ListFavoritesResponse], error) {
	favorites, err := s.store.ListFavoriteModpacks(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list favorites"))
	}

	// Unwrap preloaded modpack rows, skip dangling favorites
	modpacks := make([]*v1.IndexedModpack, 0, len(favorites))
	for _, fav := range favorites {
		if fav.Modpack == nil {
			continue
		}
		fav.Modpack.IsFavorited = true
		modpacks = append(modpacks, fav.Modpack)
	}

	return connect.NewResponse(&v1.ListFavoritesResponse{
		Modpacks: modpacks,
	}), nil
}

// Gets indexer status
func (s *ModpackService) GetIndexerStatus(ctx context.Context, req *connect.Request[v1.GetIndexerStatusRequest]) (*connect.Response[v1.GetIndexerStatusResponse], error) {
	// Get global settings to check API key
	globalSettings, _, err := s.store.GetGlobalSettings(ctx)
	if err != nil {
		s.log.Error("Failed to get global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get global settings"))
	}

	apiKeyConfigured := false
	if globalSettings.CfApiKey != nil && *globalSettings.CfApiKey != "" {
		apiKeyConfigured = true
	}

	// Count modpacks per indexer in the database
	modpacksByIndexer := make(map[string]int32)
	totalModpacks := int32(0)
	if counts, err := s.store.CountIndexedModpacksByIndexer(ctx); err == nil {
		for indexer, count := range counts {
			modpacksByIndexer[indexer] = int32(count)
			totalModpacks += int32(count)
		}
	}

	indexersAvailable := map[string]bool{
		"fuego":                apiKeyConfigured,
		"modrinth":             true, // Modrinth doesn't require API key
		indexers.ManualIndexer: true, // Manual uploads always available
	}

	return connect.NewResponse(&v1.GetIndexerStatusResponse{
		IndexersAvailable: indexersAvailable,
		TotalModpacks:     totalModpacks,
		ModpacksByIndexer: modpacksByIndexer,
	}), nil
}

// Syncs modpack files
func (s *ModpackService) SyncModpackFiles(ctx context.Context, req *connect.Request[v1.SyncModpackFilesRequest]) (*connect.Response[v1.SyncModpackFilesResponse], error) {
	modpackID := req.Msg.Id

	// Get the modpack to determine its indexer
	modpack, err := s.store.GetIndexedModpack(ctx, modpackID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("modpack not found"))
	}

	indexerClient, err := s.getIndexer(ctx, modpack.Indexer)
	if err != nil {
		return nil, err
	}

	// Get all files from the indexer
	files, err := indexerClient.GetModpackFiles(ctx, modpack.IndexerId, "", "")
	if err != nil {
		s.log.Error("Failed to get modpack files: %v", err)
		return nil, mapIndexerError(err, "failed to get modpack files")
	}

	// Store files in database
	synced := 0
	for _, file := range files {
		if err := s.store.UpsertIndexedModpackFile(ctx, file); err != nil {
			s.log.Error("Failed to store modpack file %s: %v", file.Id, err)
			continue
		}
		synced++
	}

	return connect.NewResponse(&v1.SyncModpackFilesResponse{
		SyncedCount: int32(synced),
		Message:     fmt.Sprintf("Synced %d of %d files", synced, len(files)),
	}), nil
}
