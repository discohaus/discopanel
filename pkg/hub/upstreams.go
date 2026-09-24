package hub

import (
	"net/url"
	"slices"
)

// One discohaus-index cached upstream
type Upstream struct {
	Name   string
	Origin string
	Prefix string
}

// Every upstream base url. We should be able to turn all of these off for airgapped instances.
var Upstreams = []Upstream{
	{Name: "modrinth", Origin: "https://api.modrinth.com", Prefix: "/modrinth"},
	{Name: "curseforge", Origin: "https://api.curseforge.com", Prefix: "/curseforge"},
	{Name: "curseforge-cdn", Origin: "https://edge.forgecdn.net", Prefix: "/curseforge-cdn"},
	{Name: "mojang-meta", Origin: "https://piston-meta.mojang.com", Prefix: "/mojang/meta"},
	{Name: "mojang-data", Origin: "https://piston-data.mojang.com", Prefix: "/mojang/data"},
	{Name: "mojang-launcher", Origin: "https://launcher.mojang.com", Prefix: "/mojang/launcher"},
	{Name: "mojang-api", Origin: "https://api.mojang.com", Prefix: "/mojang-api"},
	{Name: "fabric", Origin: "https://meta.fabricmc.net", Prefix: "/fabric"},
	{Name: "quilt", Origin: "https://meta.quiltmc.org", Prefix: "/quilt"},
	{Name: "quilt-maven", Origin: "https://maven.quiltmc.org", Prefix: "/quilt-maven"},
	{Name: "paper", Origin: "https://fill.papermc.io", Prefix: "/paper"},
	{Name: "paper-data", Origin: "https://fill-data.papermc.io", Prefix: "/paper-data"},
	{Name: "purpur", Origin: "https://api.purpurmc.org", Prefix: "/purpur"},
	{Name: "forge-files", Origin: "https://files.minecraftforge.net", Prefix: "/forge/files"},
	{Name: "forge-maven", Origin: "https://maven.minecraftforge.net", Prefix: "/forge/maven"},
	{Name: "neoforge", Origin: "https://maven.neoforged.net", Prefix: "/neoforge"},
	{Name: "ftb", Origin: "https://api.feed-the-beast.com", Prefix: "/ftb"},
}

// Looks an upstream up by name, panicking on a name the table lacks
func upstream(name string) Upstream {
	for _, u := range Upstreams {
		if u.Name == name {
			return u
		}
	}
	panic("hub: unknown upstream " + name)
}

// Base url for a named upstream: the index prefix when enabled, its origin otherwise
func Base(name string) string {
	u := upstream(name)
	st := current.Load()
	if !st.indexOn {
		return u.Origin
	}
	return st.index.String() + u.Prefix
}

func Modrinth() string       { return Base("modrinth") }
func CurseForge() string     { return Base("curseforge") }
func CurseForgeCDN() string  { return Base("curseforge-cdn") }
func MojangMeta() string     { return Base("mojang-meta") }
func MojangData() string     { return Base("mojang-data") }
func MojangLauncher() string { return Base("mojang-launcher") }
func MojangAPI() string      { return Base("mojang-api") }
func Fabric() string         { return Base("fabric") }
func Quilt() string          { return Base("quilt") }
func QuiltMaven() string     { return Base("quilt-maven") }
func Paper() string          { return Base("paper") }
func PaperData() string      { return Base("paper-data") }
func Purpur() string         { return Base("purpur") }
func ForgeFiles() string     { return Base("forge-files") }
func ForgeMaven() string     { return Base("forge-maven") }
func NeoForge() string       { return Base("neoforge") }
func FTB() string            { return Base("ftb") }

// Hosts the panel contacts for upstream data, sorted and distinct
// The index host alone when enabled, every origin otherwise
func UpstreamHosts() []string {
	st := current.Load()
	if st.indexOn {
		return []string{st.index.Host}
	}
	hosts := make([]string, 0, len(Upstreams))
	for _, u := range Upstreams {
		parsed, err := url.Parse(u.Origin)
		if err != nil {
			panic("hub: bad upstream origin " + u.Origin)
		}
		hosts = append(hosts, parsed.Host)
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}
