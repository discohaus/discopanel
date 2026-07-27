package modrinth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/pkg/indexers"
)

const (
	BaseURL = "https://api.modrinth.com/v2"
)

type Client struct {
	http *indexers.HTTPClient
}

func NewClient(userAgent string) *Client {
	return &Client{
		http: indexers.NewHTTPClient("modrinth", userAgent, nil),
	}
}

// Represents the Modrinth search API response
type SearchResponse struct {
	Hits      []Project `json:"hits"`
	Offset    int       `json:"offset"`
	Limit     int       `json:"limit"`
	TotalHits int       `json:"total_hits"`
}

// Represents a Modrinth project or modpack
type Project struct {
	Slug              string   `json:"slug"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	Categories        []string `json:"categories"`
	ClientSide        string   `json:"client_side"`
	ServerSide        string   `json:"server_side"`
	ProjectType       string   `json:"project_type"`
	Downloads         int64    `json:"downloads"`
	IconURL           string   `json:"icon_url"`
	ProjectID         string   `json:"project_id"`
	Author            string   `json:"author"`
	DisplayCategories []string `json:"display_categories"`
	Versions          []string `json:"versions"`
	Follows           int      `json:"follows"`
	DateCreated       string   `json:"date_created"`
	DateModified      string   `json:"date_modified"`
	LatestVersion     string   `json:"latest_version"`
	License           string   `json:"license"`
	Gallery           []string `json:"gallery"`
	FeaturedGallery   string   `json:"featured_gallery"`
	Color             *int     `json:"color"`
}

// Full project details from GET /project/{id}
type ProjectDetails struct {
	ID                   string        `json:"id"`
	Slug                 string        `json:"slug"`
	Title                string        `json:"title"`
	Description          string        `json:"description"`
	Body                 string        `json:"body"`
	ProjectType          string        `json:"project_type"`
	ClientSide           string        `json:"client_side"`
	ServerSide           string        `json:"server_side"`
	GameVersions         []string      `json:"game_versions"`
	Loaders              []string      `json:"loaders"`
	Categories           []string      `json:"categories"`
	AdditionalCategories []string      `json:"additional_categories"`
	Status               string        `json:"status"`
	Published            string        `json:"published"`
	Updated              string        `json:"updated"`
	Downloads            int64         `json:"downloads"`
	Followers            int           `json:"followers"`
	IconURL              string        `json:"icon_url"`
	Color                *int          `json:"color"`
	Gallery              []GalleryItem `json:"gallery"`
	SourceURL            string        `json:"source_url"`
	IssuesURL            string        `json:"issues_url"`
	WikiURL              string        `json:"wiki_url"`
	DiscordURL           string        `json:"discord_url"`
	DonationURLs         []DonationURL `json:"donation_urls"`
	Team                 string        `json:"team"`
	Versions             []string      `json:"versions"`
	License              License       `json:"license"`
}

type GalleryItem struct {
	URL         string  `json:"url"`
	Featured    bool    `json:"featured"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Created     string  `json:"created"`
	Ordering    int     `json:"ordering"`
}

type DonationURL struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

type License struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Represents a single project version
type Version struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	VersionNumber string       `json:"version_number"`
	ProjectID     string       `json:"project_id"`
	GameVersions  []string     `json:"game_versions"`
	Loaders       []string     `json:"loaders"`
	VersionType   string       `json:"version_type"`
	Featured      bool         `json:"featured"`
	Status        string       `json:"status"`
	Files         []File       `json:"files"`
	Downloads     int64        `json:"downloads"`
	DatePublished string       `json:"date_published"`
	Changelog     *string      `json:"changelog"`
	Dependencies  []Dependency `json:"dependencies"`
}

type File struct {
	Hashes   Hashes `json:"hashes"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Size     int64  `json:"size"`
	FileType string `json:"file_type"`
}

type Hashes struct {
	SHA512 string `json:"sha512"`
	SHA1   string `json:"sha1"`
}

type Dependency struct {
	VersionID      *string `json:"version_id"`
	ProjectID      *string `json:"project_id"`
	FileName       *string `json:"file_name"`
	DependencyType string  `json:"dependency_type"`
}

// Searches for modpacks on Modrinth
func (c *Client) SearchModpacks(ctx context.Context, query string, gameVersion string, modLoader string, offset, limit int) (*SearchResponse, error) {
	var loaders, gameVersions []string
	if modLoader != "" {
		loaders = []string{modLoader}
	}
	if gameVersion != "" {
		gameVersions = []string{gameVersion}
	}
	return c.SearchProjects(ctx, query, "modpack", loaders, gameVersions, offset, limit)
}

// Searches projects of one type with facet filters
func (c *Client) SearchProjects(ctx context.Context, query, projectType string, loaders, gameVersions []string, offset, limit int) (*SearchResponse, error) {
	facets := [][]string{
		{fmt.Sprintf("project_type:%s", projectType)},
	}

	if len(gameVersions) > 0 {
		group := make([]string, len(gameVersions))
		for i, v := range gameVersions {
			group[i] = fmt.Sprintf("versions:%s", v)
		}
		facets = append(facets, group)
	}

	if len(loaders) > 0 {
		group := make([]string, len(loaders))
		for i, l := range loaders {
			group[i] = fmt.Sprintf("categories:%s", strings.ToLower(l))
		}
		facets = append(facets, group)
	}

	facetsJSON, err := json.Marshal(facets)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal facets: %w", err)
	}

	params := url.Values{}
	if query != "" {
		params.Set("query", query)
	}
	params.Set("facets", string(facetsJSON))
	params.Set("index", "downloads") // Sort by downloads
	params.Set("offset", strconv.Itoa(offset))
	params.Set("limit", strconv.Itoa(limit))

	var searchResp SearchResponse
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/search?%s", BaseURL, params.Encode()), &searchResp); err != nil {
		return nil, err
	}

	return &searchResp, nil
}

// Retrieves detailed info for one modpack
func (c *Client) GetModpack(ctx context.Context, modpackID string) (*ProjectDetails, error) {
	var project ProjectDetails
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/project/%s", BaseURL, modpackID), &project); err != nil {
		return nil, err
	}

	return &project, nil
}

// Retrieves project versions, filtered by loader and game version
func (c *Client) GetProjectVersionsFiltered(ctx context.Context, projectID string, loaders []string, gameVersions []string) ([]Version, error) {
	params := url.Values{}
	if len(loaders) > 0 {
		if f, err := json.Marshal(loaders); err == nil {
			params.Set("loaders", string(f))
		}
	}
	if len(gameVersions) > 0 {
		if f, err := json.Marshal(gameVersions); err == nil {
			params.Set("game_versions", string(f))
		}
	}

	endpoint := fmt.Sprintf("%s/project/%s/version", BaseURL, projectID)
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	var versions []Version
	if err := c.http.DoJSON(ctx, endpoint, &versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// Retrieves a single version by its ID
func (c *Client) GetVersion(ctx context.Context, versionID string) (*Version, error) {
	var version Version
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/version/%s", BaseURL, versionID), &version); err != nil {
		return nil, err
	}
	return &version, nil
}
