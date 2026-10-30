// Package memlimit sizes the Go heap ceiling for the panel
package memlimit

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/pkg/config"
)

// Share of a cgroup limit handed to the Go runtime
const cgroupRatio = 0.9

// Cgroup v1 reports no limit as a huge int
const unlimitedFloor = int64(1) << 60

// Where the effective limit came from
const (
	SourceConfig = "config"
	SourceEnv    = "GOMEMLIMIT"
	SourceCgroup = "cgroup"
	SourceNone   = "none"
)

// Heap ceiling for the runtime, zero bytes means off
type Limit struct {
	Bytes  int64
	Source string
}

// Paths the resolver reads, swapped in tests
type Paths struct {
	CgroupRoot string
	ProcCgroup string
	Getenv     func(string) string
}

// Host defaults for the resolver paths
func DefaultPaths() Paths {
	return Paths{CgroupRoot: "/sys/fs/cgroup", ProcCgroup: "/proc/self/cgroup", Getenv: os.Getenv}
}

// Picks the limit from config, then GOMEMLIMIT, then the cgroup
func Resolve(setting string, paths Paths) (Limit, error) {
	mode, bytes, err := config.ParseMemoryLimit(setting)
	if err != nil {
		return Limit{}, err
	}
	switch mode {
	case config.MemoryLimitOff:
		return Limit{Source: SourceConfig}, nil
	case config.MemoryLimitFixed:
		return Limit{Bytes: bytes, Source: SourceConfig}, nil
	}
	if paths.Getenv(SourceEnv) != "" {
		return Limit{Bytes: runtimeLimit(), Source: SourceEnv}, nil
	}
	if limit, ok := cgroupLimit(paths.CgroupRoot, paths.ProcCgroup); ok {
		return Limit{Bytes: int64(float64(limit) * cgroupRatio), Source: SourceCgroup}, nil
	}
	return Limit{Source: SourceNone}, nil
}

// Reads the limit the runtime holds, off becomes zero
func runtimeLimit() int64 {
	current := debug.SetMemoryLimit(-1)
	if current == math.MaxInt64 {
		return 0
	}
	return current
}

// Hands the limit to the runtime, none and off skip
func Apply(limit Limit) {
	if limit.Bytes > 0 && limit.Source != SourceEnv {
		debug.SetMemoryLimit(limit.Bytes)
	}
}

// Smallest memory limit across this process's cgroup ancestors
func cgroupLimit(root, procCgroup string) (int64, bool) {
	file, err := os.Open(procCgroup)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	best := int64(math.MaxInt64)
	found := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		var base, name string
		switch {
		case parts[0] == "0" && parts[1] == "":
			base, name = root, "memory.max"
		case hasController(parts[1], "memory"):
			base, name = filepath.Join(root, "memory"), "memory.limit_in_bytes"
		default:
			continue
		}
		for _, dir := range ancestors(parts[2]) {
			limit, ok := readLimit(filepath.Join(base, dir, name))
			if ok && limit < best {
				best, found = limit, true
			}
		}
	}
	return best, found
}

// True when a v1 controller list names the controller
func hasController(list, want string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == want {
			return true
		}
	}
	return false
}

// Cgroup path and every parent up to the root
func ancestors(path string) []string {
	path = filepath.Clean("/" + path)
	var out []string
	for {
		out = append(out, path)
		if path == "/" {
			return out
		}
		path = filepath.Dir(path)
	}
}

// Parses one limit file, unlimited values report false
func readLimit(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(data))
	if text == "max" || text == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 || value >= unlimitedFloor {
		return 0, false
	}
	return value, true
}
