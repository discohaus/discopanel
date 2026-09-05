package runtimespec

import (
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Fills heap defaults then validates the memory trio
func NormalizeServerMemory(server *v1.Server) error {
	if server.Memory < 1024 {
		return fmt.Errorf("server memory must be at least 1024 MB")
	}
	defInit, defMax := DefaultHeapForMemory(int(server.Memory))
	if server.MemoryMax <= 0 {
		server.MemoryMax = int32(defMax)
	}
	if server.MemoryMin <= 0 {
		server.MemoryMin = min(int32(defInit), server.MemoryMax)
	}
	if server.MemoryMin > server.MemoryMax {
		return fmt.Errorf("initial heap %d MB exceeds max heap %d MB", server.MemoryMin, server.MemoryMax)
	}
	if server.Memory-server.MemoryMax < 256 {
		return fmt.Errorf("max heap %d MB must leave at least 256 MB of the %d MB server memory for JVM overhead", server.MemoryMax, server.Memory)
	}
	return nil
}

// Parses memory strings like 4096M or 12G to MB
func ParseMemoryMB(s string) int {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	mult := 1
	switch {
	case strings.HasSuffix(s, "G"):
		mult = 1024
		s = strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		s = strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		s = strings.TrimSuffix(s, "K")
		if v, err := strconv.Atoi(s); err == nil {
			return v / 1024
		}
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v * mult
}

// Returns default JVM heap sizing for a container limit
func DefaultHeapForMemory(memoryMB int) (initMB, maxMB int) {
	return memoryMB / 2, memoryMB * 3 / 4
}

// Mirrors server heap sizing into read-only properties
func SyncPropertiesMemory(c *v1.ServerProperties, server *v1.Server) {
	initMB, maxMB := int(server.MemoryMin), int(server.MemoryMax)
	defInit, defMax := DefaultHeapForMemory(int(server.Memory))
	if initMB <= 0 {
		initMB = defInit
	}
	if maxMB <= 0 {
		maxMB = defMax
	}
	initStr := fmt.Sprintf("%dM", initMB)
	maxStr := fmt.Sprintf("%dM", maxMB)
	c.InitMemory = &initStr
	c.MaxMemory = &maxStr
}
