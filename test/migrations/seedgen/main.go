// Captures upgrade fixtures
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const finalV2 = "v2.0.15"

var stablePattern = regexp.MustCompile(`^v[12]\.[0-9]+\.[0-9]+$`)

func main() {
	out := flag.String("out", "test/migrations/fixtures", "fixture output directory")
	repo := flag.String("repo", "discohaus/discopanel", "github repository holding releases")
	tagList := flag.String("tags", "", "comma separated tags, empty takes every stable tag")
	genesisPath := flag.String("genesis", "internal/db/migrations/genesis.snapshot.json", "genesis snapshot path, empty skips harvest")
	pristine := flag.Bool("pristine", true, "capture one seeded fixture per release")
	chain := flag.Bool("chain", true, "carry one data dir through every release")
	cache := flag.String("cache", "test/migrations/seedgen/cache", "downloaded binary cache")
	flag.Parse()

	tags, err := resolveTags(*tagList)
	if err != nil {
		log.Fatalf("resolve tags: %v", err)
	}
	if len(tags) == 0 {
		log.Fatal("no stable tags found")
	}
	log.Printf("capturing %d releases, %s through %s", len(tags), tags[0], tags[len(tags)-1])

	if err := os.MkdirAll(*out, 0755); err != nil {
		log.Fatalf("mkdir out: %v", err)
	}

	if *pristine {
		for _, tag := range tags {
			if err := capturePristine(*repo, *cache, *out, tag); err != nil {
				log.Printf("SKIP pristine %s: %v", tag, err)
			}
		}
	}

	if *chain {
		if err := captureChain(*repo, *cache, *out, tags); err != nil {
			log.Printf("chain capture stopped: %v", err)
		}
	}

	if *genesisPath != "" {
		if err := harvestGenesis(*repo, *cache, *genesisPath); err != nil {
			log.Printf("genesis harvest failed: %v", err)
		} else {
			log.Printf("genesis snapshot written to %s", *genesisPath)
		}
	}
}

// Stable release tags in ascending version order
func resolveTags(override string) ([]string, error) {
	var raw []string
	if override != "" {
		raw = strings.Split(override, ",")
	} else {
		outBytes, err := exec.Command("git", "tag", "-l", "v*").Output()
		if err != nil {
			return nil, err
		}
		raw = strings.Fields(string(outBytes))
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

// One seeded single release fixture
func capturePristine(repo, cache, out, tag string) error {
	bin, err := fetchBinary(repo, cache, tag)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "discopanel-pristine-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	panel, err := startPanel(bin, dir)
	if err != nil {
		return err
	}
	seed(panel, tag)
	if err := panel.stop(); err != nil {
		return err
	}
	return captureDB(panel.dbPath, filepath.Join(out, "pristine-"+tag+".db.gz"))
}

// One data dir upgraded through every release in order
func captureChain(repo, cache, out string, tags []string) error {
	dir, err := os.MkdirTemp("", "discopanel-chain-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	for i, tag := range tags {
		bin, err := fetchBinary(repo, cache, tag)
		if err != nil {
			log.Printf("SKIP chain hop %s: %v", tag, err)
			continue
		}
		panel, err := startPanel(bin, dir)
		if err != nil {
			return fmt.Errorf("hop %s: %w", tag, err)
		}
		if i == 0 {
			seed(panel, tag)
		}
		if err := panel.stop(); err != nil {
			return fmt.Errorf("hop %s: %w", tag, err)
		}
		if err := captureDB(panel.dbPath, filepath.Join(out, "chain-"+tag+".db.gz")); err != nil {
			return fmt.Errorf("hop %s: %w", tag, err)
		}
		log.Printf("chained through %s", tag)
	}
	return nil
}

// Genesis spec read from an unseeded final v2 boot
func harvestGenesis(repo, cache, genesisPath string) error {
	bin, err := fetchBinary(repo, cache, finalV2)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "discopanel-genesis-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	panel, err := startPanel(bin, dir)
	if err != nil {
		return err
	}
	if err := panel.stop(); err != nil {
		return err
	}

	db, err := gorm.Open(sqlite.Open(panel.dbPath), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return err
	}
	spec, err := migrate.SpecOfDB(db)
	if err != nil {
		return err
	}
	data, err := spec.MarshalCanonical()
	if err != nil {
		return err
	}
	return os.WriteFile(genesisPath, data, 0644)
}

// Downloads and unpacks one release binary
func fetchBinary(repo, cache, tag string) (string, error) {
	arch := runtime.GOARCH
	bin := filepath.Join(cache, tag, "discopanel")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/discopanel-linux-%s.tar.gz", repo, tag, arch)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(filepath.Base(hdr.Name), "discopanel") {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
			return "", err
		}
		f, err := os.OpenFile(bin, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
		return bin, nil
	}
	return "", fmt.Errorf("archive %s holds no binary", url)
}

// One running panel process under capture
type panelProc struct {
	cmd    *exec.Cmd
	base   string
	dbPath string
	token  string
}

// Boots one panel binary against one data dir
func startPanel(bin, dir string) (*panelProc, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "discopanel.db")
	for _, sub := range []string{"data", "backups", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			return nil, err
		}
	}

	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"DISCOPANEL_SERVER_HOST=127.0.0.1",
		"DISCOPANEL_SERVER_PORT="+strconv.Itoa(port),
		"DISCOPANEL_DATABASE_PATH="+dbPath,
		"DISCOPANEL_STORAGE_DATA_DIR="+filepath.Join(dir, "data"),
		"DISCOPANEL_STORAGE_BACKUP_DIR="+filepath.Join(dir, "backups"),
		"DISCOPANEL_PROXY_ENABLED=false",
	)
	logPath := filepath.Join(dir, "panel.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	panel := &panelProc{
		cmd:    cmd,
		base:   "http://127.0.0.1:" + strconv.Itoa(port),
		dbPath: dbPath,
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.Process.Signal(syscall.Signal(0)) != nil {
			return nil, fmt.Errorf("panel exited early, see %s", logPath)
		}
		resp, err := http.Get(panel.base + "/")
		if err == nil {
			resp.Body.Close()
			return panel, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	panel.stop()
	return nil, fmt.Errorf("panel never answered, see %s", logPath)
}

// Graceful shutdown with a hard fallback
func (p *panelProc) stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
		return nil
	case <-time.After(30 * time.Second):
		p.cmd.Process.Kill()
		<-done
		return nil
	}
}

// Free localhost tcp port
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Seeds one panel through the api its era speaks
// Required steps fail loud, optional ones only log
func seed(p *panelProc, tag string) {
	if strings.HasPrefix(tag, "v1.") {
		seedV1(p)
		return
	}
	seedV2(p)
}

// Rest era seeding for v1 releases
func seedV1(p *panelProc) {
	postJSON(p, "/api/v1/auth/register", map[string]any{
		"username": "admin", "email": "admin@example.com", "password": "fixture-Passw0rd!",
	}, false)
	var login struct {
		Token string `json:"token"`
	}
	body, err := postJSON(p, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "fixture-Passw0rd!",
	}, false)
	if err == nil {
		json.Unmarshal(body, &login)
		p.token = login.Token
	}
	if _, err := postJSON(p, "/api/v1/servers", map[string]any{
		"name": "Fixture Survival", "mod_loader": "vanilla", "mc_version": "1.20.1",
		"port": 25565, "max_players": 20, "memory": 4096,
	}, true); err != nil {
		log.Printf("v1 server seed failed: %v", err)
	}
}

// Connect era seeding for v2 releases
func seedV2(p *panelProc) {
	if _, err := postJSON(p, "/discopanel.v1.AuthService/Register", map[string]any{
		"username": "admin", "email": "admin@example.com", "password": "fixture-Passw0rd!",
	}, false); err != nil {
		log.Printf("v2 register failed: %v", err)
	}
	var login struct {
		Token string `json:"token"`
	}
	body, err := postJSON(p, "/discopanel.v1.AuthService/Login", map[string]any{
		"username": "admin", "password": "fixture-Passw0rd!",
	}, false)
	if err != nil {
		log.Printf("v2 login failed: %v", err)
	} else {
		json.Unmarshal(body, &login)
		p.token = login.Token
	}

	if _, err := postJSON(p, "/discopanel.v1.ServerService/CreateServer", map[string]any{
		"name": "Fixture Survival", "modLoader": "MOD_LOADER_VANILLA", "mcVersion": "1.20.1",
		"port": 25565, "maxPlayers": 20, "memory": 4096, "proxyHostname": "play.example.com",
	}, true); err != nil {
		log.Printf("v2 server seed failed: %v", err)
	}
	if _, err := postJSON(p, "/discopanel.v1.AuthService/CreateAPIToken", map[string]any{
		"name": "fixture",
	}, true); err != nil {
		log.Printf("v2 api token seed skipped: %v", err)
	}
	if _, err := postJSON(p, "/discopanel.v1.AuthService/CreateInvite", map[string]any{
		"description": "fixture invite", "roles": []string{"user"},
	}, true); err != nil {
		log.Printf("v2 invite seed skipped: %v", err)
	}
	if _, err := postJSON(p, "/discopanel.v1.TaskService/CreateScheduledTask", map[string]any{
		"task": map[string]any{
			"name": "announce", "taskType": "TASK_TYPE_COMMAND", "schedule": "SCHEDULE_TYPE_CRON",
			"cronExpr": "0 * * * *", "config": `{"command":"say hi"}`,
		},
	}, true); err != nil {
		log.Printf("v2 task seed skipped: %v", err)
	}
}

// Posts one json body, optionally with the session token
func postJSON(p *panelProc, path string, payload any, authed bool) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, p.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authed && p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return data, fmt.Errorf("%s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Snapshots one sqlite database into a gzip file
// Vacuum folds any wal into a single clean copy
func captureDB(dbPath, outPath string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no database captured: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return err
	}
	snap := dbPath + ".snap"
	os.Remove(snap)
	if err := db.Exec("VACUUM INTO ?", snap).Error; err != nil {
		return err
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.Close()
	}
	defer os.Remove(snap)

	in, err := os.Open(snap)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		return err
	}
	return gz.Close()
}
