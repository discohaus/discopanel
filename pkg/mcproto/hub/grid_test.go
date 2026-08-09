package hub

import (
	"testing"
)

// Small grid parses and rasterizes correctly
func TestGridParse(t *testing.T) {
	data := []byte(`{
		"version": 1,
		"spawn_x": 0.5, "spawn_y": -60, "spawn_z": 0.5, "spawn_yaw": 90,
		"min_y": -64,
		"fills": [
			{"x1": -2, "y1": -61, "z1": -2, "x2": 2, "y2": -61, "z2": 2, "block": "minecraft:grass_block"},
			{"x1": 0, "y1": -61, "z1": 0, "x2": 0, "y2": -61, "z2": 0, "block": "minecraft:sea_lantern"},
			{"x1": 1, "y1": -61, "z1": 1, "x2": 1, "y2": -61, "z2": 1, "block": "air"}
		],
		"signs": [
			{"x": 0, "y": -59, "z": 2, "facing": "north", "wall": true, "lines": ["a", "b", "", ""]}
		]
	}`)

	grid, err := Parse(data)
	if err != nil {
		t.Fatalf("parse failed %v", err)
	}
	if grid.BlockAt(-2, -61, -2) != "grass_block" {
		t.Fatal("corner block missing")
	}
	if grid.BlockAt(0, -61, 0) != "sea_lantern" {
		t.Fatal("later fill must override")
	}
	if grid.BlockAt(1, -61, 1) != "" {
		t.Fatal("air fill must clear")
	}
	if grid.BlockAt(9, 9, 9) != "" {
		t.Fatal("untouched must be air")
	}

	min, max := grid.Bounds()
	if min != [3]int{-2, -61, -2} || max != [3]int{2, -61, 2} {
		t.Fatalf("bounds = %v %v", min, max)
	}
	cx1, cz1, cx2, cz2 := grid.ChunkRange()
	if cx1 != -1 || cz1 != -1 || cx2 != 0 || cz2 != 0 {
		t.Fatalf("chunk range = %d %d %d %d", cx1, cz1, cx2, cz2)
	}
	if len(grid.Signs) != 1 || grid.Signs[0].Lines[0] != "a" {
		t.Fatalf("signs wrong: %+v", grid.Signs)
	}
}

// Wrong versions and huge fills get refused
func TestGridRejects(t *testing.T) {
	if _, err := Parse([]byte(`{"version": 99}`)); err == nil {
		t.Fatal("wrong version must fail")
	}
	if _, err := Parse([]byte(`{"version": 1,
		"fills": [{"x1": -600, "y1": -64, "z1": -600, "x2": 600, "y2": 320, "z2": 600, "block": "stone"}]}`)); err == nil {
		t.Fatal("oversized grid must fail")
	}
}

// Block names keep states but lose namespaces
func TestNormalizeBlock(t *testing.T) {
	if NormalizeBlock("minecraft:oak_sign[rotation=4]") != "oak_sign[rotation=4]" {
		t.Fatal("normalize failed")
	}
	if NormalizeBlock(" grass_block ") != "grass_block" {
		t.Fatal("trim failed")
	}
	base, state := SplitState("quartz_pillar[axis=x]")
	if base != "quartz_pillar" || state != "axis=x" {
		t.Fatalf("split = %q %q", base, state)
	}
	if StateProp("axis=x,facing=north", "facing") != "north" {
		t.Fatal("state prop failed")
	}
	if StateProp("axis=x", "missing") != "" {
		t.Fatal("missing prop must be empty")
	}
}
