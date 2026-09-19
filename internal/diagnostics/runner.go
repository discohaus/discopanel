// Package diagnostics runs panel self checks and release probes
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/docker/docker/api/types/container"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// What kicked a run off
const (
	TriggerStartup = "startup"
	TriggerManual  = "manual"
	TriggerBundle  = "bundle"
)

// Settings row the last report persists under
const reportSettingKey = "diagnostics.last_report"

// Wall clock caps for one run and one check
const (
	runBudget    = 60 * time.Second
	checkBudget  = 15 * time.Second
	startupDelay = 3 * time.Second
)

// Docs pages remedies point at
const (
	docsBase            = "https://docs.discopanel.app"
	docsTroubleshooting = docsBase + "/troubleshooting/"
	docsConfiguration   = docsBase + "/configuration/"
	docsProxy           = docsBase + "/guides/proxy/"
	docsModpacks        = docsBase + "/guides/modpacks/"
	docsTLS             = docsBase + "/guides/tls/"
	docsOIDC            = docsBase + "/configuration/"
	docsFAQ             = docsBase + "/faq/"
	docsModules         = docsBase + "/guides/modules/"
)

// Runs checks, keeps the last report, probes releases
type Runner struct {
	store   *db.Store
	docker  *docker.Client
	cfg     *config.Config
	proxy   *proxy.Manager
	log     *logger.Logger
	version string
	http    *http.Client

	mu       sync.Mutex
	last     *v1.DiagnosticReport
	inflight chan struct{}

	versionMu   sync.Mutex
	versionRes  *v1.GetVersionStatusResponse
	versionAt   time.Time
	versionBusy bool
	// Hub heartbeat answers, the release check reads them when telemetry is on
	releases ReleaseSource

	stop     chan struct{}
	stopOnce sync.Once
}

// Creates a runner and loads the persisted report
func NewRunner(store *db.Store, dockerClient *docker.Client, cfg *config.Config, proxyManager *proxy.Manager, log *logger.Logger) *Runner {
	r := &Runner{
		store:   store,
		docker:  dockerClient,
		cfg:     cfg,
		proxy:   proxyManager,
		log:     log,
		version: config.ResolvedVersion(),
		http: &http.Client{
			Timeout:   10 * time.Second,
			Transport: hub.Transport(nil),
			// Redirect targets could be anything, one hop is plenty
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 2 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		stop: make(chan struct{}),
	}
	r.loadPersisted()
	return r
}

// Source of the hub's last heartbeat answer
type ReleaseSource interface {
	// True while heartbeats go out
	Enabled() bool
	// Last answer, when it arrived, and the error of the most recent attempt
	LastHeartbeat() (*hub.HeartbeatResponse, time.Time, error)
}

// True while the heartbeat sender is wired and turned on
func (r *Runner) telemetryOn() bool {
	r.versionMu.Lock()
	src := r.releases
	r.versionMu.Unlock()
	return src != nil && src.Enabled()
}

// Wires the heartbeat sender the release check reads when telemetry is on
func (r *Runner) SetReleaseSource(src ReleaseSource) {
	r.versionMu.Lock()
	r.releases = src
	r.versionMu.Unlock()
}

// Kicks off the startup run and the GitHub release refresher
func (r *Runner) Start() {
	go r.versionLoop()
	if !r.cfg.Diagnostics.RunOnStartup {
		r.log.Info("Startup diagnostics disabled by configuration")
		return
	}
	go func() {
		// Sockets and modules settle before the first pass
		select {
		case <-time.After(startupDelay):
		case <-r.stop:
			return
		}
		if _, err := r.Run(context.Background(), TriggerStartup); err != nil {
			r.log.Error("Startup diagnostics failed: %v", err)
		}
	}()
}

// Stops background refreshers
func (r *Runner) Stop() {
	r.stopOnce.Do(func() { close(r.stop) })
}

// Last completed report and whether one is running now
func (r *Runner) Last() (*v1.DiagnosticReport, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last, r.inflight != nil
}

// Runs every check, joining a run already in flight
// Returns early on ctx cancel while the run continues
func (r *Runner) Run(ctx context.Context, trigger string) (*v1.DiagnosticReport, error) {
	r.mu.Lock()
	if r.inflight != nil {
		done := r.inflight
		r.mu.Unlock()
		return r.await(ctx, done)
	}
	done := make(chan struct{})
	r.inflight = done
	r.mu.Unlock()

	go func() {
		report := r.execute(trigger)
		r.mu.Lock()
		r.last = report
		r.inflight = nil
		r.mu.Unlock()
		r.persist(report)
		r.logSummary(report)
		close(done)
	}()
	return r.await(ctx, done)
}

// Last report when younger than maxAge, else a fresh run
func (r *Runner) Fresh(ctx context.Context, maxAge time.Duration, trigger string) (*v1.DiagnosticReport, error) {
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	if last != nil && last.FinishedAt != nil && time.Since(last.FinishedAt.AsTime()) < maxAge {
		return last, nil
	}
	return r.Run(ctx, trigger)
}

// Waits for the run or the caller giving up
func (r *Runner) await(ctx context.Context, done chan struct{}) (*v1.DiagnosticReport, error) {
	select {
	case <-done:
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.last == nil {
			return nil, errors.New("diagnostics produced no report")
		}
		return r.last, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// One check definition
type spec struct {
	id       string
	category v1.DiagnosticCategory
	title    string
	timeout  time.Duration
	// Nil applies always, false leaves the check out of the run
	when func() bool
	run  func(ctx context.Context, c *check)
}

// Facts checks share within one run
type runState struct {
	selfOnce sync.Once
	self     *container.InspectResponse
	selfErr  error

	pingOnce sync.Once
	pingErr  error

	imageOnce sync.Once
	image     string
	imageErr  error
}

// Executes every check in parallel and assembles the report
func (r *Runner) execute(trigger string) *v1.DiagnosticReport {
	ctx, cancel := context.WithTimeout(context.Background(), runBudget)
	defer cancel()

	started := time.Now()
	specs := slices.DeleteFunc(r.specs(), func(sp spec) bool { return sp.when != nil && !sp.when() })
	state := &runState{}
	results := make([]*v1.DiagnosticCheck, len(specs))
	var wg sync.WaitGroup
	for i, sp := range specs {
		wg.Go(func() {
			results[i] = r.runOne(ctx, state, sp)
		})
	}
	wg.Wait()

	slices.SortStableFunc(results, func(a, b *v1.DiagnosticCheck) int {
		if a.Category != b.Category {
			return int(a.Category) - int(b.Category)
		}
		return strings.Compare(a.Id, b.Id)
	})

	report := &v1.DiagnosticReport{
		StartedAt:  timestamppb.New(started),
		FinishedAt: timestamppb.Now(),
		Trigger:    trigger,
		Version:    r.version,
		Checks:     results,
	}
	for _, c := range results {
		switch c.Severity {
		case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_PASS:
			report.PassCount++
		case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_INFO:
			report.InfoCount++
		case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN:
			report.WarnCount++
		case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_FAIL:
			report.FailCount++
		default:
			report.SkipCount++
		}
	}
	return report
}

// Runs one check under its own deadline and panic guard
func (r *Runner) runOne(ctx context.Context, state *runState, sp spec) *v1.DiagnosticCheck {
	timeout := sp.timeout
	if timeout <= 0 {
		timeout = checkBudget
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := &check{runner: r, state: state, facts: map[string]string{}}
	started := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("Diagnostic %s panicked: %v\n%s", sp.id, rec, debug.Stack())
				c.reset()
				c.fail("Check crashed: %v", rec)
			}
		}()
		sp.run(cctx, c)
	}()
	select {
	case <-done:
	case <-cctx.Done():
		// Late results after a timeout are dropped on purpose
		c = &check{runner: r, state: state, facts: map[string]string{}}
		c.skip("Timed out after %s", timeout.Round(time.Second))
	}

	out := c.proto()
	out.Id = sp.id
	out.Category = sp.category
	out.Title = sp.title
	out.DurationMs = time.Since(started).Milliseconds()
	if out.Severity == v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_UNSPECIFIED {
		out.Severity = v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_INFO
		if out.Summary == "" {
			out.Summary = "No verdict"
		}
	}
	return out
}

// Writes the report to the settings table
func (r *Runner) persist(report *v1.DiagnosticReport) {
	raw, err := protojson.Marshal(report)
	if err != nil {
		r.log.Error("Failed to encode diagnostics report: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row := &v1.SystemSetting{Key: reportSettingKey, Value: string(raw)}
	if err := r.store.UpdateSystemSetting(ctx, row); err != nil {
		r.log.Error("Failed to persist diagnostics report: %v", err)
	}
}

// Restores the previous report so the ui has one immediately
func (r *Runner) loadPersisted() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := r.store.GetSystemSetting(ctx, reportSettingKey)
	if err != nil || row.Value == "" {
		return
	}
	var report v1.DiagnosticReport
	if err := protojson.Unmarshal([]byte(row.Value), &report); err != nil {
		r.log.Warn("Ignoring unreadable persisted diagnostics report: %v", err)
		return
	}
	r.mu.Lock()
	r.last = &report
	r.mu.Unlock()
}

// Logs counts plus one line per warning or failure
func (r *Runner) logSummary(report *v1.DiagnosticReport) {
	elapsed := report.FinishedAt.AsTime().Sub(report.StartedAt.AsTime()).Round(100 * time.Millisecond)
	r.log.Info("Diagnostics (%s): %d passed, %d info, %d warnings, %d failed, %d skipped in %s",
		report.Trigger, report.PassCount, report.InfoCount, report.WarnCount, report.FailCount, report.SkipCount, elapsed)
	for _, c := range report.Checks {
		line := fmt.Sprintf("Diagnostic [%s] %s: %s", c.Id, c.Title, c.Summary)
		if c.Remedy != "" {
			line += " | fix: " + c.Remedy
		}
		switch c.Severity {
		case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_FAIL:
			r.log.Error("%s", line)
		case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN:
			r.log.Warn("%s", line)
		}
	}
}

// Own container inspect, cached for the run
func (c *check) selfContainer(ctx context.Context) (*container.InspectResponse, error) {
	st := c.state
	st.selfOnce.Do(func() {
		if c.runner.docker == nil {
			st.selfErr = errors.New("docker client unavailable")
			return
		}
		st.self, st.selfErr = c.runner.docker.InspectSelf(ctx)
	})
	return st.self, st.selfErr
}
