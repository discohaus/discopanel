package telemetry

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"time"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/diagnostics"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"gorm.io/gorm"
)

// When this process came up, uptime counts from here
var processStart = time.Now()

// Settings row holding the operator's on or off choice
const enabledSettingKey = "telemetry.enabled"

// Returned when a hosted panel tries to change the heartbeat
var ErrManaged = errors.New("telemetry is managed by discohaus on a hosted panel")

// Returned when the config file forces the heartbeat off
var ErrConfigDisabled = errors.New("telemetry.enabled is false in the config, the switch is locked")

// Sends heartbeats on the hub's cadence and remembers the last answer
type Sender struct {
	store  *storage.Store
	docker *docker.Client
	cfg    *config.Config
	diag   *diagnostics.Runner
	log    *logger.Logger

	mu        sync.Mutex
	enabled   bool
	last      *hub.HeartbeatResponse
	lastAt    time.Time
	lastErr   error
	successes int
	failures  int
	lastWarn  time.Time

	// Nudges the loop after a toggle
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// Creates a sender with the persisted choice, nothing runs until Start
func New(store *storage.Store, dockerClient *docker.Client, cfg *config.Config, diag *diagnostics.Runner, log *logger.Logger) *Sender {
	s := &Sender{
		store:  store,
		docker: dockerClient,
		cfg:    cfg,
		diag:   diag,
		log:    log,
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	s.enabled = s.loadEnabled()
	return s
}

// Reads the persisted choice, hosted panels and a missing row mean on
// Config false wins over the row
func (s *Sender) loadEnabled() bool {
	if hub.Hosted() {
		return true
	}
	if !s.cfg.Telemetry.Enabled {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := s.store.GetSystemSetting(ctx, enabledSettingKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true
	}
	if err != nil {
		s.log.Error("Telemetry setting unreadable, heartbeat stays on: %v", err)
		return true
	}
	return row.Value != "false"
}

// True while heartbeats go out
func (s *Sender) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// True on a hosted panel, where the heartbeat cannot be turned off
func (s *Sender) Managed() bool {
	return hub.Hosted()
}

// True when the config file forces the heartbeat off
func (s *Sender) ConfigDisabled() bool {
	return !hub.Hosted() && !s.cfg.Telemetry.Enabled
}

// Persists the choice and beats at once when turned on
func (s *Sender) SetEnabled(ctx context.Context, on bool) error {
	if hub.Hosted() {
		return ErrManaged
	}
	if !s.cfg.Telemetry.Enabled {
		return ErrConfigDisabled
	}
	value := "false"
	if on {
		value = "true"
	}
	if err := s.store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: enabledSettingKey, Value: value}); err != nil {
		return fmt.Errorf("persist %s: %w", enabledSettingKey, err)
	}
	s.mu.Lock()
	changed := s.enabled != on
	s.enabled = on
	s.mu.Unlock()
	if changed {
		s.log.Info("Telemetry heartbeat turned %s", map[bool]string{true: "on", false: "off"}[on])
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Starts the heartbeat loop, the first beat goes out after FirstDelay
func (s *Sender) Start() {
	s.startOnce.Do(func() { go s.loop() })
}

// Stops the loop and waits for an in flight heartbeat to finish
func (s *Sender) Stop() {
	s.stopOnce.Do(func() {
		close(s.stop)
		s.startOnce.Do(func() { close(s.done) })
		<-s.done
	})
}

// Last answer the hub gave, when it arrived, and the error of the most recent attempt
// A nil answer means no heartbeat has succeeded yet
func (s *Sender) LastHeartbeat() (*hub.HeartbeatResponse, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.lastAt, s.lastErr
}

// Runs heartbeats until Stop, parking while turned off
func (s *Sender) loop() {
	defer close(s.done)
	delay := FirstDelay
	for {
		if !s.Enabled() {
			select {
			case <-s.stop:
				return
			case <-s.wake:
			}
			// Turned back on, beat right away
			delay = 0
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-s.stop:
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
			continue
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), beatTimeout)
		resp, err := s.beat(ctx)
		cancel()
		delay = s.record(resp, err)
	}
}

// Books the outcome of one attempt and picks the next delay
func (s *Sender) record(resp *hub.HeartbeatResponse, err error) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
	if err == nil {
		s.last = resp
		s.lastAt = time.Now()
		s.failures = 0
		s.successes++
		delay := NextDelay(resp)
		if s.successes == 1 {
			s.log.Info("Telemetry heartbeat reached %s, next in %s", hub.SupportBase(), delay)
		}
		return delay
	}
	s.failures++
	delay := RetryDelay(s.failures, err)
	if s.failures == 1 || time.Since(s.lastWarn) >= warnEvery {
		s.lastWarn = time.Now()
		if he := hub.AsError(err); he != nil && he.Paused() {
			s.log.Warn("Telemetry heartbeat: %s has paused telemetry, next attempt in %s", hub.SupportBase(), delay)
		} else {
			s.log.Warn("Telemetry heartbeat to %s failed (attempt %d): %v, retrying in %s", hub.SupportBase(), s.failures, err, delay)
		}
	}
	return delay
}

// Gathers facts and sends one heartbeat
func (s *Sender) beat(ctx context.Context) (*hub.HeartbeatResponse, error) {
	facts, err := s.Facts(ctx)
	if err != nil {
		return nil, err
	}
	return hub.Heartbeat(ctx, BuildPayload(facts))
}

// Everything the next heartbeat reports, read fresh from the panel
func (s *Sender) Facts(ctx context.Context) (Facts, error) {
	port, err := strconv.Atoi(s.cfg.Server.Port)
	if err != nil {
		return Facts{}, fmt.Errorf("server.port %q is not a number", s.cfg.Server.Port)
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return Facts{}, fmt.Errorf("list servers: %w", err)
	}
	modules, err := s.store.ListModules(ctx)
	if err != nil {
		return Facts{}, fmt.Errorf("list modules: %w", err)
	}
	// A daemon that does not answer has no version to report, the heartbeat still goes out
	dockerVersion, err := s.docker.ServerVersion(ctx)
	if err != nil {
		dockerVersion = ""
	}
	images := make([]string, 0, len(servers))
	for _, server := range servers {
		images = append(images, s.docker.DesiredImage(server))
	}
	report, _ := s.diag.Last()
	return Facts{
		InstallID:     hub.InstallID(),
		Version:       config.ResolvedVersion(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		DockerVersion: dockerVersion,
		Servers:       servers,
		Modules:       modules,
		RuntimeImages: images,
		Report:        report,
		PanelPort:     port,
		Uptime:        time.Since(processStart),
	}, nil
}
