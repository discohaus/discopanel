// Update checks for installed mod and plugin jars, by exact file hash
package modupdates

import (
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	utils "github.com/discohaus/discopanel/pkg/utils"
)

// Upstream verdicts live this long before a check asks again
const resultTTL = 10 * time.Minute

// File digests stay valid while path, size and mtime hold
const digestTTL = time.Hour

// Entries each cache keeps at most
const cacheEntries = 4096

// One installed jar, read only through Root
type Jar struct {
	Root    *os.Root
	Dir     string
	Name    string
	Enabled bool
	Size    int64
	ModTime time.Time
}

func (j Jar) Path() string { return filepath.Join(j.Dir, j.Name) }

// Jar with the digests upstreams identify files by
type HashedJar struct {
	Jar
	SHA1    string
	SHA512  string
	CFPrint uint32
}

// Server facts a newer build must match
type Target struct {
	McVersion   string
	Loaders     []string
	IncludeBeta bool
}

// Release channels the target accepts
func (t Target) Channels() []string {
	if t.IncludeBeta {
		return []string{"release", "beta"}
	}
	return []string{"release"}
}

func (t Target) allows(channel string) bool {
	return slices.Contains(t.Channels(), strings.ToLower(channel))
}

func (t Target) key() string {
	return fmt.Sprintf("%s|%s|%t", t.McVersion, strings.Join(t.Loaders, ","), t.IncludeBeta)
}

type Status int

const (
	StatusUnknown Status = iota
	StatusUpToDate
	StatusAvailable
	StatusNoCompatible
	StatusError
)

// One build of a project
type Release struct {
	VersionID    string
	Version      string
	FileName     string
	URL          string
	HashAlgo     string
	HashSum      string
	Size         int64
	Channel      string
	Published    time.Time
	ChangelogURL string
}

// Verdict for one jar
type Result struct {
	Jar         Jar
	Status      Status
	Source      v1.ModUpdateSource
	ProjectID   string
	ProjectName string
	ProjectURL  string
	Current     *Release
	Latest      *Release
	Err         string
}

// Upstream that identifies jars by digest and names newer builds
type Source interface {
	Kind() v1.ModUpdateSource
	// Results keyed by jar path, jars the source does not know are left out
	Lookup(ctx context.Context, jars []*HashedJar, t Target) (map[string]*Result, error)
}

// Shared caches for checks across servers
type Checker struct {
	digests *ttlCache[string, *HashedJar]
	results *ttlCache[string, *Result]
}

func NewChecker() *Checker {
	return &Checker{
		digests: newTTLCache[string, *HashedJar](digestTTL, cacheEntries),
		results: newTTLCache[string, *Result](resultTTL, cacheEntries),
	}
}

// Sources answer in order, the first that knows a jar wins
func (c *Checker) Check(ctx context.Context, sources []Source, jars []Jar, t Target) []*Result {
	out := make([]*Result, len(jars))
	var pending []int
	hashed := make([]*HashedJar, len(jars))
	for i, j := range jars {
		h, err := c.digest(j)
		if err != nil {
			out[i] = &Result{Jar: j, Status: StatusError, Err: "could not read the jar: " + err.Error()}
			continue
		}
		hashed[i] = h
		pending = append(pending, i)
	}

	failures := map[int][]string{}
	for _, src := range sources {
		if len(pending) == 0 {
			break
		}
		var ask []*HashedJar
		var askIdx []int
		var still []int
		for _, i := range pending {
			if r, ok := c.results.get(c.resultKey(src, hashed[i], t)); ok {
				if r != nil {
					out[i] = withJar(r, jars[i])
				} else {
					still = append(still, i)
				}
				continue
			}
			ask = append(ask, hashed[i])
			askIdx = append(askIdx, i)
		}
		if len(ask) > 0 {
			found, err := src.Lookup(ctx, ask, t)
			if err != nil {
				for _, i := range askIdx {
					failures[i] = append(failures[i], fmt.Sprintf("%s: %v", sourceName(src.Kind()), err))
				}
				still = append(still, askIdx...)
			} else {
				for n, i := range askIdx {
					r := found[ask[n].Path()]
					// Failures ask again on the next check
					if r == nil || r.Status != StatusError {
						c.results.put(c.resultKey(src, ask[n], t), r)
					}
					if r != nil {
						out[i] = withJar(r, jars[i])
					} else {
						still = append(still, i)
					}
				}
			}
		}
		slices.Sort(still)
		pending = still
	}

	for _, i := range pending {
		if errs := failures[i]; len(errs) > 0 {
			out[i] = &Result{Jar: jars[i], Status: StatusError, Err: strings.Join(errs, "; ")}
		} else {
			out[i] = &Result{Jar: jars[i], Status: StatusUnknown}
		}
	}
	return out
}

// Cached verdicts are shared, each caller gets its own copy
func withJar(r *Result, j Jar) *Result {
	cp := *r
	cp.Jar = j
	return &cp
}

func (c *Checker) resultKey(src Source, h *HashedJar, t Target) string {
	return fmt.Sprintf("%d|%s|%s", src.Kind(), h.SHA512, t.key())
}

// Hashes a jar once per file state
func (c *Checker) digest(j Jar) (*HashedJar, error) {
	key := fmt.Sprintf("%s|%d|%d", j.Path(), j.Size, j.ModTime.UnixNano())
	if h, ok := c.digests.get(key); ok {
		cp := *h
		cp.Jar = j
		return &cp, nil
	}
	h, err := HashJar(j)
	if err != nil {
		return nil, err
	}
	c.digests.put(key, h)
	return h, nil
}

// Computes the digests Modrinth and CurseForge look files up by
func HashJar(j Jar) (*HashedJar, error) {
	if j.Root == nil {
		return nil, errors.New("jar folder is not open")
	}
	f, info, err := openRegular(j.Root, j.Name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	j.Size = info.Size()
	s1, s512 := sha1.New(), sha512.New()
	if _, err := io.Copy(io.MultiWriter(s1, s512), f); err != nil {
		return nil, err
	}
	fp, err := utils.CFFingerprintReader(f)
	if err != nil {
		return nil, err
	}
	return &HashedJar{
		Jar:     j,
		SHA1:    hex.EncodeToString(s1.Sum(nil)),
		SHA512:  hex.EncodeToString(s512.Sum(nil)),
		CFPrint: fp,
	}, nil
}

// Indexer loader names whose builds run on this server
func TargetLoaders(loader v1.ModLoader, dataPath, modsDir string) []string {
	if minecraft.LoaderFor(loader).ModsDirectory == "plugins" {
		return pluginLoaders(loader)
	}
	return minecraft.DialectFacets(minecraft.ResolveDialects(loader, dataPath, modsDir))
}

// Plugin platforms each server type loads, most specific first
func pluginLoaders(loader v1.ModLoader) []string {
	switch loader {
	case v1.ModLoader_MOD_LOADER_FOLIA:
		// Folia only runs plugins built for its threading model
		return []string{"folia"}
	case v1.ModLoader_MOD_LOADER_PURPUR:
		return []string{"purpur", "paper", "spigot", "bukkit"}
	case v1.ModLoader_MOD_LOADER_SPIGOT:
		return []string{"spigot", "bukkit"}
	case v1.ModLoader_MOD_LOADER_BUKKIT, v1.ModLoader_MOD_LOADER_GLOWSTONE:
		return []string{"bukkit"}
	}
	return []string{"paper", "spigot", "bukkit"}
}

func sourceName(s v1.ModUpdateSource) string {
	switch s {
	case v1.ModUpdateSource_MOD_UPDATE_SOURCE_MODRINTH:
		return "Modrinth"
	case v1.ModUpdateSource_MOD_UPDATE_SOURCE_CURSEFORGE:
		return "CurseForge"
	}
	return "unknown"
}
