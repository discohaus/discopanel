package family

import (
	"sort"
	"strings"

	"github.com/discohaus/discopanel/pkg/mcproto/mcdata"
)

// Columns run oldest legacy group to newest
// 754 755 757 759 761 762 763 764 765
func modernCol(protocol int32) int {
	switch protocol {
	case 754:
		return 0
	case 755, 756:
		return 1
	case 757, 758:
		return 2
	case 759, 760:
		return 3
	case 761:
		return 4
	case 762:
		return 5
	case 763:
		return 6
	case 764:
		return 7
	case 765:
		return 8
	default:
		return -1
	}
}

// Palette stand ins for blocks born later
var legacySubstitutes = []struct {
	base  string
	sub   string
	floor int32
}{
	{"pearlescent_froglight", "sea_lantern", 759},
	{"amethyst_block", "purpur_block", 755},
	{"amethyst_cluster", "soul_torch", 755},
	{"smooth_basalt", "polished_blackstone", 755},
	{"waxed_oxidized_cut_copper", "warped_planks", 755},
}

// Single state ids for the legacy palette
// Values come straight from vanilla block reports
var modernSingles = map[string][9]int32{
	"air":                          {0, 0, 0, 0, 0, 0, 0, 0, 0},
	"bedrock":                      {33, 33, 33, 74, 76, 79, 79, 79, 79},
	"dirt":                         {10, 10, 10, 10, 10, 10, 10, 10, 10},
	"grass_block":                  {9, 9, 9, 9, 9, 9, 9, 9, 9},
	"sea_lantern":                  {7866, 8112, 8112, 8603, 10247, 10579, 10583, 10724, 10724},
	"iron_block":                   {1428, 1484, 1484, 1682, 2040, 2088, 2092, 2092, 2092},
	"polished_blackstone":          {16258, 16504, 16504, 17459, 19243, 19712, 19730, 19871, 19871},
	"chiseled_polished_blackstone": {16261, 16507, 16507, 17462, 19246, 19715, 19733, 19874, 19874},
	"purpur_block":                 {9138, 9384, 9384, 10015, 11799, 12265, 12269, 12410, 12410},
	"beacon":                       {5660, 5862, 5862, 6248, 7688, 7914, 7918, 7918, 7918},
	"polished_diorite":             {5, 5, 5, 5, 5, 5, 5, 5, 5},
	"polished_andesite":            {7, 7, 7, 7, 7, 7, 7, 7, 7},
	"smooth_basalt":                {0, 20336, 20336, 21431, 23215, 23700, 24102, 24243, 26557},
	"polished_blackstone_bricks":   {16259, 16505, 16505, 17460, 19244, 19713, 19731, 19872, 19872},
	"amethyst_block":               {0, 17664, 17664, 18619, 20403, 20872, 20890, 21031, 21031},
	"waxed_oxidized_cut_copper":    {0, 18172, 18172, 19266, 21050, 21519, 21921, 22062, 23304},
	"soul_lantern":                 {14897, 15143, 15143, 16098, 17882, 18351, 18369, 18510, 18510},
	"soul_torch":                   {4008, 4077, 4077, 4317, 5693, 5855, 5859, 5859, 5858},
	"warped_planks":                {15054, 15300, 15300, 16255, 18039, 18508, 18526, 18667, 18667},
	// Freestanding post state, the hub uses no others
	"polished_blackstone_brick_wall": {16351, 16597, 16597, 17552, 19336, 19805, 19823, 19964, 19964},
	// Upward facing state, the hub uses no others
	"amethyst_cluster": {0, 17675, 17675, 18630, 20414, 20883, 20901, 21042, 21042},
}

// First stained glass id, colors run consecutive
var modernGlassBase = [9]int32{4095, 4164, 4164, 4404, 5780, 5942, 5946, 5946, 5945}

// Vanilla color order shared by glass and wool
var vanillaColors = []string{
	"white", "orange", "magenta", "light_blue", "yellow", "lime",
	"pink", "gray", "light_gray", "cyan", "purple", "blue",
	"brown", "green", "red", "black",
}

// Multi state bases for the hub palette
var modernBases = map[string][9]int32{
	"nether_portal":         {4014, 4083, 4083, 4323, 5699, 5861, 5865, 5865, 5864},
	"quartz_pillar":         {6744, 6946, 6946, 7357, 8841, 9093, 9097, 9237, 9237},
	"pearlescent_froglight": {0, 0, 0, 21443, 23227, 23712, 24114, 24255, 26569},
	"end_rod":               {9062, 9308, 9308, 9939, 11723, 12189, 12193, 12334, 12334},
	"warped_wall_sign":      {15735, 15981, 15981, 16936, 18720, 19189, 19207, 19348, 19348},
}

// Offset within a multi state block for one state
func modernStateOffset(base, state string) int32 {
	switch base {
	case "nether_portal":
		if StateProp(state, "axis") == "z" {
			return 1
		}
		return 0
	case "warped_wall_sign":
		switch StateProp(state, "facing") {
		case "south":
			return 3
		case "west":
			return 5
		case "east":
			return 7
		default:
			return 1
		}
	case "quartz_pillar", "pearlescent_froglight":
		switch StateProp(state, "axis") {
		case "x":
			return 0
		case "z":
			return 2
		default:
			return 1
		}
	case "end_rod":
		switch StateProp(state, "facing") {
		case "north":
			return 0
		case "east":
			return 1
		case "south":
			return 2
		case "west":
			return 3
		case "down":
			return 5
		default:
			return 4
		}
	default:
		return 0
	}
}

// Small positive int to text without imports
func itoa(v int32) string {
	if v == 0 {
		return "0"
	}
	digits := []byte{}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// Default state suffix for multi state palette blocks
var defaultStates = map[string]string{
	"nether_portal":         "axis=x",
	"quartz_pillar":         "axis=y",
	"pearlescent_froglight": "axis=y",
	"end_rod":               "facing=up",
	"warped_wall_sign":      "facing=north",
}

// Palette spelling with the state suffix filled and trimmed
func canonicalState(block string) string {
	base, state := SplitState(NormalizeBlock(block))
	if state == "" {
		state = defaultStates[base]
	}
	if state == "" {
		return base
	}
	var pairs []string
	for _, pair := range strings.Split(state, ",") {
		k, v, _ := strings.Cut(pair, "=")
		pairs = append(pairs, strings.TrimSpace(k)+"="+strings.TrimSpace(v))
	}
	sort.Strings(pairs)
	return base + "[" + strings.Join(pairs, ",") + "]"
}

// State id for one grid block name
func ModernStateID(protocol int32, block string) (int32, bool) {
	if protocol >= dataFloor {
		p := mcdata.Lookup(protocol)
		if p == nil {
			return 0, false
		}
		return p.StateID(canonicalState(block))
	}
	col := modernCol(protocol)
	if col < 0 {
		return 0, false
	}
	base, state := SplitState(block)

	for _, s := range legacySubstitutes {
		if s.base == base && protocol < s.floor {
			base, state = s.sub, ""
			break
		}
	}

	if ids, ok := modernSingles[base]; ok {
		return ids[col], true
	}
	if bases, ok := modernBases[base]; ok {
		return bases[col] + modernStateOffset(base, state), true
	}
	if color, ok := strings.CutSuffix(base, "_stained_glass"); ok {
		for i, name := range vanillaColors {
			if name == color {
				return modernGlassBase[col] + int32(i), true
			}
		}
	}
	return 0, false
}

// Every block state spelling the hub grid can place
func PaletteStates() []string {
	var out []string
	for name := range modernSingles {
		out = append(out, name)
	}
	for _, axis := range []string{"x", "z"} {
		out = append(out, "nether_portal[axis="+axis+"]")
	}
	for _, axis := range []string{"x", "y", "z"} {
		out = append(out, "quartz_pillar[axis="+axis+"]", "pearlescent_froglight[axis="+axis+"]")
	}
	for _, facing := range []string{"north", "east", "south", "west", "up", "down"} {
		out = append(out, "end_rod[facing="+facing+"]")
	}
	for _, facing := range []string{"north", "south", "west", "east"} {
		out = append(out, "warped_wall_sign[facing="+facing+"]")
	}
	for _, color := range vanillaColors {
		out = append(out, color+"_stained_glass")
	}
	sort.Strings(out)
	return out
}
