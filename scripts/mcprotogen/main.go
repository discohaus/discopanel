// Command mcprotogen derives hub wire tables from official Minecraft jars
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/mcdata"
)

const usage = `usage:
  mcprotogen gen [--since VERSION] [--force] [--java BIN] [--cache DIR] [--out DIR] [VERSION...]
  mcprotogen serve [--port N] [--worlds N] [--once]
  mcprotogen verify [--java BIN] [--cache DIR] VERSION
  mcprotogen diff [--java BIN] [--cache DIR] VERSION VERSION`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = runGen(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "diff":
		err = runDiff(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcprotogen:", err)
		os.Exit(1)
	}
}

func defaultCache() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "discopanel-mcprotogen")
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05")+" "+format+"\n", args...)
}

// One manifest release with its resolved protocol number
type resolved struct {
	entry    manifestEntry
	meta     *versionMeta
	protocol int32
}

// Generates one table file per protocol for the requested versions
func runGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	since := fs.String("since", "", "every release from this version onward")
	force := fs.Bool("force", false, "regenerate protocols that already have a file")
	javaBin := fs.String("java", "java", "java binary able to run the newest requested jar")
	cache := fs.String("cache", defaultCache(), "jar and report cache")
	out := fs.String("out", "pkg/mcproto/mcdata/data", "table directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *since == "" && fs.NArg() == 0 {
		return fmt.Errorf("name versions or pass --since")
	}
	m, err := fetchManifest()
	if err != nil {
		return err
	}
	var entries []manifestEntry
	if *since != "" {
		if entries, err = m.releasesSince(*since); err != nil {
			return err
		}
	}
	for _, id := range fs.Args() {
		e, ok := m.find(id)
		if !ok {
			return fmt.Errorf("version %q is not in the manifest", id)
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ReleaseTime.Before(entries[j].ReleaseTime) })

	groups := map[int32][]resolved{}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		meta, err := fetchMeta(*cache, e)
		if err != nil {
			return err
		}
		jv, err := cachedJarVersion(*cache, meta)
		if err != nil {
			return err
		}
		logf("%s speaks protocol %d", e.ID, jv.Protocol)
		groups[jv.Protocol] = append(groups[jv.Protocol], resolved{entry: e, meta: meta, protocol: jv.Protocol})
	}
	protocols := make([]int32, 0, len(groups))
	for p := range groups {
		protocols = append(protocols, p)
	}
	sort.Slice(protocols, func(i, j int) bool { return protocols[i] < protocols[j] })

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, p := range protocols {
		group := groups[p]
		names := make([]string, 0, len(group))
		for _, r := range group {
			names = append(names, r.entry.ID)
		}
		path := filepath.Join(*out, fmt.Sprintf("%d.json", p))
		if raw, err := os.ReadFile(path); err == nil && !*force {
			existing, err := mcdata.Parse(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			existing.Versions = mergeVersions(existing.Versions, names)
			if err := writeProtocol(path, existing); err != nil {
				return err
			}
			logf("protocol %d kept, versions %v", p, existing.Versions)
			continue
		}
		newest := group[len(group)-1]
		doc, err := generate(context.Background(), *javaBin, *cache, newest)
		if err != nil {
			return fmt.Errorf("%s: %w", newest.entry.ID, err)
		}
		doc.Versions = names
		if err := writeProtocol(path, doc); err != nil {
			return err
		}
		logf("protocol %d written from %s", p, newest.entry.ID)
	}
	return nil
}

// Appends names not already present, keeping existing order
func mergeVersions(have, add []string) []string {
	out := append([]string{}, have...)
	for _, name := range add {
		found := false
		for _, h := range out {
			if h == name {
				found = true
				break
			}
		}
		if !found {
			out = append(out, name)
		}
	}
	return out
}

// Builds one protocol document from a jar and a live vanilla server
func generate(ctx context.Context, javaBin, cache string, r resolved) (*mcdata.Protocol, error) {
	major, err := javaMajor(javaBin)
	if err != nil {
		return nil, err
	}
	if need := r.meta.JavaVersion.MajorVersion; major < need {
		return nil, fmt.Errorf("needs java %d, %s is java %d", need, javaBin, major)
	}
	dir := filepath.Join(cache, r.entry.ID)
	jar := filepath.Join(dir, "server.jar")
	logf("%s downloading server jar", r.entry.ID)
	if err := downloadFile(r.meta.Downloads["server"], jar); err != nil {
		return nil, err
	}
	logf("%s running data generator", r.entry.ID)
	if err := runDatagen(javaBin, jar, filepath.Join(dir, "gen")); err != nil {
		return nil, err
	}
	rep, err := parseReports(filepath.Join(dir, "gen"))
	if err != nil {
		return nil, err
	}
	doc := &mcdata.Protocol{
		Protocol:         r.protocol,
		Packets:          rep.packets,
		EntityTypes:      map[string]int32{},
		BlockEntityTypes: map[string]int32{},
		BlockStates:      map[string]int32{},
	}
	for _, name := range []string{"player"} {
		id, err := rep.registryID("entity_type", name)
		if err != nil {
			return nil, err
		}
		doc.EntityTypes["minecraft:"+name] = id
	}
	for _, name := range []string{"sign", "beacon"} {
		id, err := rep.registryID("block_entity_type", name)
		if err != nil {
			return nil, err
		}
		doc.BlockEntityTypes["minecraft:"+name] = id
	}
	for _, spelling := range family.PaletteStates() {
		id, err := rep.stateID(spelling)
		if err != nil {
			return nil, err
		}
		doc.BlockStates[spelling] = id
	}
	cap, err := captureVanilla(ctx, javaBin, jar, filepath.Join(dir, "run"), rep, r.protocol, r.entry.ID, logf)
	if err != nil {
		return nil, err
	}
	doc.FeatureFlags = cap.FeatureFlags
	doc.Registries = cap.Registries
	doc.Tags = cap.Tags
	return doc, nil
}
