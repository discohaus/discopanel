// Runs one panel binary against an isolated data directory
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Docker endpoint nothing listens on, keeps the real daemon untouched
const deadDocker = "tcp://127.0.0.1:1"

// One panel process under capture
type Panel struct {
	Tag    string
	Dir    string
	DBPath string
	Base   string
	cmd    *exec.Cmd
	logs   *os.File
	done   chan error
}

// Writes the config file every release reads from its cwd
func writeConfig(dir string, port int) error {
	cfg := fmt.Sprintf(`server:
  host: 127.0.0.1
  port: "%d"
database:
  path: %s
  auto_migrate: true
storage:
  data_dir: %s
  backup_dir: %s
  temp_dir: %s
docker:
  host: %s
  network_name: discopanel-fixture-%d
proxy:
  enabled: false
module:
  enabled: true
logging:
  enabled: true
  file_path: %s
auth:
  local:
    enabled: true
    allow_registration: false
`, port,
		filepath.Join(dir, "discopanel.db"),
		filepath.Join(dir, "data"),
		filepath.Join(dir, "backups"),
		filepath.Join(dir, "tmp"),
		deadDocker, port,
		filepath.Join(dir, "discopanel.log"))
	return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
}

// Boots one binary and waits until it answers http
func startPanel(ctx context.Context, bin, dir, tag string, logf func(string, ...any)) (*Panel, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"data", "backups", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			return nil, err
		}
	}
	if err := writeConfig(dir, port); err != nil {
		return nil, err
	}
	absBin, err := filepath.Abs(bin)
	if err != nil {
		return nil, err
	}
	logs, err := os.OpenFile(filepath.Join(dir, "panel-"+tag+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(absBin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"DOCKER_HOST="+deadDocker,
		"DISCOPANEL_DOCKER_HOST="+deadDocker,
		"DISCOPANEL_DOCKER.HOST="+deadDocker,
		"APP_VERSION="+tag,
	)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		logs.Close()
		return nil, err
	}
	p := &Panel{
		Tag:    tag,
		Dir:    dir,
		DBPath: filepath.Join(dir, "discopanel.db"),
		Base:   "http://127.0.0.1:" + strconv.Itoa(port),
		cmd:    cmd,
		logs:   logs,
		done:   make(chan error, 1),
	}
	go func() { p.done <- cmd.Wait() }()

	deadline := time.After(2 * time.Minute)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case err := <-p.done:
			logs.Close()
			return nil, fmt.Errorf("%s exited before serving (%v), see %s", tag, err, logs.Name())
		case <-deadline:
			p.Stop()
			return nil, fmt.Errorf("%s never answered on %s, see %s", tag, p.Base, logs.Name())
		case <-ctx.Done():
			p.Stop()
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		resp, err := client.Get(p.Base + "/")
		if err == nil {
			resp.Body.Close()
			logf("%s serving on %s", tag, p.Base)
			return p, nil
		}
	}
}

// Graceful shutdown with a hard fallback
func (p *Panel) Stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	defer p.logs.Close()
	p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
		return nil
	case <-time.After(90 * time.Second):
		p.cmd.Process.Kill()
		<-p.done
		return fmt.Errorf("%s ignored SIGTERM and was killed", p.Tag)
	}
}

// Free loopback tcp port
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
