package modrinth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/indexers"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func init() {
	indexers.RegisterIndexer("modrinth",
		func(_ string, userAgent string) indexers.ModpackIndexer {
			return NewIndexer(userAgent)
		},
		indexers.WithPackSource(optionsv1.PackSource_PACK_SOURCE_MODRINTH),
		indexers.WithForceIncludeProperty("modrinthForceIncludeFiles"),
		// Modrinth documents 300 requests per minute
		indexers.WithRequestRate(5, 10),
	)
}

// Implements ModpackIndexer
var _ indexers.ModpackIndexer = (*ModrinthIndexer)(nil)

// Adapts the Modrinth client to the ModpackIndexer interface
type ModrinthIndexer struct {
	client *Client
}

// Creates a new Modrinth indexer, no API key needed
func NewIndexer(userAgent string) *ModrinthIndexer {
	return &ModrinthIndexer{
		client: NewClient(userAgent),
	}
}

// Get the name of this indexer
func (m *ModrinthIndexer) GetIndexerName() string {
	return "modrinth"
}

// Maps Modrinth version types onto the proto enum
func releaseType(versionType string) v1.ReleaseType {
	if rt, ok := protometa.FromName[v1.ReleaseType](strings.ToLower(versionType)); ok {
		return rt
	}
	return v1.ReleaseType_RELEASE_TYPE_RELEASE
}

// Search for modpacks
func (m *ModrinthIndexer) SearchModpacks(ctx context.Context, query string, gameVersion string, modLoader string, offset, limit int) (*v1.SearchModpacksResponse, error) {
	// Search using Modrinth API
	resp, err := m.client.SearchModpacks(ctx, query, gameVersion, modLoader, offset, limit)
	if err != nil {
		return nil, err
	}

	// Normalizes Modrinth projects into proto rows
	modpacks := make([]*v1.IndexedModpack, len(resp.Hits))
	for i, project := range resp.Hits {
		// Parse dates
		dateCreated, _ := time.Parse(time.RFC3339, project.DateCreated)
		dateModified, _ := time.Parse(time.RFC3339, project.DateModified)

		// Extract mod loaders from categories (Modrinth puts loaders in categories)
		modLoaders := []string{}
		for _, cat := range project.Categories {
			if loader, ok := minecraft.DetectModpackLoader(cat); ok {
				modLoaders = append(modLoaders, protometa.Name(loader))
			}
		}

		// Use display_categories if available, otherwise use categories
		categories := project.DisplayCategories
		if len(categories) == 0 {
			categories = project.Categories
		}

		modpacks[i] = &v1.IndexedModpack{
			Id:            fmt.Sprintf("modrinth-%s", project.ProjectID),
			IndexerId:     project.ProjectID,
			Indexer:       "modrinth",
			Name:          project.Title,
			Slug:          project.Slug,
			Summary:       project.Description,
			Description:   project.Description,
			LogoUrl:       project.IconURL,
			WebsiteUrl:    fmt.Sprintf("https://modrinth.com/modpack/%s", project.Slug),
			DownloadCount: int32(project.Downloads),
			Categories:    categories,
			GameVersions:  project.Versions,
			ModLoaders:    modLoaders,
			LatestFileId:  project.LatestVersion,
			DateCreated:   timestamppb.New(dateCreated),
			DateModified:  timestamppb.New(dateModified),
			DateReleased:  timestamppb.New(dateCreated), // Modrinth search carries no release date
		}
	}

	return &v1.SearchModpacksResponse{
		Modpacks: modpacks,
		Total:    int32(resp.TotalHits),
	}, nil
}

// Get a specific modpack
func (m *ModrinthIndexer) GetModpack(ctx context.Context, modpackID string) (*v1.IndexedModpack, error) {
	// Get full project details
	project, err := m.client.GetModpack(ctx, modpackID)
	if err != nil {
		return nil, err
	}

	// Parse dates
	dateCreated, _ := time.Parse(time.RFC3339, project.Published)
	dateModified, _ := time.Parse(time.RFC3339, project.Updated)

	// Use project.Loaders for mod loaders
	modLoaders := make([]string, len(project.Loaders))
	for i, loader := range project.Loaders {
		modLoaders[i] = strings.ToLower(loader)
	}

	// Use display categories (non-loader categories)
	categories := []string{}
	for _, cat := range project.Categories {
		// Skip loader categories
		if _, isLoader := minecraft.DetectModpackLoader(cat); !isLoader {
			categories = append(categories, cat)
		}
	}
	if len(project.AdditionalCategories) > 0 {
		categories = append(categories, project.AdditionalCategories...)
	}

	// Project versions list is chronological, oldest first
	latestVersionID := ""
	if len(project.Versions) > 0 {
		latestVersionID = project.Versions[len(project.Versions)-1]
	}

	return &v1.IndexedModpack{
		Id:            fmt.Sprintf("modrinth-%s", project.ID),
		IndexerId:     project.ID,
		Indexer:       "modrinth",
		Name:          project.Title,
		Slug:          project.Slug,
		Summary:       project.Description,
		Description:   project.Body, // Full description from project body
		LogoUrl:       project.IconURL,
		WebsiteUrl:    fmt.Sprintf("https://modrinth.com/modpack/%s", project.Slug),
		DownloadCount: int32(project.Downloads),
		Categories:    categories,
		GameVersions:  project.GameVersions,
		ModLoaders:    modLoaders,
		LatestFileId:  latestVersionID,
		DateCreated:   timestamppb.New(dateCreated),
		DateModified:  timestamppb.New(dateModified),
		DateReleased:  timestamppb.New(dateCreated),
	}, nil
}

// Get files for a modpack
func (m *ModrinthIndexer) GetModpackFiles(ctx context.Context, modpackID string, gameVersion string, modLoader string) ([]*v1.IndexedModpackFile, error) {
	var loaders, gameVersions []string
	if modLoader != "" {
		loaders = []string{strings.ToLower(modLoader)}
	}
	if gameVersion != "" {
		gameVersions = []string{gameVersion}
	}
	versions, err := m.client.GetProjectVersionsFiltered(ctx, modpackID, loaders, gameVersions)
	if err != nil {
		return nil, err
	}

	// Normalizes versions to proto rows, keeps newest-first order
	result := make([]*v1.IndexedModpackFile, 0, len(versions))
	for _, version := range versions {
		if len(version.Files) == 0 {
			continue
		}

		// Find primary file or use first one
		var primaryFile *File
		for i := range version.Files {
			if version.Files[i].Primary {
				primaryFile = &version.Files[i]
				break
			}
		}
		if primaryFile == nil {
			primaryFile = &version.Files[0]
		}

		// Parse date
		fileDate, _ := time.Parse(time.RFC3339, version.DatePublished)

		// Get primary mod loader
		fileLoader := ""
		if len(version.Loaders) > 0 {
			fileLoader = strings.ToLower(version.Loaders[0])
		}

		// No separate server pack files, uses the primary file
		result = append(result, &v1.IndexedModpackFile{
			Id:               version.ID,
			ModpackId:        fmt.Sprintf("modrinth-%s", modpackID),
			DisplayName:      version.Name,
			FileName:         primaryFile.Filename,
			FileDate:         timestamppb.New(fileDate),
			FileLength:       primaryFile.Size,
			ReleaseType:      releaseType(version.VersionType),
			DownloadUrl:      primaryFile.URL,
			GameVersions:     version.GameVersions,
			ModLoader:        fileLoader,
			ServerPackFileId: nil,
		})
	}

	return result, nil
}
