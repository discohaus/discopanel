package provisioner

import (
	"strings"

	"github.com/discohaus/discopanel/pkg/minecraft"
)

// Known client mods by slug or jar stem, itzg seeded
var clientOnlyMods = []string{
	"advancement-plaques",
	"ambience-music-mod",
	"ambientsounds",
	"appleskin",
	"armor-chroma",
	"armor-toughness-bar",
	"audio-extension-for-fancymenu-forge",
	"auudio-forge",
	"badoptimizations",
	"beehivetooltips",
	"better-advancements",
	"better-foliage",
	"better-modlist-neoforge",
	"better-placement",
	"better-sprinting",
	"better-third-person",
	"better-tips-nbt-tag",
	"betterbiomereblend",
	"betterf3",
	"betterfps",
	"betterfpsdist",
	"bettergrassify",
	"biomeinfo",
	"block-drops-jei-addon",
	"blur-forge",
	"cartography",
	"chattoggle",
	"cherished-worlds",
	"chunk-animator",
	"cinematiczoom",
	"clickable-advancements",
	"colorwheel",
	"colorwheel-patcher",
	"compass-coords",
	"config-menus-forge",
	"configured",
	"controllable",
	"controlling",
	"craftpresence",
	"crash-assistant",
	"createbetterfps",
	"ctm",
	"cull-less-leaves",
	"custom-main-menu",
	"dark-mode-everywhere",
	"defensive-measures",
	"ding",
	"distraction-free-recipes",
	"drippy-loading-screen",
	"dynamic-surroundings",
	"dynamic-view",
	"dynamiccrosshair",
	"dynamiclights-reforged",
	"easiervillagertrading",
	"effective-forge",
	"embeddium",
	"embeddium-extension",
	"embeddium-extras",
	"enchantment-descriptions",
	"enhanced-boss-bars",
	"enhancedvisuals",
	"entity-collision-fps-fix",
	"entity-model-features",
	"entity-texture-features-fabric",
	"entityculling",
	"equipment-compare",
	"essential-mod",
	"euphoria-patcher",
	"euphoria-patches",
	"extrasounds",
	"extreme-sound-muffler",
	"ezzoom",
	"fading-night-vision",
	"falling-leaves-forge",
	"fancymenu",
	"farsight",
	"faster-ladder-climbing",
	"fastquit",
	"fastquit-forge",
	"flerovium",
	"foamfix-optimization-mod",
	"forgeskyboxes",
	"fps-reducer",
	"free-cam",
	"ftb-backups-2",
	"fullscreen-windowed-borderless-for-minecraft",
	"gpumemleakfix",
	"hwyla",
	"iceberg",
	"ignitioncoil",
	"illager-raid-music",
	"immediatelyfast",
	"immersive-damage-indicators",
	"inmisaddon",
	"iris-flywheel-compat",
	"iris-shader-folder",
	"irisflw",
	"irisshaders",
	"item-borders",
	"item-highlighter",
	"item-obliterator",
	"itemphysic-lite",
	"itemzoom",
	"just-enough-harvestcraft",
	"just-enough-mekanism-multiblocks",
	"just-enough-resources-jer",
	"just-zoom",
	"legendary-tooltips",
	"lighty",
	"loot-capacitor-tooltips",
	"loot-journal",
	"lootbeams",
	"magnesium-extras",
	"make-bubbles-pop",
	"mekalus-oculus-fork-with-fixed-mekanism-mekasuit",
	"melody",
	"menumobs",
	"minecraft-rich-presence",
	"mining-speed-tooltips",
	"model-gap-fix",
	"more-overlays",
	"mouse-tweaks",
	"neat",
	"nekos-enchanted-books",
	"neoculus",
	"no-nv-flash",
	"no-recipe-book",
	"not-enough-animations",
	"oculus",
	"oculus-flywheel-compat",
	"ok-zoomer",
	"oldjavawarning",
	"overloaded-armor-bar",
	"packmenu",
	"packmodemenu",
	"particle-core",
	"particle-effects",
	"particle-effects-reforged",
	"particle-rain",
	"particular",
	"particular-reforged",
	"perception",
	"radium",
	"reauth",
	"rebind-narrator",
	"reblured",
	"reeses-sodium-options",
	"reforgium",
	"resource-reloader",
	"rubidium",
	"rubidium-extra",
	"ryoamiclights",
	"schematica",
	"seamless-loading-screen",
	"seamless-loading-screen-forge",
	"searchables",
	"seasonhud",
	"shouldersurfing",
	"shulkerboxviewer",
	"simplemenu",
	"skin-layers-3d",
	"smart-hud",
	"smithing-template-viewer",
	"smooth-font",
	"smooth-swapping",
	"smoothwater",
	"sodium",
	"sodium-extra",
	"sodium-extras",
	"sodium-options-api",
	"sodium-rubidium-occlusion-culling-fix",
	"sodiumdynamiclights",
	"sound",
	"sound-filters",
	"sound-physics-remastered",
	"sound-reloader",
	"status-effect-bars-reforged",
	"stellar-sky",
	"swingthroughgrass",
	"textrues-embeddium-options",
	"textrues-rubidium-options",
	"thaumic-jei",
	"tips",
	"toast-control",
	"torohealth-damage-indicators",
	"true-darkness",
	"ungrab-mouse-mod",
	"vanillafix",
	"visual-keybinder",
	"visuality",
	"waila-harvestability",
	"waila-stages",
	"wakes-reforged",
	"wawla",
	"welcome-screen",
	"xaeroplus",
	"yungs-menu-tweaks",
	"zume",
}

// Normalized keys the known list folds to
var clientOnlyKeys = func() map[string]bool {
	keys := make(map[string]bool, len(clientOnlyMods))
	for _, name := range clientOnlyMods {
		keys[clientModKey(name)] = true
	}
	return keys
}()

// Tokens that name a platform rather than the mod
var clientNoiseTokens = func() map[string]bool {
	noise := map[string]bool{"mc": true}
	for _, name := range minecraft.PackLoaderNames() {
		noise[name] = true
	}
	return noise
}()

// Reports whether a slug or jar names a client mod
func knownClientMod(name string) bool {
	return clientOnlyKeys[clientModKey(name)]
}

// Folds a name to a platform and version free key
func clientModKey(name string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".jar")
	var b strings.Builder
	for _, tok := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == '+' || r == ' '
	}) {
		if versionToken(tok) {
			break
		}
		if clientNoiseTokens[tok] {
			continue
		}
		b.WriteString(tok)
	}
	return b.String()
}

// Reports whether a token starts the version tail
func versionToken(tok string) bool {
	for _, prefix := range []string{"mc", "v", ""} {
		rest, ok := strings.CutPrefix(tok, prefix)
		if !ok || rest == "" || rest[0] < '0' || rest[0] > '9' {
			continue
		}
		return strings.Contains(rest, ".") || strings.Trim(rest, "0123456789") == ""
	}
	return false
}
