package minecraft

import (
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/discohaus/discopanel/pkg/runtimespec"
)

func TestDialectBuiltin(t *testing.T) {
	cases := []struct {
		dialect, id string
		want        bool
	}{
		{"fabric", "fabricloader", true},
		{"fabric", "minecraft", true},
		{"quilt", "quilt_loader", true},
		// Quilt reads fabric manifests, so it provides fabric ids
		{"quilt", "fabricloader", true},
		{"neoforge", "neoforge", true},
		{"neoforge", "fml", true},
		{"forge", "neoforge", false},
		{"fabric", "forge", false},
		// Unknown dialect falls back to every platform id
		{"", "quilt_base", true},
		{"", "sodium", false},
	}
	for _, tc := range cases {
		if got := dialectBuiltin(tc.dialect, tc.id); got != tc.want {
			t.Errorf("dialectBuiltin(%q, %q) = %v, want %v", tc.dialect, tc.id, got, tc.want)
		}
	}
}

func TestDialectFacets(t *testing.T) {
	got := DialectFacets([]string{"quilt", "neoforge"})
	want := []string{"quilt", "fabric", "neoforge"}
	if !slices.Equal(got, want) {
		t.Fatalf("DialectFacets = %v, want %v", got, want)
	}
	if DialectFacets(nil) != nil {
		t.Fatal("no dialects should yield no facets")
	}
}

func TestInferLoader(t *testing.T) {
	jar := func(name string, dialects ...string) ModJarMeta {
		m := ModJarMeta{FileName: name + ".jar"}
		for _, d := range dialects {
			m.Mods = append(m.Mods, ModInfo{ID: name, Declared: true, Dialect: d})
		}
		return m
	}
	fabricOnly := jar("a", "fabric")
	forgeOnly := jar("b", "forge")
	dual := jar("c", "forge", "neoforge")
	neoOnly := jar("d", "neoforge")
	quiltOnly := jar("e", "quilt")
	dualQuilt := jar("f", "fabric", "quilt")
	nested := jar("g", "neoforge")
	nested.Mods = append(nested.Mods, ModInfo{ID: "lib", Dialect: "forge"})

	cases := []struct {
		name  string
		metas []ModJarMeta
		want  v1.ModLoader
	}{
		{"empty", nil, v1.ModLoader_MOD_LOADER_UNSPECIFIED},
		{"exclusive fabric", []ModJarMeta{fabricOnly}, v1.ModLoader_MOD_LOADER_FABRIC},
		{"exclusive neoforge beats dual", []ModJarMeta{dual, neoOnly}, v1.ModLoader_MOD_LOADER_NEOFORGE},
		{"dual jars settle on family base", []ModJarMeta{dual}, v1.ModLoader_MOD_LOADER_FORGE},
		{"mixed families stay unknown", []ModJarMeta{fabricOnly, forgeOnly}, v1.ModLoader_MOD_LOADER_UNSPECIFIED},
		{"split exclusives need the covering loader", []ModJarMeta{forgeOnly, neoOnly}, v1.ModLoader_MOD_LOADER_NEOFORGE},
		{"dual quilt jars stay fabric", []ModJarMeta{fabricOnly, dualQuilt}, v1.ModLoader_MOD_LOADER_FABRIC},
		{"quilt only jar forces quilt", []ModJarMeta{fabricOnly, dualQuilt, quiltOnly}, v1.ModLoader_MOD_LOADER_QUILT},
		{"nested manifests never vote", []ModJarMeta{nested}, v1.ModLoader_MOD_LOADER_NEOFORGE},
	}
	for _, tc := range cases {
		got := v1.ModLoader_MOD_LOADER_UNSPECIFIED
		if row := inferLoader(tc.metas); row != nil {
			got = row.Loader()
		}
		if got != tc.want {
			t.Errorf("%s: inferLoader = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestInferModsLoader(t *testing.T) {
	mods := t.TempDir()
	if got := InferModsLoader(mods); got != v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		t.Fatalf("empty dir must stay unknown, got %v", got)
	}
	writeTestJar(t, mods, "plain.jar", map[string]string{
		"fabric.mod.json": `{"id":"plain","version":"1.0"}`,
	})
	writeTestJar(t, mods, "dual.jar", map[string]string{
		"fabric.mod.json": `{"id":"dual","version":"1.0"}`,
		"quilt.mod.json":  `{"quilt_loader":{"id":"dual","version":"1.0"}}`,
	})
	if got := InferModsLoader(mods); got != v1.ModLoader_MOD_LOADER_FABRIC {
		t.Fatalf("dual jars must not force quilt, got %v", got)
	}
	writeTestJar(t, mods, "quiltish.jar", map[string]string{
		"quilt.mod.json": `{"quilt_loader":{"id":"quiltish","version":"1.0"}}`,
	})
	if got := InferModsLoader(mods); got != v1.ModLoader_MOD_LOADER_QUILT {
		t.Fatalf("quilt only jar must force quilt, got %v", got)
	}
}

func TestResolveDialects(t *testing.T) {
	dir := t.TempDir()
	mods := filepath.Join(dir, "mods")

	// A declared loader never touches the disk
	if got := ResolveDialects(v1.ModLoader_MOD_LOADER_QUILT, dir, mods); !slices.Equal(got, []string{"quilt", "fabric"}) {
		t.Fatalf("quilt resolved %v", got)
	}
	if got := ResolveDialects(v1.ModLoader_MOD_LOADER_MOHIST, dir, mods); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("mohist resolved %v", got)
	}
	if got := ResolveDialects(v1.ModLoader_MOD_LOADER_PAPER, dir, mods); got != nil {
		t.Fatalf("paper resolved %v, want none", got)
	}

	// Pack platforms declare nothing, the install testifies
	if got := ResolveDialects(v1.ModLoader_MOD_LOADER_MODRINTH, dir, mods); got != nil {
		t.Fatalf("empty install resolved %v", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "libraries", "net", "neoforged"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDialects(v1.ModLoader_MOD_LOADER_MODRINTH, dir, mods); !slices.Equal(got, []string{"neoforge", "forge"}) {
		t.Fatalf("neoforged libraries resolved %v", got)
	}

	// A launch spec naming a loader outranks stale libraries
	if err := runtimespec.WriteLaunchSpec(dir, &v1.LaunchSpec{Loader: v1.ModLoader_MOD_LOADER_FABRIC}); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDialects(v1.ModLoader_MOD_LOADER_MODRINTH, dir, mods); !slices.Equal(got, []string{"fabric"}) {
		t.Fatalf("launch spec resolved %v", got)
	}

	// Hybrid brands in the spec resolve through their registry row
	if err := runtimespec.WriteLaunchSpec(dir, &v1.LaunchSpec{Loader: v1.ModLoader_MOD_LOADER_MOHIST}); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDialects(v1.ModLoader_MOD_LOADER_CUSTOM, dir, mods); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("hybrid spec resolved %v", got)
	}
}
