package family

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Ground margin past the gate ring
const groundMargin = 17

// Heights stack on the superflat layers
const (
	bedrockY = -64 // probe layer
	pyramidY = -63 // beacon pyramids under the gate thresholds
	underY   = -62 // beacons and the dance floor lantern bed
	floorY   = -61 // paved plaza surface
	standY   = -60 // player feet
	lintelY  = -57 // gate lintels and plaque signs
	finialY  = -56 // end rods on the gate pillars
	ballY    = -47 // mirror ball centre
)

// Spawn contract every hub session shares
const (
	SpawnX   = 0.5
	SpawnY   = float64(standY)
	SpawnZ   = 0.5
	SpawnYaw = float32(180)
)

// One panel server the hub offers
type Target struct {
	ID       string
	Name     string
	Hostname string
	Port     int
	Addr     string
	Version  string
	Protocol int32
	Running  bool
	Waking   bool
	Wakeable bool
	Online   int
}

// Sorts targets into stable gate order
func SortTargets(targets []Target) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Name != targets[j].Name {
			return targets[i].Name < targets[j].Name
		}
		return targets[i].ID < targets[j].ID
	})
}

// One gate mood driving glass, planes, and words
type TargetState int

const (
	StateVersionGap TargetState = iota
	StateRunning
	StateWaking
	StateAsleep
	StateOffline
)

// Classifies one target against a client protocol
func StateOf(t *Target, protocol int32) TargetState {
	switch {
	case t.Protocol != 0 && protocol != 0 && t.Protocol != protocol:
		return StateVersionGap
	case t.Running:
		return StateRunning
	case t.Waking:
		return StateWaking
	case t.Wakeable:
		return StateAsleep
	default:
		return StateOffline
	}
}

// Beam glass under the gate for one state
func stateGlass(st TargetState) string {
	switch st {
	case StateVersionGap:
		return "minecraft:orange_stained_glass"
	case StateRunning:
		return "minecraft:light_blue_stained_glass"
	case StateWaking, StateAsleep:
		return "minecraft:purple_stained_glass"
	default:
		return "minecraft:gray_stained_glass"
	}
}

// Portal plane filling the gate for one state
func statePlane(st TargetState, axis string) string {
	switch st {
	case StateVersionGap:
		return "minecraft:orange_stained_glass"
	case StateRunning:
		return "minecraft:nether_portal[axis=" + axis + "]"
	case StateWaking:
		return "minecraft:magenta_stained_glass"
	case StateAsleep:
		return "minecraft:purple_stained_glass"
	default:
		return "minecraft:gray_stained_glass"
	}
}

// Plaque lines for one target state
func SignLines(t *Target, protocol int32) [4]string {
	name := trimSign(t.Name)
	switch StateOf(t, protocol) {
	case StateVersionGap:
		return [4]string{name, "needs " + t.Version, "update your game", ""}
	case StateRunning:
		return [4]string{name, fmt.Sprintf("%d playing", t.Online), "walk through", t.Version}
	case StateWaking:
		return [4]string{name, "starting up", "wait here", t.Version}
	case StateAsleep:
		return [4]string{name, "asleep", "walk in to wake", t.Version}
	default:
		return [4]string{name, "offline", "ask the owner", t.Version}
	}
}

// Chat status fragment for one target state
func ChatStatus(t *Target, protocol int32) string {
	switch StateOf(t, protocol) {
	case StateVersionGap:
		return "§6needs " + t.Version
	case StateRunning:
		return fmt.Sprintf("§a%d playing", t.Online)
	case StateWaking:
		return "§dstarting up"
	case StateAsleep:
		return "§5asleep"
	default:
		return "§8offline"
	}
}

// Signs hold about fifteen characters per line
func trimSign(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > 15 {
		return string(r[:14]) + "…"
	}
	return s
}

// One free-standing portal gate on the ring
type gate struct {
	X, Z   int    // threshold centre on the wall plane
	UX, UZ int    // unit vector along the lintel
	NX, NZ int    // unit vector toward the plaza centre
	Facing string // plaque sign facing
}

// Clockwise ring starting north-west, slot order follows it
func hubGates() []gate {
	return []gate{
		{X: -8, Z: -15, UX: 1, UZ: 0, NX: 0, NZ: 1, Facing: "south"},
		{X: 8, Z: -15, UX: 1, UZ: 0, NX: 0, NZ: 1, Facing: "south"},
		{X: 15, Z: -8, UX: 0, UZ: 1, NX: -1, NZ: 0, Facing: "west"},
		{X: 15, Z: 8, UX: 0, UZ: 1, NX: -1, NZ: 0, Facing: "west"},
		{X: 8, Z: 15, UX: 1, UZ: 0, NX: 0, NZ: -1, Facing: "north"},
		{X: -8, Z: 15, UX: 1, UZ: 0, NX: 0, NZ: -1, Facing: "north"},
		{X: -15, Z: 8, UX: 0, UZ: 1, NX: 1, NZ: 0, Facing: "east"},
		{X: -15, Z: -8, UX: 0, UZ: 1, NX: 1, NZ: 0, Facing: "east"},
	}
}

// Slot spread keeps small fleets facing each other
var slotSpread = [...]int{0, 4, 2, 6, 1, 5, 3, 7}

// Plaza half size fitting one fleet
func ringHalf(n int) int {
	perWall := (n + 3) / 4
	s := 4*(perWall-1) + 9
	if s < 15 {
		s = 15
	}
	return s
}

// Gate ring layout sized for one fleet
func layoutGates(n int) []gate {
	if n <= len(slotSpread) {
		return hubGates()
	}
	return grownGates(n)
}

// Clockwise walls packed for fleets past eight
func grownGates(n int) []gate {
	s := ringHalf(n)
	counts := [4]int{}
	for i := range counts {
		counts[i] = n / 4
		if i < n%4 {
			counts[i]++
		}
	}
	out := make([]gate, 0, n)
	for w, c := range counts {
		for k := 0; k < c; k++ {
			off := (2*k - c + 1) * 4
			switch w {
			case 0:
				out = append(out, gate{X: off, Z: -s, UX: 1, UZ: 0, NX: 0, NZ: 1, Facing: "south"})
			case 1:
				out = append(out, gate{X: s, Z: off, UX: 0, UZ: 1, NX: -1, NZ: 0, Facing: "west"})
			case 2:
				out = append(out, gate{X: -off, Z: s, UX: 1, UZ: 0, NX: 0, NZ: -1, Facing: "north"})
			case 3:
				out = append(out, gate{X: -s, Z: -off, UX: 0, UZ: 1, NX: 1, NZ: 0, Facing: "east"})
			}
		}
	}
	return out
}

// Gate slot holding one sorted target index
func SlotForTarget(i, targets int) int {
	if i < 0 || i >= targets {
		return -1
	}
	if targets > len(slotSpread) {
		return i
	}
	return slotSpread[i]
}

// Sorted target index standing in one slot
func TargetForSlot(slot, targets int) int {
	if targets > len(slotSpread) {
		if slot < 0 || slot >= targets {
			return -1
		}
		return slot
	}
	for i := 0; i < targets; i++ {
		if slotSpread[i] == slot {
			return i
		}
	}
	return -1
}

// Feet box covering one gate and its doorstep
type GateBox struct {
	X1, Y1, Z1 float64
	X2, Y2, Z2 float64
}

// Reports whether feet stand inside the box
func (b GateBox) Contains(x, y, z float64) bool {
	return x >= b.X1 && x < b.X2 && y >= b.Y1 && y < b.Y2 && z >= b.Z1 && z < b.Z2
}

// Trigger boxes in gate slot order for one fleet
func GateBoxes(targets int) []GateBox {
	gates := layoutGates(targets)
	out := make([]GateBox, len(gates))
	for i, g := range gates {
		x1 := float64(min(g.X-g.UX, g.X+g.UX))
		z1 := float64(min(g.Z-g.UZ, g.Z+g.UZ))
		x2 := float64(max(g.X-g.UX, g.X+g.UX)) + 1
		z2 := float64(max(g.Z-g.UZ, g.Z+g.UZ)) + 1
		// Doorstep row reaches solid planes from outside
		switch {
		case g.NX > 0:
			x2++
		case g.NX < 0:
			x1--
		case g.NZ > 0:
			z2++
		case g.NZ < 0:
			z1--
		}
		out[i] = GateBox{X1: x1, Y1: standY, Z1: z1, X2: x2, Y2: standY + 2, Z2: z2}
	}
	return out
}

func fill(x1, y1, z1, x2, y2, z2 int, block string) Fill {
	return Fill{X1: x1, Y1: y1, Z1: z1, X2: x2, Y2: y2, Z2: z2, Block: block}
}

func set(x, y, z int, block string) Fill {
	return fill(x, y, z, x, y, z, block)
}

// Row fills covering the annulus rIn to rOut at one height
func ringFills(rIn, rOut float64, y int, block string) []Fill {
	var out []Fill
	zMax := int(rOut)
	for z := -zMax; z <= zMax; z++ {
		outer := rOut*rOut - float64(z*z)
		if outer <= 0 {
			continue
		}
		b := int(math.Sqrt(outer))
		var a int
		if inner := rIn*rIn - float64(z*z); inner > 0 {
			a = int(math.Ceil(math.Sqrt(inner)))
		}
		if a > b {
			continue
		}
		if a == 0 {
			out = append(out, fill(-b, y, z, b, y, z, block))
			continue
		}
		out = append(out, fill(-b, y, z, -a, y, z, block), fill(a, y, z, b, y, z, block))
	}
	return out
}

// Tile colors cycling across the dance floor
var discoTiles = [4]string{
	"minecraft:white_stained_glass",
	"minecraft:light_blue_stained_glass",
	"minecraft:magenta_stained_glass",
	"minecraft:purple_stained_glass",
}

// Glowing checkered dance floor over the lantern bed
func danceFloorFills() []Fill {
	const r = 4.7
	var out []Fill
	out = append(out, ringFills(0, r, underY, "minecraft:sea_lantern")...)
	for z := -4; z <= 4; z++ {
		for x := -4; x <= 4; x++ {
			if float64(x*x+z*z) > r*r {
				continue
			}
			out = append(out, set(x, floorY, z, discoTiles[(x&1)|(z&1)<<1]))
		}
	}
	return out
}

// Paved rings from the dance floor out to the rim
func plazaBandFills(s int) []Fill {
	sf := float64(s)
	bands := []struct {
		rIn, rOut float64
		block     string
	}{
		{4.7, 5.7, "minecraft:polished_diorite"},
		{5.7, 10.7, "minecraft:polished_blackstone"},
		{10.7, 11.7, "minecraft:polished_blackstone_bricks"},
		{11.7, 14.3, "minecraft:polished_andesite"},
		{14.3, sf + 1.7, "minecraft:polished_blackstone"},
		{sf + 1.7, sf + 4.7, "minecraft:smooth_basalt"},
		{sf + 4.7, sf + 5.7, "minecraft:polished_blackstone_bricks"},
	}
	var out []Fill
	for _, b := range bands {
		out = append(out, ringFills(b.rIn, b.rOut, floorY, b.block)...)
	}
	return out
}

// Checkered sphere floating over the dance floor
func mirrorBallFills() []Fill {
	var out []Fill
	for dx := -2; dx <= 2; dx++ {
		for dy := -2; dy <= 2; dy++ {
			for dz := -2; dz <= 2; dz++ {
				if dx*dx+dy*dy+dz*dz > 7 {
					continue
				}
				block := "minecraft:iron_block"
				if (dx+dy+dz)%2 == 0 {
					block = "minecraft:sea_lantern"
				}
				out = append(out, set(dx, ballY+dy, dz, block))
			}
		}
	}
	return append(out,
		set(0, ballY+3, 0, "minecraft:end_rod"),
		set(0, ballY-3, 0, "minecraft:end_rod[facing=down]"),
		set(3, ballY, 0, "minecraft:end_rod[facing=east]"),
		set(-3, ballY, 0, "minecraft:end_rod[facing=west]"),
		set(0, ballY, 3, "minecraft:end_rod[facing=south]"),
		set(0, ballY, -3, "minecraft:end_rod[facing=north]"),
	)
}

// One soul lantern post on a wall stem
func lampFills(x, z int) []Fill {
	return []Fill{
		set(x, floorY, z, "minecraft:chiseled_polished_blackstone"),
		fill(x, standY, z, x, standY+1, z, "minecraft:polished_blackstone_brick_wall"),
		set(x, standY+2, z, "minecraft:soul_lantern"),
	}
}

// Amethyst shrines mark the diagonals
func shrineFills(s int) []Fill {
	var out []Fill
	for _, sx := range []int{-1, 1} {
		for _, sz := range []int{-1, 1} {
			cx, cz := (s-2)*sx, (s-2)*sz
			out = append(out, set(9*sx, floorY, 9*sz, "minecraft:pearlescent_froglight"))
			out = append(out, lampFills(cx, cz)...)
			out = append(out,
				set(cx-sx, standY, cz, "minecraft:amethyst_block"),
				set(cx-sx, standY+1, cz, "minecraft:amethyst_cluster"),
				set(cx, standY, cz-sz, "minecraft:amethyst_block"),
				set(cx, standY+1, cz-sz, "minecraft:amethyst_cluster"),
			)
		}
	}
	return out
}

// Plaza sized to one gate ring half size
func plazaFills(s int) []Fill {
	out := danceFloorFills()
	// Grown plazas pave wall to wall under the rings
	if s > 15 {
		out = append(out, fill(-(s+6), floorY, -(s+6), s+6, floorY, s+6, "minecraft:smooth_basalt"))
	}
	out = append(out, plazaBandFills(s)...)
	out = append(out, shrineFills(s)...)
	edge := s + 3
	for _, p := range [][2]int{{0, -edge}, {0, edge}, {-edge, 0}, {edge, 0}} {
		out = append(out, lampFills(p[0], p[1])...)
	}
	return append(out, mirrorBallFills()...)
}

// One gate raised for one live target
func gateFills(g gate, t *Target, protocol int32) []Fill {
	st := StateOf(t, protocol)
	axis := "x"
	if g.UZ != 0 {
		axis = "z"
	}
	p1x, p1z := g.X-2*g.UX, g.Z-2*g.UZ
	p2x, p2z := g.X+2*g.UX, g.Z+2*g.UZ
	out := []Fill{
		set(p1x, floorY, p1z, "minecraft:chiseled_polished_blackstone"),
		set(p2x, floorY, p2z, "minecraft:chiseled_polished_blackstone"),
		fill(p1x, standY, p1z, p1x, standY+2, p1z, "minecraft:quartz_pillar"),
		fill(p2x, standY, p2z, p2x, standY+2, p2z, "minecraft:quartz_pillar"),
		set(p1x, finialY, p1z, "minecraft:end_rod"),
		set(p2x, finialY, p2z, "minecraft:end_rod"),
		fill(p1x, lintelY, p1z, p2x, lintelY, p2z, "minecraft:waxed_oxidized_cut_copper"),
		fill(g.X-1, pyramidY, g.Z-1, g.X+1, pyramidY, g.Z+1, "minecraft:iron_block"),
		set(g.X, underY, g.Z, "minecraft:beacon"),
		fill(g.X-g.UX, floorY, g.Z-g.UZ, g.X+g.UX, floorY, g.Z+g.UZ, stateGlass(st)),
		fill(g.X-g.UX, standY, g.Z-g.UZ, g.X+g.UX, standY+2, g.Z+g.UZ, statePlane(st, axis)),
		set(g.X+g.NX, lintelY, g.Z+g.NZ, fmt.Sprintf("minecraft:warped_wall_sign[facing=%s]", g.Facing)),
	}
	// Runway walks the plaza up to the doorstep
	for k := 1; k <= 4; k++ {
		block := "minecraft:polished_diorite"
		if k == 2 {
			block = "minecraft:pearlescent_froglight"
		}
		out = append(out,
			set(g.X+g.NX*k-g.UX, floorY, g.Z+g.NZ*k-g.UZ, block),
			set(g.X+g.NX*k, floorY, g.Z+g.NZ*k, block),
			set(g.X+g.NX*k+g.UX, floorY, g.Z+g.NZ*k+g.UZ, block),
		)
	}
	return out
}

// Superflat layers under and around the plaza
func groundFills(edge int) []Fill {
	return []Fill{
		fill(-edge, bedrockY, -edge, edge-1, bedrockY, edge-1, "minecraft:bedrock"),
		fill(-edge, pyramidY, -edge, edge-1, underY, edge-1, "minecraft:dirt"),
		fill(-edge, floorY, -edge, edge-1, floorY, edge-1, "minecraft:grass_block"),
	}
}

// Assembles the plaza for one fleet and protocol
// Targets must already ride sorted order
func BuildHub(targets []Target, protocol int32) *Grid {
	grid := &Grid{
		SpawnX:   SpawnX,
		SpawnY:   SpawnY,
		SpawnZ:   SpawnZ,
		SpawnYaw: SpawnYaw,
		MinY:     bedrockY,
	}
	s := ringHalf(len(targets))
	grid.Fills = append(groundFills(s+groundMargin), plazaFills(s)...)
	gates := layoutGates(len(targets))
	for i := range targets {
		slot := SlotForTarget(i, len(targets))
		if slot < 0 {
			break
		}
		g := gates[slot]
		grid.Fills = append(grid.Fills, gateFills(g, &targets[i], protocol)...)
		grid.Signs = append(grid.Signs, Sign{
			X:      g.X + g.NX,
			Y:      lintelY,
			Z:      g.Z + g.NZ,
			Facing: g.Facing,
			Wall:   true,
			Lines:  SignLines(&targets[i], protocol),
		})
	}
	grid.Rasterize()
	return grid
}

// One filled cuboid of a single block
type Fill struct {
	X1    int
	Y1    int
	Z1    int
	X2    int
	Y2    int
	Z2    int
	Block string
}

// One sign with its text lines
type Sign struct {
	X      int
	Y      int
	Z      int
	Facing string
	Wall   bool
	Lines  [4]string
}

// Static hub world the panel rasterizes and bakes
type Grid struct {
	SpawnX   float64
	SpawnY   float64
	SpawnZ   float64
	SpawnYaw float32
	MinY     int
	Fills    []Fill
	Signs    []Sign

	blocks map[[3]int]string
	min    [3]int
	max    [3]int
}

// Applies fills in order onto the block map
func (g *Grid) Rasterize() {
	g.blocks = make(map[[3]int]string)
	first := true
	for _, f := range g.Fills {
		x1, x2 := ordered(f.X1, f.X2)
		y1, y2 := ordered(f.Y1, f.Y2)
		z1, z2 := ordered(f.Z1, f.Z2)
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

// One block write for live updates
type BlockSet struct {
	X, Y, Z int
	Block   string
}

// Block and sign changes lifting old onto fresh
func DiffGrids(old, fresh *Grid) ([]BlockSet, []Sign) {
	if old == nil || fresh == nil {
		return nil, nil
	}
	var blocks []BlockSet
	for pos, block := range fresh.Blocks() {
		if old.BlockAt(pos[0], pos[1], pos[2]) != block {
			blocks = append(blocks, BlockSet{X: pos[0], Y: pos[1], Z: pos[2], Block: block})
		}
	}
	for pos := range old.Blocks() {
		if fresh.BlockAt(pos[0], pos[1], pos[2]) == "" {
			blocks = append(blocks, BlockSet{X: pos[0], Y: pos[1], Z: pos[2], Block: "air"})
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		a, b := blocks[i], blocks[j]
		if a.Y != b.Y {
			return a.Y < b.Y
		}
		if a.X != b.X {
			return a.X < b.X
		}
		return a.Z < b.Z
	})

	before := make(map[[3]int][4]string, len(old.Signs))
	for _, s := range old.Signs {
		before[[3]int{s.X, s.Y, s.Z}] = s.Lines
	}
	var signs []Sign
	for _, s := range fresh.Signs {
		if lines, ok := before[[3]int{s.X, s.Y, s.Z}]; ok && lines == s.Lines {
			continue
		}
		signs = append(signs, s)
	}
	return blocks, signs
}
