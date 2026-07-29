package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Contender kinds the harness knows how to drive
const (
	KindDiscoPanel = "discopanel"
	KindItzg       = "itzg"
)

// Scenario server types
const (
	TypeVanilla = "vanilla"
	TypePaper   = "paper"
)

// Benchmark matrix plus shared fairness knobs
type Config struct {
	Iterations   int           `yaml:"iterations"`
	Bots         int           `yaml:"bots"`
	LoadDuration time.Duration `yaml:"load_duration"`
	RampSkip     time.Duration `yaml:"ramp_skip"`
	MemoryMB     int           `yaml:"memory_mb"`
	HeapMB       int           `yaml:"heap_mb"`
	CPUs         float64       `yaml:"cpus"`
	Seed         string        `yaml:"seed"`
	ViewDistance int           `yaml:"view_distance"`
	SimDistance  int           `yaml:"simulation_distance"`
	ReadyTimeout time.Duration `yaml:"ready_timeout"`
	StopTimeout  time.Duration `yaml:"stop_timeout"`
	WalkSpeed    float64       `yaml:"walk_speed"`
	WalkRadius   float64       `yaml:"walk_radius"`

	Scenarios  []Scenario    `yaml:"scenarios"`
	Contenders []ContenderCfg `yaml:"contenders"`
}

// One server flavor benchmarked across all contenders
type Scenario struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type"`
	MCVersion string `yaml:"mc_version"`
	JavaMajor int    `yaml:"java_major"`
	// Bots need a protocol match, zero skips the load phase
	BotsSupported bool `yaml:"bots_supported"`
}

// One runtime image under test
type ContenderCfg struct {
	Name  string            `yaml:"name"`
	Kind  string            `yaml:"kind"`
	Image string            `yaml:"image"`
	// Extra env for A/B testing flag sets on one contender
	Env map[string]string `yaml:"env"`
}

// Returns standard matrix of discopanel versus itzg
func DefaultConfig() *Config {
	return &Config{
		Iterations:   3,
		Bots:         30,
		LoadDuration: 5 * time.Minute,
		RampSkip:     30 * time.Second,
		MemoryMB:     4096,
		HeapMB:       3072,
		Seed:         "discobench",
		ViewDistance: 10,
		SimDistance:  10,
		ReadyTimeout: 10 * time.Minute,
		StopTimeout:  3 * time.Minute,
		WalkSpeed:    4.0,
		WalkRadius:   128,
		Scenarios: []Scenario{
			{Name: "vanilla-1.21.1", Type: TypeVanilla, MCVersion: "1.21.1", JavaMajor: 21, BotsSupported: true},
			{Name: "paper-1.21.1", Type: TypePaper, MCVersion: "1.21.1", JavaMajor: 21, BotsSupported: true},
		},
		Contenders: []ContenderCfg{
			{Name: "discopanel", Kind: KindDiscoPanel, Image: "ghcr.io/discohaus/discoruntime:java21"},
			{Name: "itzg", Kind: KindItzg, Image: "itzg/minecraft-server:java21"},
		},
	}
}

// Reads YAML config, absent path returns defaults
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.Iterations < 1 {
		return fmt.Errorf("iterations must be at least 1")
	}
	if len(c.Scenarios) == 0 || len(c.Contenders) == 0 {
		return fmt.Errorf("config needs at least one scenario and one contender")
	}
	for _, s := range c.Scenarios {
		if s.Type != TypeVanilla && s.Type != TypePaper {
			return fmt.Errorf("scenario %s has unknown type %q", s.Name, s.Type)
		}
	}
	for _, ct := range c.Contenders {
		if ct.Kind != KindDiscoPanel && ct.Kind != KindItzg {
			return fmt.Errorf("contender %s has unknown kind %q", ct.Name, ct.Kind)
		}
	}
	return nil
}
