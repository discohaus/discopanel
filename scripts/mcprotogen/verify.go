package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
)

const verifyTimeout = 6 * time.Minute

// Joins the hub codec with the official client for one version
func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	javaBin := fs.String("java", "java", "java binary able to run the client")
	cache := fs.String("cache", defaultCache(), "client and asset cache")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("name exactly one version")
	}
	version := fs.Arg(0)
	m, err := fetchManifest()
	if err != nil {
		return err
	}
	entry, ok := m.find(version)
	if !ok {
		return fmt.Errorf("version %q is not in the manifest", version)
	}
	meta, err := fetchMeta(*cache, entry)
	if err != nil {
		return err
	}
	major, err := javaMajor(*javaBin)
	if err != nil {
		return err
	}
	if need := meta.JavaVersion.MajorVersion; major < need {
		return fmt.Errorf("needs java %d, %s is java %d", need, *javaBin, major)
	}
	dir := filepath.Join(*cache, version, "client")
	cmdline, err := prepareClient(*javaBin, dir, meta)
	if err != nil {
		return err
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	outcome := make(chan error, 1)
	go func() { outcome <- serveHub(ctx, l, 3, true) }()

	cmdline = append(cmdline, "--quickPlayMultiplayer", "127.0.0.1:"+strconv.Itoa(port))
	client := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	client.Dir = dir
	logFile, err := os.Create(filepath.Join(dir, "client.log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	client.Stdout, client.Stderr = logFile, logFile
	logf("launching %s client, log in %s", version, logFile.Name())
	if err := client.Start(); err != nil {
		return err
	}
	clientExit := make(chan error, 1)
	go func() { clientExit <- client.Wait() }()

	var result error
	select {
	case result = <-outcome:
	case err := <-clientExit:
		result = fmt.Errorf("client exited before joining: %v\n%s", err, lastLogLines(logFile.Name(), 15))
	case <-ctx.Done():
		result = fmt.Errorf("no verdict within %s", verifyTimeout)
	}
	// Grace lets the client log its disconnect reason
	select {
	case <-clientExit:
	case <-time.After(5 * time.Second):
		client.Process.Kill()
		<-clientExit
	}
	if result != nil {
		return fmt.Errorf("%s failed: %w\n%s", version, result, clientReasons(logFile.Name()))
	}
	logf("%s verified", version)
	return nil
}

// Disconnect and packet errors the client logged
func clientReasons(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "disconnect") || strings.Contains(lower, "exception") || strings.Contains(lower, "packet") {
			out = append(out, line)
		}
	}
	if len(out) > 20 {
		out = out[len(out)-20:]
	}
	return strings.Join(out, "\n")
}

func lastLogLines(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return tail(string(raw), n)
}

// Downloads the client and builds its launch command
func prepareClient(javaBin, dir string, meta *versionMeta) ([]string, error) {
	var classpath []string
	for _, lib := range meta.Libraries {
		if !libraryAllowed(lib) || lib.Downloads.Artifact == nil {
			continue
		}
		art := lib.Downloads.Artifact
		path := filepath.Join(dir, "libraries", filepath.FromSlash(art.Path))
		if err := downloadFile(download{URL: art.URL, SHA1: art.SHA1}, path); err != nil {
			return nil, err
		}
		classpath = append(classpath, path)
	}
	clientJar := filepath.Join(dir, "client.jar")
	if err := downloadFile(meta.Downloads["client"], clientJar); err != nil {
		return nil, err
	}
	classpath = append(classpath, clientJar)
	indexPath := filepath.Join(dir, "assets", "indexes", meta.AssetIndex.ID+".json")
	if err := downloadFile(download{URL: meta.AssetIndex.URL, SHA1: meta.AssetIndex.SHA1}, indexPath); err != nil {
		return nil, err
	}
	if err := downloadAssets(indexPath, filepath.Join(dir, "assets", "objects")); err != nil {
		return nil, err
	}
	natives := filepath.Join(dir, "natives")
	if err := os.MkdirAll(natives, 0o755); err != nil {
		return nil, err
	}
	if err := writeClientOptions(filepath.Join(dir, "options.txt")); err != nil {
		return nil, err
	}
	subst := strings.NewReplacer(
		"${natives_directory}", natives,
		"${launcher_name}", "mcprotogen",
		"${launcher_version}", "1",
		"${classpath}", strings.Join(classpath, string(os.PathListSeparator)),
		"${auth_player_name}", "mcprotogen",
		"${version_name}", meta.ID,
		"${game_directory}", dir,
		"${assets_root}", filepath.Join(dir, "assets"),
		"${assets_index_name}", meta.AssetIndex.ID,
		"${auth_uuid}", offlineHex("mcprotogen"),
		"${auth_access_token}", "0",
		"${clientid}", "0",
		"${auth_xuid}", "0",
		"${version_type}", "release",
	)
	cmd := []string{javaBin, "-Xmx2G"}
	for _, raw := range meta.Arguments.JVM {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cmd = append(cmd, subst.Replace(s))
		}
	}
	cmd = append(cmd, meta.MainClass)
	for _, raw := range meta.Arguments.Game {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			cmd = append(cmd, subst.Replace(s))
		}
	}
	return cmd, nil
}

// Options keeping the client out of first launch screens and vsync waits
var clientOptions = map[string]string{
	"onboardAccessibility":   "false",
	"enableVsync":            "false",
	"skipMultiplayerWarning": "true",
	"joinedFirstServer":      "true",
	"tutorialStep":           "none",
	"pauseOnLostFocus":       "false",
}

// Merges the verifier options over whatever the client saved
func writeClientOptions(path string) error {
	var lines []string
	seen := map[string]bool{}
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			key, _, ok := strings.Cut(line, ":")
			if ok {
				if v, override := clientOptions[key]; override {
					line = key + ":" + v
					seen[key] = true
				}
			}
			if line != "" {
				lines = append(lines, line)
			}
		}
	}
	for key, v := range clientOptions {
		if !seen[key] {
			lines = append(lines, key+":"+v)
		}
	}
	sort.Strings(lines)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

const assetBase = "https://resources.download.minecraft.net/"

// Fetches every indexed asset the renderer can touch, sounds excluded
func downloadAssets(indexPath, objects string) error {
	var index struct {
		Objects map[string]struct {
			Hash string `json:"hash"`
		} `json:"objects"`
	}
	if err := readJSON(indexPath, &index); err != nil {
		return err
	}
	var jobs []download
	paths := map[string]string{}
	for name, obj := range index.Objects {
		if strings.HasPrefix(name, "minecraft/sounds/") {
			continue
		}
		path := filepath.Join(objects, obj.Hash[:2], obj.Hash)
		if _, ok := paths[path]; ok {
			continue
		}
		paths[path] = name
		jobs = append(jobs, download{URL: assetBase + obj.Hash[:2] + "/" + obj.Hash, SHA1: obj.Hash})
	}
	errs := make(chan error, len(jobs))
	sem := make(chan struct{}, 16)
	for _, job := range jobs {
		sem <- struct{}{}
		go func(job download) {
			defer func() { <-sem }()
			errs <- downloadFile(job, filepath.Join(objects, job.SHA1[:2], job.SHA1))
		}(job)
	}
	for range jobs {
		if err := <-errs; err != nil {
			return err
		}
	}
	logf("%d assets ready", len(jobs))
	return nil
}

func offlineHex(name string) string {
	id := mojang.OfflineUUID(name)
	return fmt.Sprintf("%x", id[:])
}

// Linux is the only platform the verifier launches on
func libraryAllowed(lib library) bool {
	for _, r := range lib.Rules {
		if r.OS == nil {
			continue
		}
		match := r.OS.Name == "linux"
		if r.Action == "allow" && !match {
			return false
		}
		if r.Action == "disallow" && match {
			return false
		}
	}
	return true
}
