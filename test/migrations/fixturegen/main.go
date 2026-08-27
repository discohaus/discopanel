// Boots every released panel, seeds it, and captures its database
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/test/migrations/seed"
)

// Release tags worth capturing, prereleases excluded
var stablePattern = regexp.MustCompile(`^v[12]\.[0-9]+\.[0-9]+$`)

// Everything the command line configured
type options struct {
	out          string
	cache        string
	repo         string
	repoDir      string
	tags         string
	hop          string
	jobs         int
	repeat       int
	force        bool
	buildMissing bool
	keepWork     bool
	lenient      bool
	timeout      time.Duration
	skip         string
}

func main() {
	var opt options
	flag.StringVar(&opt.out, "out", "fixtures", "fixture output directory")
	flag.StringVar(&opt.cache, "cache", "fixturegen/cache", "downloaded and built binary cache")
	flag.StringVar(&opt.repo, "repo", "discohaus/discopanel", "github repository holding releases")
	flag.StringVar(&opt.repoDir, "repo-dir", "", "git checkout holding the tags, default the enclosing repository")
	flag.StringVar(&opt.tags, "tags", "", "comma separated tags, empty takes every stable v1 and v2 tag")
	flag.StringVar(&opt.hop, "hop", "v2.0.15", "final v2 release every fixture also boots through, empty skips")
	flag.IntVar(&opt.jobs, "jobs", min(4, runtime.NumCPU()), "panels captured at once")
	flag.IntVar(&opt.repeat, "repeat", 2, "attempts per create procedure")
	flag.BoolVar(&opt.force, "force", false, "recapture fixtures that already exist")
	flag.BoolVar(&opt.buildMissing, "build-missing", true, "build tags lacking a release binary from source")
	flag.BoolVar(&opt.keepWork, "keep-work", false, "leave panel work directories behind")
	flag.BoolVar(&opt.lenient, "lenient", false, "exit zero even when some tags failed")
	flag.DurationVar(&opt.timeout, "timeout", 20*time.Minute, "budget per tag")
	flag.StringVar(&opt.skip, "skip", seed.DefaultSkip.String(), "regexp of procedure names never called")
	flag.Parse()

	if opt.repoDir == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			log.Fatalf("find repository: %v", err)
		}
		opt.repoDir = strings.TrimSpace(string(top))
	}
	skip, err := regexp.Compile(opt.skip)
	if err != nil {
		log.Fatalf("bad skip pattern: %v", err)
	}

	tags, err := resolveTags(opt.repoDir, opt.tags)
	if err != nil {
		log.Fatalf("resolve tags: %v", err)
	}
	if len(tags) == 0 {
		log.Fatal("no stable tags found")
	}
	if err := os.MkdirAll(opt.out, 0755); err != nil {
		log.Fatalf("mkdir out: %v", err)
	}
	log.Printf("capturing %d releases, %s through %s, %d at a time", len(tags), tags[0], tags[len(tags)-1], opt.jobs)

	manifestPath := filepath.Join(opt.out, "manifest.json")
	manifest := loadManifest(manifestPath)
	manifest.Hop = opt.hop

	// Every job shares the hop binary, fetched once here
	if opt.hop != "" {
		if _, _, err := fetchBinary(context.Background(), opt, opt.hop); err != nil {
			log.Fatalf("fetch hop %s: %v", opt.hop, err)
		}
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	queue := make(chan string)
	failed := 0
	for i := 0; i < opt.jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tag := range queue {
				entry := capture(opt, skip, tag, manifest.find(tag))
				mu.Lock()
				manifest.put(entry)
				if entry.Error != "" {
					failed++
					log.Printf("[%s] FAILED %s", tag, entry.Error)
				} else {
					log.Printf("[%s] done in %s, %d tables seeded", tag, time.Duration(entry.DurationMs)*time.Millisecond, len(entry.Tables))
				}
				if err := manifest.save(manifestPath); err != nil {
					log.Printf("save manifest: %v", err)
				}
				mu.Unlock()
			}
		}()
	}
	for _, tag := range tags {
		queue <- tag
	}
	close(queue)
	wg.Wait()

	manifest.GeneratedAt = time.Now().UTC()
	if err := manifest.save(manifestPath); err != nil {
		log.Fatalf("save manifest: %v", err)
	}
	log.Printf("manifest written to %s", manifestPath)
	if failed > 0 {
		log.Printf("%d of %d tags failed", failed, len(tags))
		if !opt.lenient {
			os.Exit(1)
		}
	}
}

// Stable release tags in ascending version order
func resolveTags(repoDir, override string) ([]string, error) {
	var raw []string
	if override != "" {
		raw = strings.Split(override, ",")
	} else {
		out, err := exec.Command("git", "-C", repoDir, "tag", "-l", "v*").Output()
		if err != nil {
			return nil, err
		}
		raw = strings.Fields(string(out))
	}
	var tags []string
	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		if stablePattern.MatchString(tag) {
			tags = append(tags, tag)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return versionLess(tags[i], tags[j]) })
	return tags, nil
}

// Semver ordering over vX.Y.Z tags
func versionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func versionParts(tag string) [3]int {
	var out [3]int
	for i, part := range strings.SplitN(strings.TrimPrefix(tag, "v"), ".", 3) {
		out[i], _ = strconv.Atoi(part)
	}
	return out
}

// Fixture file name for one tag
func fixtureName(tag string) string {
	return tag + ".db.gz"
}

// Fixture file name for one tag booted through the hop
func hopName(tag, hop string) string {
	return tag + ".via-" + hop + ".db.gz"
}

// Captures one release end to end
func capture(opt options, skip *regexp.Regexp, tag string, previous *VersionEntry) *VersionEntry {
	start := time.Now()
	entry := &VersionEntry{Tag: tag, Fixture: fixtureName(tag)}
	wantHop := opt.hop != "" && tag != opt.hop
	if wantHop {
		entry.HopFixture = hopName(tag, opt.hop)
	}
	fixturePath := filepath.Join(opt.out, entry.Fixture)
	hopPath := filepath.Join(opt.out, entry.HopFixture)

	if !opt.force && exists(fixturePath) && (!wantHop || exists(hopPath)) {
		if previous != nil && previous.Error == "" {
			previous.Cached = true
			return previous
		}
		entry.Cached = true
		if tables, err := tableCountsGz(fixturePath); err == nil {
			entry.Tables = tables
		}
		if wantHop {
			if tables, err := tableCountsGz(hopPath); err == nil {
				entry.HopTables = tables
			}
		}
		return entry
	}

	ctx, cancel := context.WithTimeout(context.Background(), opt.timeout)
	defer cancel()
	logf := func(format string, args ...any) {
		log.Printf("[%s] "+format, append([]any{tag}, args...)...)
	}

	bin, source, err := fetchBinary(ctx, opt, tag)
	if err != nil {
		entry.Error = fmt.Sprintf("fetch: %v", err)
		return entry
	}
	entry.Source = source

	work, err := os.MkdirTemp("", "discopanel-fixture-"+tag+"-")
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	if opt.keepWork {
		logf("work dir kept at %s", work)
	} else {
		defer os.RemoveAll(work)
	}

	panel, err := startPanel(ctx, bin, work, tag, logf)
	if err != nil && source != "source" && opt.buildMissing {
		// Some early releases shipped without cgo and cannot open sqlite
		logf("release binary failed to boot, building from source (%v)", err)
		if buildErr := buildFromSource(ctx, opt.repoDir, tag, bin); buildErr != nil {
			entry.Error = fmt.Sprintf("boot: %v, rebuild: %v", err, buildErr)
			return entry
		}
		entry.Source = "source"
		panel, err = startPanel(ctx, bin, work, tag, logf)
	}
	if err != nil {
		entry.Error = fmt.Sprintf("boot: %v", err)
		return entry
	}

	surface, err := discover(ctx, opt.repoDir, tag, panel.Base, logf)
	if err != nil {
		panel.Stop()
		entry.Error = fmt.Sprintf("discover: %v", err)
		return entry
	}
	entry.Era = surface.Era
	logf("%s surface with %d procedures", surface.Era, len(surface.Ops))

	seeder := seed.New(surface, seed.NewClient(panel.Base))
	seeder.Repeat = opt.repeat
	seeder.Skip = skip
	seeder.Log = logf
	report, err := seeder.Run(ctx)
	if err != nil {
		panel.Stop()
		entry.Error = fmt.Sprintf("seed: %v", err)
		return entry
	}
	entry.Seed = report

	if err := panel.Stop(); err != nil {
		entry.Error = fmt.Sprintf("stop: %v", err)
		return entry
	}
	if err := captureDB(panel.DBPath, fixturePath); err != nil {
		entry.Error = fmt.Sprintf("capture: %v", err)
		return entry
	}
	if entry.Tables, err = tableCounts(panel.DBPath); err != nil {
		entry.Error = fmt.Sprintf("count: %v", err)
		return entry
	}

	if wantHop {
		hopBin, _, err := fetchBinary(ctx, opt, opt.hop)
		if err != nil {
			entry.Error = fmt.Sprintf("fetch hop: %v", err)
			return entry
		}
		hopPanel, err := startPanel(ctx, hopBin, work, opt.hop, logf)
		if err != nil {
			entry.Error = fmt.Sprintf("hop boot: %v", err)
			return entry
		}
		if err := hopPanel.Stop(); err != nil {
			entry.Error = fmt.Sprintf("hop stop: %v", err)
			return entry
		}
		if err := captureDB(hopPanel.DBPath, hopPath); err != nil {
			entry.Error = fmt.Sprintf("hop capture: %v", err)
			return entry
		}
		if entry.HopTables, err = tableCounts(hopPanel.DBPath); err != nil {
			entry.Error = fmt.Sprintf("hop count: %v", err)
			return entry
		}
	}
	entry.DurationMs = time.Since(start).Milliseconds()
	return entry
}

// Reflection first, the release's own source when absent
func discover(ctx context.Context, repoDir, tag, base string, logf func(string, ...any)) (*seed.Surface, error) {
	probe, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	surface, err := seed.DiscoverConnect(probe, base)
	if err == nil {
		return surface, nil
	}
	logf("no reflection, reading rest routes from source (%v)", err)
	return seed.DiscoverREST(ctx, repoDir, tag)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
