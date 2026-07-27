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

func TestInferDialect(t *testing.T) {
	fabricOnly := ModJarMeta{FileName: "a.jar", Mods: []ModInfo{{ID: "a", Dialect: "fabric"}}}
	forgeOnly := ModJarMeta{FileName: "b.jar", Mods: []ModInfo{{ID: "b", Dialect: "forge"}}}
	dual := ModJarMeta{FileName: "c.jar", Mods: []ModInfo{
		{ID: "c", Dialect: "forge"}, {ID: "c", Dialect: "neoforge"},
	}}
	neoOnly := ModJarMeta{FileName: "d.jar", Mods: []ModInfo{{ID: "d", Dialect: "neoforge"}}}

	cases := []struct {
		name  string
		metas []ModJarMeta
		want  string
	}{
		{"empty", nil, ""},
		{"exclusive fabric", []ModJarMeta{fabricOnly}, "fabric"},
		{"exclusive neoforge beats dual", []ModJarMeta{dual, neoOnly}, "neoforge"},
		{"dual jars settle on family base", []ModJarMeta{dual}, "forge"},
		{"mixed families stay unknown", []ModJarMeta{fabricOnly, forgeOnly}, ""},
		{"split exclusives settle on family", []ModJarMeta{forgeOnly, neoOnly}, "forge"},
	}
	for _, tc := range cases {
		if got := inferDialect(tc.metas); got != tc.want {
			t.Errorf("%s: inferDialect = %q, want %q", tc.name, got, tc.want)
		}
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
