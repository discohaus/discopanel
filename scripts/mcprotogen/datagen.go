package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/pkg/mcproto/family"
)

// Vanilla report tables the hub needs
type reports struct {
	packets    map[string]map[string]map[string]int32
	registries map[string]map[string]int32
	blocks     map[string]blockReport
}

// One block with its state table
type blockReport struct {
	Properties map[string][]string `json:"properties"`
	States     []struct {
		ID         int32             `json:"id"`
		Default    bool              `json:"default"`
		Properties map[string]string `json:"properties"`
	} `json:"states"`
}

var javaVersionPattern = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

// Major release of the java binary on the path
func javaMajor(javaBin string) (int, error) {
	out, err := exec.Command(javaBin, "-version").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("%s -version: %w", javaBin, err)
	}
	m := javaVersionPattern.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("%s -version printed no version", javaBin)
	}
	major, _ := strconv.Atoi(string(m[1]))
	if major == 1 && len(m[2]) > 0 {
		major, _ = strconv.Atoi(string(m[2]))
	}
	return major, nil
}

// Runs the vanilla data generator unless its reports exist
func runDatagen(javaBin, jar, outDir string) error {
	if _, err := os.Stat(filepath.Join(outDir, "reports", "registries.json")); err == nil {
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command(javaBin, "-DbundlerMainClass=net.minecraft.data.Main", "-jar", jar, "--reports", "--output", outDir)
	cmd.Dir = outDir
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("data generator failed: %w\n%s", err, tail(log.String(), 20))
	}
	if _, err := os.Stat(filepath.Join(outDir, "reports", "registries.json")); err != nil {
		return fmt.Errorf("data generator wrote no registries.json")
	}
	return nil
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimSpace(s), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

// Loads the three reports the hub tables derive from
func parseReports(dir string) (*reports, error) {
	r := &reports{}
	var packets map[string]map[string]map[string]struct {
		ID int32 `json:"protocol_id"`
	}
	// Packet reports start with 1.21.2, older eras keep hand tables
	if err := readJSON(filepath.Join(dir, "reports", "packets.json"), &packets); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	r.packets = map[string]map[string]map[string]int32{}
	for state, dirs := range packets {
		r.packets[state] = map[string]map[string]int32{}
		for dir, names := range dirs {
			r.packets[state][dir] = map[string]int32{}
			for name, e := range names {
				r.packets[state][dir][name] = e.ID
			}
		}
	}
	var regs map[string]struct {
		Entries map[string]struct {
			ID int32 `json:"protocol_id"`
		} `json:"entries"`
	}
	if err := readJSON(filepath.Join(dir, "reports", "registries.json"), &regs); err != nil {
		return nil, err
	}
	r.registries = map[string]map[string]int32{}
	for name, reg := range regs {
		r.registries[name] = map[string]int32{}
		for entry, e := range reg.Entries {
			r.registries[name][entry] = e.ID
		}
	}
	if err := readJSON(filepath.Join(dir, "reports", "blocks.json"), &r.blocks); err != nil {
		return nil, err
	}
	return r, nil
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Registry entry id or an error naming what is missing
func (r *reports) registryID(registry, entry string) (int32, error) {
	id, ok := r.registries["minecraft:"+registry]["minecraft:"+entry]
	if !ok {
		return 0, fmt.Errorf("registry %s has no entry %s", registry, entry)
	}
	return id, nil
}

// State id for one hub palette spelling
// Unspecified properties take the block default
func (r *reports) stateID(spelling string) (int32, error) {
	base, state := family.SplitState(spelling)
	block, ok := r.blocks["minecraft:"+base]
	if !ok {
		return 0, fmt.Errorf("block %s is not in blocks.json", base)
	}
	want := map[string]string{}
	if state != "" {
		for _, pair := range strings.Split(state, ",") {
			k, v, ok := strings.Cut(pair, "=")
			if !ok {
				return 0, fmt.Errorf("block %s has a malformed state %q", base, state)
			}
			want[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	for k := range want {
		if _, ok := block.Properties[k]; !ok {
			return 0, fmt.Errorf("block %s has no property %s", base, k)
		}
	}
	var defaults map[string]string
	for _, s := range block.States {
		if s.Default {
			defaults = s.Properties
		}
	}
	for _, s := range block.States {
		match := true
		for k, v := range s.Properties {
			if w, ok := want[k]; ok {
				match = match && w == v
			} else {
				match = match && defaults[k] == v
			}
		}
		if match {
			return s.ID, nil
		}
	}
	return 0, fmt.Errorf("block %s has no state %q", base, state)
}
