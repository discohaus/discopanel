package fuego

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/pkg/indexers"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/utils"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func init() {
	indexers.RegisterIndexer("fuego",
		func(apiKey string, userAgent string) indexers.ModpackIndexer {
			return NewIndexer(apiKey, userAgent)
		},
		indexers.WithCredentialProperty("cfApiKey"),
		indexers.WithPackSource(optionsv1.PackSource_PACK_SOURCE_CURSEFORGE),
		indexers.WithForceIncludeProperty("cfForceIncludeMods"),
		indexers.WithRequestRate(4, 8),
	)
}

// Implements ModpackIndexer
var _ indexers.ModpackIndexer = (*FuegoIndexer)(nil)

// Adapts the Fuego client to the ModpackIndexer interface
type FuegoIndexer struct {
	client *Client
}

// Creates a new Fuego indexer
func NewIndexer(apiKey string, userAgent string) *FuegoIndexer {
	return &FuegoIndexer{
		client: NewClient(apiKey, userAgent),
	}
}

// Returns the name of this indexer
func (f *FuegoIndexer) GetIndexerName() string {
	return "fuego"
}

// Loader facet names per CurseForge loader code
var loaderNames = map[ModLoaderType]string{
	ModLoaderForge:    "forge",
	ModLoaderFabric:   "fabric",
	ModLoaderQuilt:    "quilt",
	ModLoaderNeoForge: "neoforge",
}

// Maps a loader name onto the CurseForge loader type
func loaderType(modLoader string) ModLoaderType {
	name := strings.ToLower(modLoader)
	for code, facet := range loaderNames {
		if facet == name {
			return code
		}
	}
	return ModLoaderAny
}

// Maps CurseForge release codes onto the proto enum
func releaseType(code int) v1.ReleaseType {
	switch code {
	case 2:
		return v1.ReleaseType_RELEASE_TYPE_BETA
	case 3:
		return v1.ReleaseType_RELEASE_TYPE_ALPHA
	default:
		return v1.ReleaseType_RELEASE_TYPE_RELEASE
	}
}

// Normalizes one Fuego modpack into a proto row
func toModpack(fm *Modpack) *v1.IndexedModpack {
	categories := make([]string, len(fm.Categories))
	for i, cat := range fm.Categories {
		categories[i] = cat.Name
	}

	var gameVersions, modLoaders []string
	for _, file := range fm.LatestFiles {
		gameVersions = append(gameVersions, file.GameVersions...)
	}
	for _, fileIndex := range fm.LatestFilesIndexes {
		if fileIndex.ModLoader != nil {
			if facet, ok := loaderNames[ModLoaderType(*fileIndex.ModLoader)]; ok {
				modLoaders = append(modLoaders, facet)
			}
		}
	}

	return &v1.IndexedModpack{
		Id:            fmt.Sprintf("fuego-%d", fm.ID),
		IndexerId:     strconv.Itoa(fm.ID),
		Indexer:       "fuego",
		Name:          fm.Name,
		Slug:          fm.Slug,
		Summary:       fm.Summary,
		Description:   fm.Summary, // Fuego search carries no separate description
		LogoUrl:       fm.Logo.ThumbnailURL,
		WebsiteUrl:    fm.Links.WebsiteURL,
		DownloadCount: int32(fm.DownloadCount),
		Categories:    categories,
		GameVersions:  utils.DeduplicateStrings(gameVersions),
		ModLoaders:    utils.DeduplicateStrings(modLoaders),
		LatestFileId:  strconv.Itoa(fm.MainFileID),
		DateCreated:   timestamppb.New(fm.DateCreated),
		DateModified:  timestamppb.New(fm.DateModified),
		DateReleased:  timestamppb.New(fm.DateReleased),
	}
}

// Search for modpacks
func (f *FuegoIndexer) SearchModpacks(ctx context.Context, query string, gameVersion string, modLoader string, offset, limit int) (*v1.SearchModpacksResponse, error) {
	if limit <= 0 {
		limit = 20
	}

	// CurseForge index counts items, not pages
	resp, err := f.client.SearchModpacks(ctx, query, gameVersion, loaderType(modLoader), offset, limit)
	if err != nil {
		return nil, err
	}

	modpacks := make([]*v1.IndexedModpack, len(resp.Data))
	for i := range resp.Data {
		modpacks[i] = toModpack(&resp.Data[i])
	}
	return &v1.SearchModpacksResponse{
		Modpacks: modpacks,
		Total:    int32(resp.Pagination.TotalCount),
	}, nil
}

// Get a specific modpack
func (f *FuegoIndexer) GetModpack(ctx context.Context, modpackID string) (*v1.IndexedModpack, error) {
	id, err := strconv.Atoi(modpackID)
	if err != nil {
		return nil, fmt.Errorf("invalid modpack ID: %s", modpackID)
	}

	fm, err := f.client.GetModpack(ctx, id)
	if err != nil {
		return nil, err
	}
	return toModpack(fm), nil
}

// Get files for a modpack
func (f *FuegoIndexer) GetModpackFiles(ctx context.Context, modpackID string, gameVersion string, modLoader string) ([]*v1.IndexedModpackFile, error) {
	id, err := strconv.Atoi(modpackID)
	if err != nil {
		return nil, fmt.Errorf("invalid modpack ID: %s", modpackID)
	}

	files, err := f.client.GetModpackFiles(ctx, id, gameVersion, loaderType(modLoader))
	if err != nil {
		return nil, err
	}

	// Normalizes Fuego files into proto rows
	result := make([]*v1.IndexedModpackFile, len(files))
	for i, file := range files {
		// Extract primary mod loader w/ best effort score matching
		fileLoader := ""
		if loader, ok := minecraft.DetectModpackLoader(file.GameVersions...); ok {
			fileLoader = protometa.Name(loader)
		}

		serverPackID := ""
		if file.ServerPackFileID != nil {
			serverPackID = strconv.Itoa(*file.ServerPackFileID)
		}

		result[i] = &v1.IndexedModpackFile{
			Id:               strconv.Itoa(file.ID),
			ModpackId:        fmt.Sprintf("fuego-%s", modpackID),
			DisplayName:      file.DisplayName,
			FileName:         file.FileName,
			FileDate:         timestamppb.New(file.FileDate),
			FileLength:       file.FileLength,
			ReleaseType:      releaseType(file.ReleaseType),
			DownloadUrl:      file.DownloadURL,
			GameVersions:     file.GameVersions,
			ModLoader:        fileLoader,
			ServerPackFileId: &serverPackID,
		}
	}

	return result, nil
}
