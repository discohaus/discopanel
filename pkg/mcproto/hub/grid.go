// Package hub models the static lobby world
package hub

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Grid contract version this panel understands
const GridVersion = 1

// One filled cuboid of a single block
type Fill struct {
	X1    int    `json:"x1"`
	Y1    int    `json:"y1"`
	Z1    int    `json:"z1"`
	X2    int    `json:"x2"`
	Y2    int    `json:"y2"`
	Z2    int    `json:"z2"`
	Block string `json:"block"`
}

// One sign with its text lines
type Sign struct {
	X      int       `json:"x"`
	Y      int       `json:"y"`
	Z      int       `json:"z"`
	Facing string    `json:"facing"`
	Wall   bool      `json:"wall"`
	Lines  [4]string `json:"lines"`
}

// Static hub world description from the lobby module
type Grid struct {
	Version  int     `json:"version"`
	SpawnX   float64 `json:"spawn_x"`
	SpawnY   float64 `json:"spawn_y"`
	SpawnZ   float64 `json:"spawn_z"`
	SpawnYaw float32 `json:"spawn_yaw"`
	MinY     int     `json:"min_y"`
	Fills    []Fill  `json:"fills"`
	Signs    []Sign  `json:"signs"`

	blocks map[[3]int]string
	min    [3]int
	max    [3]int
}

// Caps accepted fill volume against hostile files
const maxGridBlocks = 4 << 20

// Parses and rasterizes one grid file
func Parse(data []byte) (*Grid, error) {
	g := &Grid{}
	if err := json.Unmarshal(data, g); err != nil {
		return nil, fmt.Errorf("grid unreadable: %w", err)
	}
	if g.Version != GridVersion {
		return nil, fmt.Errorf("grid version %d unsupported", g.Version)
	}
	if err := g.rasterize(); err != nil {
		return nil, err
	}
	return g, nil
}

// Applies fills in order onto the block map
func (g *Grid) rasterize() error {
	g.blocks = make(map[[3]int]string)
	total := 0
	first := true
	for _, f := range g.Fills {
		x1, x2 := ordered(f.X1, f.X2)
		y1, y2 := ordered(f.Y1, f.Y2)
		z1, z2 := ordered(f.Z1, f.Z2)
		volume := (x2 - x1 + 1) * (y2 - y1 + 1) * (z2 - z1 + 1)
		total += volume
		if total > maxGridBlocks {
			return fmt.Errorf("grid too large past %d blocks", maxGridBlocks)
		}
		block := NormalizeBlock(f.Block)
		base, _ := SplitState(block)
		for x := x1; x <= x2; x++ {
			for y := y1; y <= y2; y++ {
				for z := z1; z <= z2; z++ {
					pos := [3]int{x, y, z}
					if base == "air" || base == "cave_air" {
						delete(g.blocks, pos)
					} else {
						g.blocks[pos] = block
					}
					if first {
						g.min, g.max = pos, pos
						first = false
					} else {
						g.grow(pos)
					}
				}
			}
		}
	}
	return nil
}

func (g *Grid) grow(pos [3]int) {
	for i := range 3 {
		if pos[i] < g.min[i] {
			g.min[i] = pos[i]
		}
		if pos[i] > g.max[i] {
			g.max[i] = pos[i]
		}
	}
}

func ordered(a, b int) (int, int) {
	if a > b {
		return b, a
	}
	return a, b
}

// Block name without namespace, state suffix kept
func NormalizeBlock(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "minecraft:")
}

// Splits a block name off its state suffix
func SplitState(name string) (base, state string) {
	if i := strings.IndexByte(name, '['); i >= 0 {
		state = strings.TrimSuffix(name[i+1:], "]")
		return name[:i], state
	}
	return name, ""
}

// Reads one property out of a state suffix
func StateProp(state, key string) string {
	for _, pair := range strings.Split(state, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Block at one position, empty means air
func (g *Grid) BlockAt(x, y, z int) string {
	return g.blocks[[3]int{x, y, z}]
}

// Touched bounds of the rasterized world
func (g *Grid) Bounds() (min, max [3]int) {
	return g.min, g.max
}

// Every solid block for bakers to walk
func (g *Grid) Blocks() map[[3]int]string {
	return g.blocks
}

// Chunk coordinates covering the touched bounds
func (g *Grid) ChunkRange() (cx1, cz1, cx2, cz2 int) {
	return floorDiv(g.min[0], 16), floorDiv(g.min[2], 16),
		floorDiv(g.max[0], 16), floorDiv(g.max[2], 16)
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
