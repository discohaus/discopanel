package minecraft

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/indexers"
)

const (
	// Uses the v2 manifest API
	versionManifestV2URL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

	// Cache for 1 hour
	cacheDuration = time.Hour

	// Failed refetches wait this long before trying again
	refetchFloor = time.Minute

	// Bounds one manifest or metadata fetch
	pistonFetchTimeout = 30 * time.Second
)

// Shared resilience client for piston-meta requests
var pistonHTTP = indexers.NewHTTPClient("piston-meta.mojang.com", "", nil)

type VersionManifestV2 struct {
	Latest   LatestVersions `json:"latest"`
	Versions []Version      `json:"versions"`
}

type LatestVersions struct {
	Release  string `json:"release"`
	Snapshot string `json:"snapshot"`
}

// A single Minecraft version entry
type Version struct {
	ID              string    `json:"id"`
	Type            string    `json:"type"`
	URL             string    `json:"url"`
	Time            time.Time `json:"time"`
	ReleaseTime     time.Time `json:"releaseTime"`
	SHA1            string    `json:"sha1"`
	ComplianceLevel int       `json:"complianceLevel"`
}

// Metadata for a specific version
type VersionMetadata struct {
	JavaVersion struct {
		Component    string `json:"component"`
		MajorVersion int    `json:"majorVersion"`
	} `json:"javaVersion"`
	Downloads struct {
		Server struct {
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"server"`
	} `json:"downloads"`
}

// Manifest data
type versionCache struct {
	mu            sync.RWMutex
	manifest      *VersionManifestV2
	lastFetchTime time.Time
	lastAttempt   time.Time
	javaVersions  map[string]string // Cache for Java versions by MC version ID
}

var cache = &versionCache{
	javaVersions: make(map[string]string),
}

// Fetches the version manifest, stale beats a failed refetch
func fetchVersionManifest() (*VersionManifestV2, error) {
	cache.mu.RLock()
	stale := cache.manifest
	fresh := stale != nil && time.Since(cache.lastFetchTime) < cacheDuration
	attempted := time.Since(cache.lastAttempt) < refetchFloor
	cache.mu.RUnlock()
	if fresh || (stale != nil && attempted) {
		return stale, nil
	}

	cache.mu.Lock()
	cache.lastAttempt = time.Now()
	cache.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), pistonFetchTimeout)
	defer cancel()
	var manifest VersionManifestV2
	if err := pistonHTTP.DoJSON(ctx, versionManifestV2URL, &manifest); err != nil {
		if stale != nil {
			return stale, nil
		}
		return nil, fmt.Errorf("failed to fetch version manifest: %w", err)
	}

	cache.mu.Lock()
	cache.manifest = &manifest
	cache.lastFetchTime = time.Now()
	cache.mu.Unlock()

	return &manifest, nil
}

// Returns the latest Minecraft release version
func GetLatestVersion() string {
	manifest, err := fetchVersionManifest()
	if err != nil {
		return "0"
	}

	if manifest.Latest.Release != "" {
		return manifest.Latest.Release
	}

	// Fallback to first release in the list
	for _, version := range manifest.Versions {
		if version.Type == "release" {
			return version.ID
		}
	}

	return "0"
}

// Returns all versions including snapshots
func GetAllVersions() []string {
	manifest, err := fetchVersionManifest()
	if err != nil {
		return []string{}
	}

	var versions []string
	for _, version := range manifest.Versions {
		versions = append(versions, version.ID)
	}

	return versions
}

// Returns detailed information about a specific version
func GetVersionInfo(version string) (*Version, error) {
	manifest, err := fetchVersionManifest()
	if err != nil {
		return nil, err
	}

	for _, v := range manifest.Versions {
		if v.ID == version {
			return &v, nil
		}
	}

	return nil, fmt.Errorf("version %s not found", version)
}

// Fetches the full metadata document for a specific Minecraft version
func GetVersionMetadata(mcVersion string) (*VersionMetadata, error) {
	versionInfo, err := GetVersionInfo(mcVersion)
	if err != nil {
		return nil, fmt.Errorf("version %s not found in manifest", mcVersion)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pistonFetchTimeout)
	defer cancel()
	var metadata VersionMetadata
	if err := pistonHTTP.DoJSON(ctx, versionInfo.URL, &metadata); err != nil {
		return nil, fmt.Errorf("failed to fetch version metadata: %w", err)
	}

	// Cache the java version for GetJavaVersion
	cache.mu.Lock()
	cache.javaVersions[mcVersion] = strconv.Itoa(metadata.JavaVersion.MajorVersion)
	cache.mu.Unlock()

	return &metadata, nil
}

// Fetches required Java version for a specific Minecraft version
func GetJavaVersion(mcVersion string) (string, error) {
	// Check cache first
	cache.mu.RLock()
	if javaVer, ok := cache.javaVersions[mcVersion]; ok {
		cache.mu.RUnlock()
		return javaVer, nil
	}
	cache.mu.RUnlock()

	metadata, err := GetVersionMetadata(mcVersion)
	if err != nil {
		return "0", err
	}

	return strconv.Itoa(metadata.JavaVersion.MajorVersion), nil
}

// First release shipping the management server
const managementProtocolRelease = "1.21.9"

// Management protocol snapshot floor (25w35a introduced it)
const (
	managementSnapshotYear = 25
	managementSnapshotWeek = 35
)

var snapshotIDPattern = regexp.MustCompile(`^(\d{2})w(\d{2})`)

// Compares version strings numerically, ignoring pre-release suffix
func CompareGameVersions(a, b string) int {
	as, bs := splitVersionSegments(a), splitVersionSegments(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Parses leading numeric dot-separated segments into ints
func splitVersionSegments(v string) []int {
	var out []int
	for _, seg := range strings.Split(strings.TrimSpace(v), ".") {
		n := 0
		digits := 0
		for _, r := range seg {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
			digits++
		}
		if digits == 0 {
			return out
		}
		out = append(out, n)
	}
	return out
}

// Reports whether a version supports Mojang's management API
func SupportsManagementProtocol(mcVersion string) bool {
	if m := snapshotIDPattern.FindStringSubmatch(strings.TrimSpace(mcVersion)); m != nil {
		year, _ := strconv.Atoi(m[1])
		week, _ := strconv.Atoi(m[2])
		return year > managementSnapshotYear ||
			(year == managementSnapshotYear && week >= managementSnapshotWeek)
	}
	if len(splitVersionSegments(mcVersion)) == 0 {
		return false
	}
	return CompareGameVersions(mcVersion, managementProtocolRelease) >= 0
}

func FindMostRecentMinecraftVersion(versions []string) string {
	for i := len(versions) - 1; i >= 0; i-- {
		hasLetter := false
		for _, ch := range versions[i] {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
				hasLetter = true
				break
			}
		}
		if !hasLetter {
			return versions[i]
		}
	}
	if len(versions) > 0 {
		return versions[len(versions)-1] // Return last because we don't have a choice now
	}
	return ""
}
