package family

import (
	"strings"

	"github.com/discohaus/discopanel/pkg/mcproto/hub"
)

// Columns run oldest group to newest
// 754 755 757 759 761 762 763 764 765 766 768 769 770 771 773 775 776
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
	case 766, 767:
		return 9
	case 768:
		return 10
	case 769:
		return 11
	case 770:
		return 12
	case 771, 772:
		return 13
	case 773, 774:
		return 14
	case 775:
		return 15
	case 776:
		return 16
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

// Single state ids for the hub palette
// Values come straight from vanilla block reports
var modernSingles = map[string][17]int32{
	"air":                           {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	"bedrock":                       {33, 33, 33, 74, 76, 79, 79, 79, 79, 79, 85, 85, 85, 85, 85, 85, 85},
	"dirt":                          {10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10},
	"grass_block":                   {9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9},
	"oak_planks":                    {15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15},
	"stone_bricks":                  {4495, 4564, 4564, 4868, 6308, 6534, 6538, 6538, 6537, 6537, 6767, 6770, 6780, 6780, 7553, 7754, 7754},
	"chiseled_stone_bricks":         {4498, 4567, 4567, 4871, 6311, 6537, 6541, 6541, 6540, 6540, 6770, 6773, 6783, 6783, 7556, 7757, 7757},
	"glowstone":                     {4013, 4082, 4082, 4322, 5698, 5860, 5864, 5864, 5863, 5863, 6029, 6032, 6042, 6042, 6815, 7016, 7016},
	"sea_lantern":                   {7866, 8112, 8112, 8603, 10247, 10579, 10583, 10724, 10724, 10724, 11059, 11603, 11613, 11613, 12690, 12892, 12892},
	"white_wool":                    {1384, 1440, 1440, 1638, 1996, 2043, 2047, 2047, 2047, 2047, 2090, 2090, 2093, 2093, 2093, 2293, 2293},
	"black_concrete":                {9457, 9703, 9703, 10334, 12118, 12587, 12602, 12743, 12743, 12743, 13212, 13756, 13766, 13766, 14843, 15045, 15045},
	"gold_block":                    {1427, 1483, 1483, 1681, 2039, 2087, 2091, 2091, 2091, 2091, 2134, 2134, 2137, 2137, 2137, 2338, 2338},
	"iron_block":                    {1428, 1484, 1484, 1682, 2040, 2088, 2092, 2092, 2092, 2092, 2135, 2135, 2138, 2138, 2138, 2339, 2339},
	"emerald_block":                 {5407, 5609, 5609, 5995, 7435, 7661, 7665, 7665, 7665, 7665, 7895, 8439, 8449, 8449, 9526, 9727, 9727},
	"diamond_block":                 {3355, 3412, 3412, 3610, 4224, 4272, 4276, 4276, 4276, 4276, 4328, 4331, 4340, 4340, 5108, 5309, 5309},
	"polished_blackstone":           {16258, 16504, 16504, 17459, 19243, 19712, 19730, 19871, 19871, 19871, 20340, 20884, 20899, 20931, 22040, 22242, 22242},
	"chiseled_polished_blackstone":  {16261, 16507, 16507, 17462, 19246, 19715, 19733, 19874, 19874, 19874, 20343, 20887, 20902, 20934, 22043, 22245, 22245},
	"purpur_block":                  {9138, 9384, 9384, 10015, 11799, 12265, 12269, 12410, 12410, 12410, 12879, 13423, 13433, 13433, 14510, 14712, 14712},
	"beacon":                        {5660, 5862, 5862, 6248, 7688, 7914, 7918, 7918, 7918, 7918, 8148, 8692, 8702, 8702, 9779, 9980, 9980},
	"quartz_block":                  {6742, 6944, 6944, 7355, 8839, 9091, 9095, 9235, 9235, 9235, 9490, 10034, 10044, 10044, 11121, 11323, 11323},
	"smooth_quartz":                 {8420, 8666, 8666, 9169, 10825, 11163, 11167, 11308, 11308, 11308, 11649, 12193, 12203, 12203, 13280, 13482, 13482},
	"end_gateway":                   {9228, 9474, 9474, 10105, 11889, 12358, 12373, 12514, 12514, 12514, 12983, 13527, 13537, 13537, 14614, 14816, 14816},
	"torch":                         {1435, 1491, 1491, 1689, 2303, 2351, 2355, 2355, 2355, 2355, 2398, 2398, 2401, 2401, 3169, 3370, 3370},
	"light_weighted_pressure_plate": {6650, 6852, 6852, 7263, 8747, 8999, 9003, 9143, 9143, 9143, 9398, 9942, 9952, 9952, 11029, 11231, 11231},
	"barrier":                       {7540, 7754, 7754, 8245, 9889, 10221, 10225, 10366, 10366, 10366, 10701, 11245, 11255, 11255, 12332, 12534, 12534},
	"polished_diorite":              {5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5},
	"polished_andesite":             {7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7},
	"smooth_basalt":                 {0, 20336, 20336, 21431, 23215, 23700, 24102, 24243, 26557, 26557, 27026, 27570, 27617, 27649, 29374, 29576, 32069},
	"polished_blackstone_bricks":    {16259, 16505, 16505, 17460, 19244, 19713, 19731, 19872, 19872, 19872, 20341, 20885, 20900, 20932, 22041, 22243, 22243},
	"amethyst_block":                {0, 17664, 17664, 18619, 20403, 20872, 20890, 21031, 21031, 21031, 21500, 22044, 22059, 22091, 23200, 23402, 23402},
	"waxed_oxidized_cut_copper":     {0, 18172, 18172, 19266, 21050, 21519, 21921, 22062, 23304, 23304, 23773, 24317, 24332, 24364, 25473, 25675, 27799},
	"soul_lantern":                  {14897, 15143, 15143, 16098, 17882, 18351, 18369, 18510, 18510, 18510, 18979, 19523, 19533, 19565, 20642, 20844, 20844},
	"soul_torch":                    {4008, 4077, 4077, 4317, 5693, 5855, 5859, 5859, 5858, 5858, 6024, 6027, 6037, 6037, 6805, 7006, 7006},
	"warped_planks":                 {15054, 15300, 15300, 16255, 18039, 18508, 18526, 18667, 18667, 18667, 19136, 19680, 19690, 19722, 20831, 21033, 21033},
	// Freestanding post state, the hub uses no others
	"polished_blackstone_brick_wall": {16351, 16597, 16597, 17552, 19336, 19805, 19823, 19964, 19964, 19964, 20433, 20977, 20992, 21024, 22133, 22335, 22335},
	// Upward facing state, the hub uses no others
	"amethyst_cluster": {0, 17675, 17675, 18630, 20414, 20883, 20901, 21042, 21042, 21042, 21511, 22055, 22070, 22102, 23211, 23413, 23413},
}

// First stained glass id, colors run consecutive
var modernGlassBase = [17]int32{4095, 4164, 4164, 4404, 5780, 5942, 5946, 5946, 5945, 5945, 6111, 6114, 6124, 6124, 6897, 7098, 7098}

// Vanilla color order shared by glass and wool
var vanillaColors = []string{
	"white", "orange", "magenta", "light_blue", "yellow", "lime",
	"pink", "gray", "light_gray", "cyan", "purple", "blue",
	"brown", "green", "red", "black",
}

// Multi state bases for the hub palette
var modernBases = map[string][17]int32{
	"end_portal_frame":      {5150, 5351, 5351, 5737, 7177, 7403, 7407, 7407, 7407, 7407, 7637, 8181, 8191, 8191, 9268, 9469, 9469},
	"nether_portal":         {4014, 4083, 4083, 4323, 5699, 5861, 5865, 5865, 5864, 5864, 6030, 6033, 6043, 6043, 6816, 7017, 7017},
	"oak_sign":              {3381, 3438, 3438, 3636, 4250, 4298, 4302, 4302, 4302, 4302, 4354, 4357, 4366, 4366, 5134, 5335, 5335},
	"oak_wall_sign":         {3735, 3802, 3802, 4032, 4678, 4758, 4762, 4762, 4762, 4762, 4846, 4849, 4858, 4858, 5626, 5827, 5827},
	"purpur_pillar":         {9139, 9385, 9385, 10016, 11800, 12266, 12270, 12411, 12411, 12411, 12880, 13424, 13434, 13434, 14511, 14713, 14713},
	"quartz_pillar":         {6744, 6946, 6946, 7357, 8841, 9093, 9097, 9237, 9237, 9237, 9492, 10036, 10046, 10046, 11123, 11325, 11325},
	"pearlescent_froglight": {0, 0, 0, 21443, 23227, 23712, 24114, 24255, 26569, 26569, 27038, 27582, 27629, 27661, 29386, 29588, 32081},
	"end_rod":               {9062, 9308, 9308, 9939, 11723, 12189, 12193, 12334, 12334, 12334, 12803, 13347, 13357, 13357, 14434, 14636, 14636},
	"warped_wall_sign":      {15735, 15981, 15981, 16936, 18720, 19189, 19207, 19348, 19348, 19348, 19817, 20361, 20371, 20403, 21512, 21714, 21714},
}

// Offset within a multi state block for one state
func modernStateOffset(base, state string) int32 {
	axisOffset := func() int32 {
		switch hub.StateProp(state, "axis") {
		case "x":
			return 0
		case "z":
			return 2
		default:
			return 1
		}
	}
	facingIndex := func(def int32) int32 {
		switch hub.StateProp(state, "facing") {
		case "north":
			return 0
		case "south":
			return 1
		case "west":
			return 2
		case "east":
			return 3
		default:
			return def
		}
	}

	switch base {
	case "end_portal_frame":
		offset := facingIndex(0)
		if hub.StateProp(state, "eye") != "true" {
			offset += 4
		}
		return offset
	case "nether_portal":
		if hub.StateProp(state, "axis") == "z" {
			return 1
		}
		return 0
	case "oak_sign":
		rotation := int32(0)
		if r := hub.StateProp(state, "rotation"); r != "" {
			for i := int32(0); i < 16; i++ {
				if r == itoa(i) {
					rotation = i
					break
				}
			}
		}
		return rotation*2 + 1
	case "oak_wall_sign", "warped_wall_sign":
		return facingIndex(0)*2 + 1
	case "purpur_pillar", "quartz_pillar", "pearlescent_froglight":
		return axisOffset()
	case "end_rod":
		switch hub.StateProp(state, "facing") {
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

// State id for one grid block name
func ModernStateID(protocol int32, block string) (int32, bool) {
	col := modernCol(protocol)
	if col < 0 {
		return 0, false
	}
	base, state := hub.SplitState(block)

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

// Inverse hub palette for puppet block watching
func ModernStateNames(protocol int32) map[int32]string {
	col := modernCol(protocol)
	if col < 0 {
		return nil
	}
	out := make(map[int32]string, 128)
	for name, ids := range modernSingles {
		if ids[col] == 0 && name != "air" {
			continue
		}
		out[ids[col]] = name
	}
	for i, color := range vanillaColors {
		out[modernGlassBase[col]+int32(i)] = color + "_stained_glass"
	}
	for base, bases := range modernBases {
		if bases[col] == 0 {
			continue
		}
		span := int32(8)
		switch base {
		case "oak_sign":
			span = 32
		case "nether_portal":
			span = 2
		case "purpur_pillar", "quartz_pillar", "pearlescent_froglight":
			span = 3
		case "end_rod":
			span = 6
		}
		for off := int32(0); off < span; off++ {
			out[bases[col]+off] = base
		}
	}
	return out
}
