// Benchmarks server container runtimes with external measurement only
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	var (
		configPath = flag.String("config", "", "YAML config path, defaults built in")
		outDir     = flag.String("out", "results", "output directory for results.json and report.md")
		cacheDir   = flag.String("cache", ".cache", "download cache directory")
		workDir    = flag.String("work", "", "server data dir parent, defaults to out/work")
		keep       = flag.Bool("keep", false, "keep server data dirs after each run")
		iterations = flag.Int("iterations", 0, "override config iterations")
		bots       = flag.Int("bots", -1, "override config bot count")
		duration   = flag.Duration("duration", 0, "override config load duration")
		onlyScn    = flag.String("scenario", "", "run only the named scenario")
		onlyCnt    = flag.String("contender", "", "run only the named contender")
	)
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fatal("config: %v", err)
	}
	if *iterations > 0 {
		cfg.Iterations = *iterations
	}
	if *bots >= 0 {
		cfg.Bots = *bots
	}
	if *duration > 0 {
		cfg.LoadDuration = *duration
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	runner, err := newDockerRunner()
	if err != nil {
		fatal("docker: %v", err)
	}

	absOut, err := filepath.Abs(*outDir)
	if err != nil {
		fatal("out dir: %v", err)
	}
	absCache, err := filepath.Abs(*cacheDir)
	if err != nil {
		fatal("cache dir: %v", err)
	}
	work := *workDir
	if work == "" {
		work = filepath.Join(absOut, "work")
	}
	if work, err = filepath.Abs(work); err != nil {
		fatal("work dir: %v", err)
	}

	host, _ := os.Hostname()
	report := &Report{StartedAt: time.Now(), Host: host, Config: cfg}

	for _, scn := range cfg.Scenarios {
		if *onlyScn != "" && scn.Name != *onlyScn {
			continue
		}
		for _, cnt := range cfg.Contenders {
			if *onlyCnt != "" && cnt.Name != *onlyCnt {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			cell := runCell(ctx, runner, cfg, scn, cnt, work, absCache, *keep)
			report.Cells = append(report.Cells, cell)
		}
	}

	report.FinishedAt = time.Now()
	if err := report.write(absOut); err != nil {
		fatal("writing report: %v", err)
	}
	fmt.Printf("\ndiscobench: wrote %s and %s\n",
		filepath.Join(absOut, "results.json"), filepath.Join(absOut, "report.md"))
}

// Benchmarks one scenario and contender pairing
func runCell(ctx context.Context, runner *dockerRunner, cfg *Config, scn Scenario, cnt ContenderCfg, work, cache string, keep bool) CellResult {
	cell := CellResult{Scenario: scn.Name, Contender: cnt.Name, Image: cnt.Image}

	fmt.Printf("\n=== %s / %s (%s) ===\n", scn.Name, cnt.Name, cnt.Image)
	pullSecs, sizeBytes, err := runner.ensureImage(ctx, cnt.Image)
	if err != nil {
		cell.Iterations = append(cell.Iterations, IterationResult{Error: err.Error()})
		return cell
	}
	cell.PullSeconds = pullSecs
	cell.ImageSizeMB = float64(sizeBytes) / 1024 / 1024

	for i := range cfg.Iterations {
		if ctx.Err() != nil {
			break
		}
		fmt.Printf("--- iteration %d/%d ---\n", i+1, cfg.Iterations)
		res := runIteration(ctx, runner, cfg, scn, cnt, work, cache, i, keep)
		if res.Error != "" {
			fmt.Printf("    FAILED: %s\n", res.Error)
		}
		cell.Iterations = append(cell.Iterations, res)
	}
	return cell
}

// Runs one full pass from cold boot to warm boot
func runIteration(ctx context.Context, runner *dockerRunner, cfg *Config, scn Scenario, cnt ContenderCfg, work, cache string, iter int, keep bool) (res IterationResult) {
	fail := func(stage string, err error) IterationResult {
		res.Error = fmt.Sprintf("%s: %v", stage, err)
		return res
	}

	dataDir := filepath.Join(work, fmt.Sprintf("%s-%s-%d", scn.Name, cnt.Name, iter))
	if err := os.RemoveAll(dataDir); err != nil {
		return fail("cleaning data dir", err)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fail("creating data dir", err)
	}
	if !keep {
		defer os.RemoveAll(dataDir)
	}

	var env []string
	switch cnt.Kind {
	case KindDiscoPanel:
		jar, err := serverJar(scn, cache)
		if err != nil {
			return fail("resolving server jar", err)
		}
		if err := prepareDiscoPanel(cfg, scn, dataDir, jar); err != nil {
			return fail("provisioning data dir", err)
		}
		env = discopanelEnv(cfg, cnt.Env)
	case KindItzg:
		env = itzgEnv(cfg, scn, cnt.Env)
	}

	name := containerName(scn.Name, cnt.Name, fmt.Sprintf("%d", iter))
	bc, err := runner.createContainer(ctx, name, cnt.Image, dataDir, env, cfg)
	if err != nil {
		return fail("creating container", err)
	}
	defer bc.remove(context.WithoutCancel(ctx))

	// Cold boot races ping readiness against Done line
	if err := bc.start(ctx); err != nil {
		return fail("starting container", err)
	}
	doneCh := make(chan error, 1)
	go func() {
		secs, err := bc.waitDone(ctx, bc.startedAt, cfg.ReadyTimeout)
		res.ColdDoneSeconds = secs
		doneCh <- err
	}()
	slpSecs, slpErr := pingUntilUp(ctx, bc.addr(), bc.startedAt, cfg.ReadyTimeout)
	if err := <-doneCh; err != nil {
		return fail("cold boot", err)
	}
	if slpErr != nil {
		return fail("cold boot ping", slpErr)
	}
	res.ColdSLPSeconds = slpSecs
	fmt.Printf("    cold: ping %.1fs, done %.1fs\n", res.ColdSLPSeconds, res.ColdDoneSeconds)

	// Idle settle, then read resident memory
	select {
	case <-ctx.Done():
		return fail("idle settle", ctx.Err())
	case <-time.After(20 * time.Second):
	}
	res.IdleRSSMB = lastRSS(ctx, bc, 8*time.Second)
	fmt.Printf("    idle rss: %.0f MB\n", res.IdleRSSMB)

	// Load phase with the bot swarm and stats sampling
	if cfg.Bots > 0 && scn.BotsSupported {
		statsCtx, stopStats := context.WithCancel(ctx)
		statsCh := bc.sampleStats(statsCtx)
		var cpuSum, cpuMax, rssMax float64
		var cpuN int
		statsDone := make(chan struct{})
		go func() {
			defer close(statsDone)
			for s := range statsCh {
				cpuSum += s.CPUPercent
				cpuN++
				cpuMax = math.Max(cpuMax, s.CPUPercent)
				rssMax = math.Max(rssMax, s.RSSMB)
			}
		}()

		sw := runSwarm(ctx, cfg, bc.addr())
		stopStats()
		<-statsDone

		tps := summarizeTPS(sw.TPSSamples, cfg.RampSkip)
		res.TPSMedian, res.TPSP5, res.TPSMin, res.TPSMean, res.TPSSamples =
			tps.Median, tps.P5, tps.Min, tps.Mean, tps.Samples
		res.PeakJoined, res.Reconnects, res.JoinFailures = sw.PeakJoined, sw.Reconnects, sw.JoinFailures
		if cpuN > 0 {
			res.LoadCPUMeanPercent = cpuSum / float64(cpuN)
		}
		res.LoadCPUMaxPercent = cpuMax
		res.LoadRSSMaxMB = rssMax
		fmt.Printf("    load: %d joined, tps median %.1f p5 %.1f min %.1f (%d samples), cpu mean %.0f%%\n",
			sw.PeakJoined, tps.Median, tps.P5, tps.Min, tps.Samples, res.LoadCPUMeanPercent)
		if sw.PeakJoined == 0 {
			return fail("load phase", fmt.Errorf("no bot ever joined (protocol mismatch?)"))
		}
	}

	// Graceful stop
	stopSecs, exitCode, err := bc.stopTimed(ctx, cfg.StopTimeout)
	if err != nil {
		return fail("graceful stop", err)
	}
	res.StopSeconds = stopSecs
	res.StopExitCode = exitCode
	fmt.Printf("    stop: %.1fs (exit %d)\n", stopSecs, exitCode)

	// Warm restart on the same data dir
	if err := bc.start(ctx); err != nil {
		return fail("warm start", err)
	}
	warmSecs, err := bc.waitDone(ctx, bc.startedAt, cfg.ReadyTimeout)
	if err != nil {
		return fail("warm boot", err)
	}
	res.WarmDoneSeconds = warmSecs
	fmt.Printf("    warm: done %.1fs\n", warmSecs)

	if _, _, err := bc.stopTimed(ctx, cfg.StopTimeout); err != nil {
		return fail("final stop", err)
	}
	return res
}

// Reads stats briefly, returns final resident sample
func lastRSS(ctx context.Context, bc *benchContainer, window time.Duration) float64 {
	sctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	var rss float64
	for s := range bc.sampleStats(sctx) {
		rss = s.RSSMB
	}
	return rss
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "discobench: "+format+"\n", args...)
	os.Exit(1)
}
