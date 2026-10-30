package minecraft

import (
	"testing"
)

func TestVersionSatisfies(t *testing.T) {
	cases := []struct {
		version, rng, dialect string
		want                  bool
	}{
		{"1.2.3", "", "", true},
		{"", "[1.0,)", "", true},
		{"1.2.3", "*", "", true},

		{"43.2.0", "[43,)", "", true},
		{"42.9.9", "[43,)", "", false},
		{"1.5", "[1.0,2.0)", "", true},
		{"2.0", "[1.0,2.0)", "", false},
		{"2.0", "[1.0,2.0]", "", true},
		{"1.0", "(1.0,2.0)", "", false},
		{"0.9", "(,1.0]", "", true},
		{"1.5", "[1.0]", "", false},
		{"1.0", "[1.0]", "", true},
		{"3.5", "[1,2),[3,4)", "", true},
		{"2.5", "[1,2),[3,4)", "", false},

		{"0.5.1", ">=0.4.0", "fabric", true},
		{"0.3.9", ">=0.4.0", "fabric", false},
		{"1.20.1", "1.20.x", "fabric", true},
		{"1.21", "1.20.x", "fabric", false},
		{"1.20.4", ">=1.20 <1.21", "fabric", true},
		{"1.21", ">=1.20 <1.21", "fabric", false},
		{"2.1.0", "^2.0.0", "fabric", true},
		{"3.0.0", "^2.0.0", "fabric", false},
		{"1.2.9", "~1.2.3", "fabric", true},
		{"1.3.0", "~1.2.3", "fabric", false},
		{"1.19.2", "1.19.2 || 1.20.1", "fabric", true},
		{"1.20.1", "1.19.2 || 1.20.1", "fabric", true},
		{"1.18", "1.19.2 || 1.20.1", "fabric", false},

		// A bare maven version is a soft hint, anything satisfies
		{"2.0.9", "2.0.0-beta.17", "neoforge", true},
		{"1.0", "9.9.9", "forge", true},
		{"2.0.9", "2.0.0-beta.17", "fabric", false},

		// Unparseable ranges never convict
		{"1.0", "[broken", "", true},
		{"1.0", "??", "", true},
	}
	for _, tc := range cases {
		if got := VersionSatisfies(tc.version, tc.rng, tc.dialect); got != tc.want {
			t.Errorf("VersionSatisfies(%q, %q, %q) = %v, want %v", tc.version, tc.rng, tc.dialect, got, tc.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2", "1.2.0", 0},
		{"1.10", "1.9", 1},
		{"1.2.3-beta", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc1", 1},
		{"1.2.3+build5", "1.2.3", 0},
		{"2.0", "10.0", -1},

		// Letter suffixes split off and outrank a missing segment
		{"1.11.2b", "1.11.1", 1},
		{"1.11.2b", "1.11.2", 1},
		{"1.11.2b", "1.11.3", -1},
		{"1.21z", "1.21", 1},
		{"1.21z", "1.21.1", -1},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSolveDepsMissingAndVersion(t *testing.T) {
	metas := []ModJarMeta{
		{
			FileName: "alpha.jar",
			Mods:     []ModInfo{{ID: "alpha", Version: "1.0", Declared: true, Dialect: "fabric"}},
			Deps: []ModDep{
				{Owner: "alpha", ID: "beta", Mandatory: true, Dialect: "fabric"},
				{Owner: "alpha", ID: "gamma", Range: ">=2.0", Mandatory: true, Dialect: "fabric"},
				{Owner: "alpha", ID: "minecraft", Range: "1.20.x", Mandatory: true, Dialect: "fabric"},
				{Owner: "alpha", ID: "fabricloader", Mandatory: true, Dialect: "fabric"},
				{Owner: "alpha", ID: "optional_thing", Mandatory: false, Dialect: "fabric"},
				{Owner: "alpha", ID: "clientside", Mandatory: true, Side: "client", Dialect: "fabric"},
			},
		},
		{
			FileName: "gamma.jar",
			Mods:     []ModInfo{{ID: "gamma", Version: "1.5", Declared: true, Dialect: "fabric"}},
		},
	}
	issues := SolveDeps(metas, []string{"fabric"})

	if !hasIssue(issues, DepMissing, "alpha", "beta") {
		t.Errorf("expected missing beta, got %+v", issues)
	}
	if !hasIssue(issues, DepVersion, "alpha", "gamma") {
		t.Errorf("expected gamma version mismatch, got %+v", issues)
	}
	for _, issue := range issues {
		switch issue.DepID {
		case "minecraft", "fabricloader", "optional_thing", "clientside":
			t.Errorf("unexpected issue for %s: %+v", issue.DepID, issue)
		}
	}
}

func TestSolveDepsPlatformRangesNeverConvict(t *testing.T) {
	// FML softens platform ranges, so [1.21,1.21.1) loads on 1.21.1
	metas := []ModJarMeta{
		{
			FileName: "avaritia.jar",
			Mods:     []ModInfo{{ID: "avaritia", Version: "1.3.1", Declared: true, Dialect: "neoforge"}},
			Deps: []ModDep{
				{Owner: "avaritia", ID: "minecraft", Range: "[1.21,1.21.1)", Mandatory: true, Dialect: "neoforge"},
				{Owner: "avaritia", ID: "neoforge", Range: "[21.1.0,)", Mandatory: true, Dialect: "neoforge"},
			},
		},
	}
	if issues := SolveDeps(metas, []string{"neoforge", "forge"}); len(issues) != 0 {
		t.Errorf("platform ranges must never convict, got %+v", issues)
	}
}

func TestSolveDepsFiltersInertDialects(t *testing.T) {
	// Universal jar fabric deps stay inert on neoforge
	metas := []ModJarMeta{
		{
			FileName: "collective.jar",
			Mods: []ModInfo{
				{ID: "collective", Version: "8.39", Declared: true, Dialect: "fabric"},
				{ID: "collective", Version: "8.39", Declared: true, Dialect: "forge"},
				{ID: "collective", Version: "8.39", Declared: true, Dialect: "neoforge"},
			},
			Deps: []ModDep{
				{Owner: "collective", ID: "fabric", Range: "*", Mandatory: true, Dialect: "fabric"},
			},
		},
	}
	if issues := SolveDeps(metas, []string{"neoforge", "forge"}); len(issues) != 0 {
		t.Errorf("inert dialect deps must not report, got %+v", issues)
	}
	if issues := SolveDeps(metas, []string{"fabric"}); !hasIssue(issues, DepMissing, "collective", "fabric") {
		t.Errorf("active dialect deps must report, got %+v", issues)
	}
}

func TestSolveDepsNeoforgeFallsBackToForgeManifest(t *testing.T) {
	// Legacy neoforge jars only carry the forge manifest
	metas := []ModJarMeta{
		{
			FileName: "legacy.jar",
			Mods:     []ModInfo{{ID: "legacy", Version: "1.0", Declared: true, Dialect: "forge"}},
			Deps: []ModDep{
				{Owner: "legacy", ID: "somelib", Mandatory: true, Dialect: "forge"},
			},
		},
	}
	if issues := SolveDeps(metas, []string{"neoforge", "forge"}); !hasIssue(issues, DepMissing, "legacy", "somelib") {
		t.Errorf("forge manifest should apply on neoforge, got %+v", issues)
	}
}

func TestSolveDepsDuplicatesAndBreaks(t *testing.T) {
	metas := []ModJarMeta{
		{
			FileName: "libv1.jar",
			Mods:     []ModInfo{{ID: "somelib", Version: "1.0", Declared: true, Dialect: "fabric"}},
		},
		{
			FileName: "libv2.jar",
			Mods:     []ModInfo{{ID: "somelib", Version: "2.0", Declared: true, Dialect: "fabric"}},
		},
		{
			FileName: "hater.jar",
			Mods:     []ModInfo{{ID: "hater", Version: "1.0", Declared: true, Dialect: "fabric"}},
			Deps: []ModDep{
				{Owner: "hater", ID: "somelib", Range: "<2.0", Breaks: true, Dialect: "fabric"},
			},
		},
		{
			FileName: "bundle.jar",
			Mods: []ModInfo{
				{ID: "bundle", Version: "1.0", Declared: true, Dialect: "fabric"},
				{ID: "somelib", Version: "1.5", Dialect: "fabric"}, // Nested, never a duplicate
			},
		},
	}
	issues := SolveDeps(metas, []string{"fabric"})

	var dup *DepIssue
	for i := range issues {
		if issues[i].Kind == DepDuplicate && issues[i].ModID == "somelib" {
			dup = &issues[i]
		}
	}
	if dup == nil {
		t.Fatalf("expected somelib duplicate, got %+v", issues)
	}
	if dup.File != "libv2.jar" || dup.OtherFile != "libv1.jar" {
		t.Errorf("duplicate should keep the newer file first, got %+v", dup)
	}
	if !hasIssue(issues, DepBreaks, "hater", "somelib") {
		t.Errorf("expected breaks issue, got %+v", issues)
	}
}

func TestSolveDepsJarNeverDuplicatesItself(t *testing.T) {
	// Same id twice inside one jar is fine
	metas := []ModJarMeta{
		{
			FileName: "universal.jar",
			Mods: []ModInfo{
				{ID: "unimod", Version: "2.0", Declared: true, Dialect: "forge"},
				{ID: "unimod", Version: "2.0", Declared: true},
			},
		},
		{
			FileName: "older.jar",
			Mods:     []ModInfo{{ID: "unimod", Version: "1.0", Declared: true, Dialect: "forge"}},
		},
	}
	issues := SolveDeps(metas, []string{"forge"})
	for _, issue := range issues {
		if issue.Kind == DepDuplicate && issue.File == issue.OtherFile {
			t.Errorf("jar convicted of duplicating itself: %+v", issue)
		}
	}
	if !hasIssue(issues, DepDuplicate, "unimod", "") {
		t.Errorf("expected unimod duplicate across jars, got %+v", issues)
	}
}

func TestSolveDepsNoDialectSolvesNothing(t *testing.T) {
	// No dialect testimony means the platform is unknown
	metas := []ModJarMeta{
		{
			FileName: "a.jar",
			Mods:     []ModInfo{{ID: "dup", Version: "1.0", Declared: true, Dialect: "fabric"}},
			Deps: []ModDep{
				{Owner: "dup", ID: "missing", Mandatory: true, Dialect: "fabric"},
			},
		},
		{
			FileName: "b.jar",
			Mods:     []ModInfo{{ID: "dup", Version: "2.0", Declared: true, Dialect: "forge"}},
		},
	}
	if issues := SolveDeps(metas, nil); len(issues) != 0 {
		t.Errorf("unscoped solve must report nothing, got %+v", issues)
	}
}

func hasIssue(issues []DepIssue, kind DepIssueKind, modID, depID string) bool {
	for _, issue := range issues {
		if issue.Kind == kind && issue.ModID == modID && issue.DepID == depID {
			return true
		}
	}
	return false
}
