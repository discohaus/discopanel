package main

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

const manifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

var httpClient = &http.Client{Timeout: 10 * time.Minute}

// One manifest row naming a release or snapshot
type manifestEntry struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	URL         string    `json:"url"`
	ReleaseTime time.Time `json:"releaseTime"`
}

// Mojang version manifest
type manifest struct {
	Latest struct {
		Release  string `json:"release"`
		Snapshot string `json:"snapshot"`
	} `json:"latest"`
	Versions []manifestEntry `json:"versions"`
}

// One downloadable artifact with its checksum
type download struct {
	URL  string `json:"url"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
}

// One client library with its platform rules
type library struct {
	Name      string `json:"name"`
	Downloads struct {
		Artifact *struct {
			Path string `json:"path"`
			URL  string `json:"url"`
			SHA1 string `json:"sha1"`
		} `json:"artifact"`
	} `json:"downloads"`
	Rules []struct {
		Action string `json:"action"`
		OS     *struct {
			Name string `json:"name"`
		} `json:"os"`
	} `json:"rules"`
}

// Per version metadata document
type versionMeta struct {
	ID          string `json:"id"`
	JavaVersion struct {
		MajorVersion int `json:"majorVersion"`
	} `json:"javaVersion"`
	Downloads  map[string]download `json:"downloads"`
	AssetIndex struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		SHA1 string `json:"sha1"`
	} `json:"assetIndex"`
	Libraries []library `json:"libraries"`
	MainClass string    `json:"mainClass"`
	Arguments struct {
		Game []json.RawMessage `json:"game"`
		JVM  []json.RawMessage `json:"jvm"`
	} `json:"arguments"`
}

// Identity block every modern jar carries at its root
type jarVersion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol int32  `json:"protocol_version"`
}

// Decodes one JSON document from a URL
func fetchJSON(url string, v any) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func fetchManifest() (*manifest, error) {
	var m manifest
	if err := fetchJSON(manifestURL, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

// Manifest row for one version id
func (m *manifest) find(id string) (manifestEntry, bool) {
	for _, v := range m.Versions {
		if v.ID == id {
			return v, true
		}
	}
	return manifestEntry{}, false
}

// Releases from one version onward, oldest first
func (m *manifest) releasesSince(since string) ([]manifestEntry, error) {
	start, ok := m.find(since)
	if !ok {
		return nil, fmt.Errorf("version %q is not in the manifest", since)
	}
	var out []manifestEntry
	for _, v := range m.Versions {
		if v.Type == "release" && !v.ReleaseTime.Before(start.ReleaseTime) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReleaseTime.Before(out[j].ReleaseTime) })
	return out, nil
}

// Version metadata, cached beside the jar
func fetchMeta(cache string, entry manifestEntry) (*versionMeta, error) {
	path := filepath.Join(cache, entry.ID, "version.json")
	var meta versionMeta
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &meta) == nil {
		return &meta, nil
	}
	if err := fetchJSON(entry.URL, &meta); err != nil {
		return nil, fmt.Errorf("%s metadata: %w", entry.ID, err)
	}
	raw, _ := json.Marshal(&meta)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &meta, os.WriteFile(path, raw, 0o644)
}

// Downloads one artifact unless the cached copy already matches
func downloadFile(d download, path string) error {
	if raw, err := os.ReadFile(path); err == nil && sha1Hex(raw) == d.SHA1 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	resp, err := httpClient.Get(d.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", d.URL, resp.Status)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha1.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != d.SHA1 {
		os.Remove(tmp)
		return fmt.Errorf("%s: sha1 %s, want %s", d.URL, got, d.SHA1)
	}
	return os.Rename(tmp, path)
}

func sha1Hex(raw []byte) string {
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:])
}

// Reads a remote zip through range requests
type rangeReader struct {
	url  string
	size int64
}

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	req, err := http.NewRequest(http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	end := off + int64(len(p)) - 1
	if end >= r.size {
		end = r.size - 1
	}
	req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(end, 10))
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("%s: range request answered %s", r.url, resp.Status)
	}
	n, err := io.ReadFull(resp.Body, p[:end-off+1])
	if err == io.ErrUnexpectedEOF {
		err = nil
	}
	if n < len(p) && err == nil {
		err = io.EOF
	}
	return n, err
}

// Identity of a server jar without downloading it whole
func remoteJarVersion(d download) (*jarVersion, error) {
	zr, err := zip.NewReader(&rangeReader{url: d.URL, size: d.Size}, d.Size)
	if err != nil {
		return nil, err
	}
	return readJarVersion(zr)
}

// Identity of a server jar on disk
func localJarVersion(path string) (*jarVersion, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return readJarVersion(&zr.Reader)
}

func readJarVersion(zr *zip.Reader) (*jarVersion, error) {
	for _, f := range zr.File {
		if f.Name != "version.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		var v jarVersion
		if err := json.NewDecoder(rc).Decode(&v); err != nil {
			return nil, err
		}
		if v.Protocol == 0 {
			return nil, fmt.Errorf("version.json carries no protocol_version")
		}
		return &v, nil
	}
	return nil, fmt.Errorf("jar has no version.json, versions before 1.14 are not supported")
}

// Jar identity cached per version so range reads happen once
func cachedJarVersion(cache string, meta *versionMeta) (*jarVersion, error) {
	path := filepath.Join(cache, meta.ID, "jar-version.json")
	var v jarVersion
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &v) == nil && v.Protocol != 0 {
		return &v, nil
	}
	jar := filepath.Join(cache, meta.ID, "server.jar")
	var got *jarVersion
	var err error
	if _, statErr := os.Stat(jar); statErr == nil {
		got, err = localJarVersion(jar)
	} else {
		got, err = remoteJarVersion(meta.Downloads["server"])
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", meta.ID, err)
	}
	raw, _ := json.Marshal(got)
	return got, os.WriteFile(path, raw, 0o644)
}
