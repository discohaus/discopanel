package fuego

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/indexers"
)

const (
	BaseURL         = "https://api.curseforge.com/v1"
	MinecraftGameID = 432
	ModpackClassID  = 4471
	ModsClassID     = 6
)

type Client struct {
	apiKey string
	http   *indexers.HTTPClient
}

func NewClient(apiKey string, userAgent string) *Client {
	return &Client{
		apiKey: apiKey,
		http: indexers.NewHTTPClient("fuego", userAgent, map[string]string{
			"x-api-key": apiKey,
		}),
	}
}

type SearchModsResponse struct {
	Data       []Modpack  `json:"data"`
	Pagination Pagination `json:"pagination"`
}

type Pagination struct {
	Index       int `json:"index"`
	PageSize    int `json:"pageSize"`
	ResultCount int `json:"resultCount"`
	TotalCount  int `json:"totalCount"`
}

type Modpack struct {
	ID                 int          `json:"id"`
	GameID             int          `json:"gameId"`
	ClassID            int          `json:"classId"`
	Name               string       `json:"name"`
	Slug               string       `json:"slug"`
	Links              Links        `json:"links"`
	Summary            string       `json:"summary"`
	Status             int          `json:"status"`
	DownloadCount      float64      `json:"downloadCount"`
	IsFeatured         bool         `json:"isFeatured"`
	PrimaryCategoryID  int          `json:"primaryCategoryId"`
	Categories         []Category   `json:"categories"`
	Authors            []Author     `json:"authors"`
	Logo               Logo         `json:"logo"`
	Screenshots        []Screenshot `json:"screenshots"`
	MainFileID         int          `json:"mainFileId"`
	LatestFiles        []File       `json:"latestFiles"`
	LatestFilesIndexes []FileIndex  `json:"latestFilesIndexes"`
	DateCreated        time.Time    `json:"dateCreated"`
	DateModified       time.Time    `json:"dateModified"`
	DateReleased       time.Time    `json:"dateReleased"`
}

type Links struct {
	WebsiteURL string `json:"websiteUrl"`
	WikiURL    string `json:"wikiUrl"`
	IssuesURL  string `json:"issuesUrl"`
	SourceURL  string `json:"sourceUrl"`
}

type Category struct {
	ID      int    `json:"id"`
	GameID  int    `json:"gameId"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	URL     string `json:"url"`
	IconURL string `json:"iconUrl"`
}

type Author struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Logo struct {
	ID           int    `json:"id"`
	ModID        int    `json:"modId"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnailUrl"`
	URL          string `json:"url"`
}

type Screenshot struct {
	ID           int    `json:"id"`
	ModID        int    `json:"modId"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnailUrl"`
	URL          string `json:"url"`
}

type File struct {
	ID                   int                   `json:"id"`
	GameID               int                   `json:"gameId"`
	ModID                int                   `json:"modId"`
	IsAvailable          bool                  `json:"isAvailable"`
	DisplayName          string                `json:"displayName"`
	FileName             string                `json:"fileName"`
	ReleaseType          int                   `json:"releaseType"`
	FileStatus           int                   `json:"fileStatus"`
	Hashes               []Hash                `json:"hashes"`
	FileDate             time.Time             `json:"fileDate"`
	FileLength           int64                 `json:"fileLength"`
	FileFingerprint      int64                 `json:"fileFingerprint"`
	DownloadCount        int64                 `json:"downloadCount"`
	DownloadURL          string                `json:"downloadUrl"`
	GameVersions         []string              `json:"gameVersions"`
	SortableGameVersions []SortableGameVersion `json:"sortableGameVersions"`
	Dependencies         []Dependency          `json:"dependencies"`
	AlternateFileID      int                   `json:"alternateFileId"`
	IsServerPack         bool                  `json:"isServerPack"`
	ServerPackFileID     *int                  `json:"serverPackFileId"`
}

type Hash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"`
}

type SortableGameVersion struct {
	GameVersionName        string    `json:"gameVersionName"`
	GameVersionPadded      string    `json:"gameVersionPadded"`
	GameVersion            string    `json:"gameVersion"`
	GameVersionReleaseDate time.Time `json:"gameVersionReleaseDate"`
}

type Dependency struct {
	ModID        int `json:"modId"`
	RelationType int `json:"relationType"`
}

// CurseForge relation type marking a required dependency
const RelationRequiredDependency = 3

type FileIndex struct {
	GameVersion       string `json:"gameVersion"`
	FileID            int    `json:"fileId"`
	Filename          string `json:"filename"`
	ReleaseType       int    `json:"releaseType"`
	GameVersionTypeID *int   `json:"gameVersionTypeId"`
	ModLoader         *int   `json:"modLoader"`
}

type ModLoaderType int

const (
	ModLoaderAny        ModLoaderType = 0
	ModLoaderForge      ModLoaderType = 1
	ModLoaderCauldron   ModLoaderType = 2
	ModLoaderLiteLoader ModLoaderType = 3
	ModLoaderFabric     ModLoaderType = 4
	ModLoaderQuilt      ModLoaderType = 5
	ModLoaderNeoForge   ModLoaderType = 6
)

func (c *Client) SearchModpacks(ctx context.Context, query string, gameVersion string, modLoader ModLoaderType, index, pageSize int) (*SearchModsResponse, error) {
	return c.SearchProjects(ctx, query, ModpackClassID, gameVersion, modLoader, index, pageSize)
}

// Searches projects of one class sorted by popularity
func (c *Client) SearchProjects(ctx context.Context, query string, classID int, gameVersion string, modLoader ModLoaderType, index, pageSize int) (*SearchModsResponse, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	params := url.Values{}
	params.Set("gameId", strconv.Itoa(MinecraftGameID))
	params.Set("classId", strconv.Itoa(classID))
	params.Set("index", strconv.Itoa(index))
	params.Set("pageSize", strconv.Itoa(pageSize))
	params.Set("sortField", "2") // Sort by popularity
	params.Set("sortOrder", "desc")

	if query != "" {
		params.Set("searchFilter", query)
	}

	if gameVersion != "" {
		params.Set("gameVersion", gameVersion)
	}

	if modLoader != ModLoaderAny {
		params.Set("modLoaderType", strconv.Itoa(int(modLoader)))
	}

	var result SearchModsResponse
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/mods/search?%s", BaseURL, params.Encode()), &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *Client) GetModpackFiles(ctx context.Context, modID int, gameVersion string, modLoader ModLoaderType) ([]File, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	params := url.Values{}
	if gameVersion != "" {
		params.Set("gameVersion", gameVersion)
	}
	if modLoader != ModLoaderAny {
		params.Set("modLoaderType", strconv.Itoa(int(modLoader)))
	}
	endpoint := fmt.Sprintf("%s/mods/%d/files", BaseURL, modID)
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	var result struct {
		Data []File `json:"data"`
	}
	if err := c.http.DoJSON(ctx, endpoint, &result); err != nil {
		return nil, err
	}

	return result.Data, nil
}

func (c *Client) GetModpack(ctx context.Context, modID int) (*Modpack, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	var result struct {
		Data Modpack `json:"data"`
	}
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/mods/%d", BaseURL, modID), &result); err != nil {
		return nil, err
	}

	return &result.Data, nil
}

// Resolves a project of any class by slug
func (c *Client) GetModBySlug(ctx context.Context, slug string, classID int) (*Modpack, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	params := url.Values{}
	params.Set("gameId", strconv.Itoa(MinecraftGameID))
	params.Set("slug", slug)
	if classID > 0 {
		params.Set("classId", strconv.Itoa(classID))
	}

	var result SearchModsResponse
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/mods/search?%s", BaseURL, params.Encode()), &result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no CurseForge project found for slug %q", slug)
	}
	return &result.Data[0], nil
}

// Fetches metadata for a single mod or modpack file
func (c *Client) GetFile(ctx context.Context, modID, fileID int) (*File, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	var result struct {
		Data File `json:"data"`
	}
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/mods/%d/files/%d", BaseURL, modID, fileID), &result); err != nil {
		return nil, err
	}
	return &result.Data, nil
}

// Bulk-fetches file metadata by ID
func (c *Client) GetFilesByIDs(ctx context.Context, fileIDs []int) ([]File, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	var result struct {
		Data []File `json:"data"`
	}
	body := map[string]any{"fileIds": fileIDs}
	if err := c.http.PostJSON(ctx, fmt.Sprintf("%s/mods/files", BaseURL), body, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// One exact fingerprint hit with its file metadata
type FingerprintMatch struct {
	ID   int  `json:"id"`
	File File `json:"file"`
}

// Identifies files by murmur2 fingerprint, unknown prints drop out
func (c *Client) GetFingerprintMatches(ctx context.Context, fingerprints []uint32) ([]FingerprintMatch, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	var result struct {
		Data struct {
			ExactMatches []FingerprintMatch `json:"exactMatches"`
		} `json:"data"`
	}
	body := map[string]any{"fingerprints": fingerprints}
	if err := c.http.PostJSON(ctx, fmt.Sprintf("%s/fingerprints/%d", BaseURL, MinecraftGameID), body, &result); err != nil {
		return nil, err
	}
	return result.Data.ExactMatches, nil
}

// Bulk-fetches mod metadata for class and slug resolution
func (c *Client) GetModsByIDs(ctx context.Context, modIDs []int) ([]Modpack, error) {
	if c.apiKey == "" {
		return nil, indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	var result struct {
		Data []Modpack `json:"data"`
	}
	body := map[string]any{"modIds": modIDs}
	if err := c.http.PostJSON(ctx, fmt.Sprintf("%s/mods", BaseURL), body, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// Resolves CDN download url, empty if author blocked distribution
func (c *Client) GetFileDownloadURL(ctx context.Context, modID, fileID int) (string, error) {
	if c.apiKey == "" {
		return "", indexers.NewAuthConfigError("fuego", "API key not configured")
	}

	var result struct {
		Data *string `json:"data"`
	}
	err := c.http.DoJSON(ctx, fmt.Sprintf("%s/mods/%d/files/%d/download-url", BaseURL, modID, fileID), &result)
	if err != nil {
		var apiErr *indexers.IndexerError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 403 {
			if verr := c.verifyKey(ctx); verr != nil {
				return "", verr
			}
			return "", nil // Key works so author blocked distribution
		}
		return "", err
	}
	if result.Data == nil {
		return "", nil
	}
	return *result.Data, nil
}

var (
	keyVerdictMu sync.Mutex
	keyVerdicts  = map[string]error{}
)

// Probes a cheap endpoint once to judge the key
func (c *Client) verifyKey(ctx context.Context) error {
	keyVerdictMu.Lock()
	verdict, known := keyVerdicts[c.apiKey]
	keyVerdictMu.Unlock()
	if known {
		return verdict
	}

	var probe struct {
		Data struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	err := c.http.DoJSON(ctx, fmt.Sprintf("%s/games/%d", BaseURL, MinecraftGameID), &probe)
	var apiErr *indexers.IndexerError
	switch {
	case err == nil:
		verdict = nil
	case errors.As(err, &apiErr) && apiErr.Kind == indexers.ErrAuth:
		verdict = fmt.Errorf("CurseForge API key was rejected, update it in settings: %w", err)
	default:
		return err
	}

	keyVerdictMu.Lock()
	keyVerdicts[c.apiKey] = verdict
	keyVerdictMu.Unlock()
	return verdict
}

// Resolves a file download url, CDN guess covers withheld urls
func (c *Client) ResolveDownloadURL(ctx context.Context, modID int, file *File) (string, error) {
	dlURL := file.DownloadURL
	if dlURL == "" {
		var err error
		dlURL, err = c.GetFileDownloadURL(ctx, modID, file.ID)
		if err != nil {
			return "", err
		}
	}
	if dlURL == "" {
		dlURL = CDNDownloadURL(file.ID, file.FileName)
	}
	if dlURL == "" {
		return "", fmt.Errorf("could not resolve a download url for %q", file.FileName)
	}
	return dlURL, nil
}

// Strongest hash CurseForge published for the file
func (f *File) BestHash() (string, string) {
	for _, h := range f.Hashes {
		if h.Algo == 1 {
			return "sha1", h.Value
		}
	}
	for _, h := range f.Hashes {
		if h.Algo == 2 {
			return "md5", h.Value
		}
	}
	return "", ""
}

// Builds Forge CDN url for an API-withheld file
func CDNDownloadURL(fileID int, fileName string) string {
	if fileID <= 0 || fileName == "" {
		return ""
	}
	return fmt.Sprintf("https://edge.forgecdn.net/files/%d/%d/%s", fileID/1000, fileID%1000, url.PathEscape(fileName))
}
