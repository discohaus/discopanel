package minecraft

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeTestJar(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for path, content := range files {
		f, err := w.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanModsDir(t *testing.T) {
	dir := t.TempDir()
	writeTestJar(t, dir, "etf.jar", map[string]string{
		"fabric.mod.json":                  `{"id":"entity_texture_features","environment":"client"}`,
		"traben/etf/ETFClientCommon.class": "x",
		"traben/etf/utils/ETFUtils.class":  "x",
	})
	writeTestJar(t, dir, "serverlib.jar", map[string]string{
		"META-INF/mods.toml":              "[[mods]]\nmodId = \"serverlib\"\n",
		"dev/example/serverlib/Lib.class": "x",
	})
	writeTestJar(t, dir, "clientforge.jar", map[string]string{
		"META-INF/mods.toml": "clientSideOnly = true\n[[mods]]\nmodId = \"clientforge\"\n",
	})
	// Mirrors ETF's forge build, client-only via dependency sides
	writeTestJar(t, dir, "etf-forge.jar", map[string]string{
		"META-INF/mods.toml": `modLoader = "javafml"
loaderVersion = "[33,)"
license = "LGPL-3.0"

[[mods]]
modId = "entity_texture_features"
version = "7.0.6"
description = '''
Multi-line
description
'''

[[dependencies.entity_texture_features]]
modId="forge"
mandatory=true
versionRange="[33,)"
side="CLIENT"

[[dependencies.entity_texture_features]]
modId = "minecraft"
mandatory = true
versionRange = "[1,)"
side = "CLIENT"
`,
	})
	// Server mods with client-side optional deps stay enabled
	writeTestJar(t, dir, "serverdeps.jar", map[string]string{
		"META-INF/mods.toml": `[[mods]]
modId = "serverdeps"

[[dependencies.serverdeps]]
modId = "minecraft"
mandatory = true
side = "BOTH"

[[dependencies.serverdeps]]
modId = "somoclientlib"
mandatory = false
side = "CLIENT"
`,
	})

	metas := ScanModsDir(dir)
	if len(metas) != 5 {
		t.Fatalf("expected 5 jars scanned, got %d", len(metas))
	}

	byFile := map[string]ModJarMeta{}
	for _, m := range metas {
		byFile[m.FileName] = m
	}

	etf := byFile["etf.jar"]
	if !etf.HasModID("entity_texture_features") || !etf.ClientOnly {
		t.Fatalf("etf.jar should be client-only entity_texture_features, got %+v", etf)
	}

	lib := byFile["serverlib.jar"]
	if !lib.HasModID("serverlib") || lib.ClientOnly {
		t.Fatalf("serverlib.jar should be a server-safe mod, got %+v", lib)
	}

	cf := byFile["clientforge.jar"]
	if !cf.ClientOnly {
		t.Fatalf("clientforge.jar should honor clientSideOnly, got %+v", cf)
	}

	etfForge := byFile["etf-forge.jar"]
	if !etfForge.ClientOnly || !etfForge.HasModID("entity_texture_features") {
		t.Fatalf("etf-forge.jar should be client-only via dep sides, got %+v", etfForge)
	}

	sd := byFile["serverdeps.jar"]
	if sd.ClientOnly {
		t.Fatalf("serverdeps.jar must stay server-safe, got %+v", sd)
	}

	// Cache returns identical results on unchanged directory
	again := ScanModsDir(dir)
	if len(again) != len(metas) {
		t.Fatalf("cached rescan should match, got %d", len(again))
	}

	// Missing directory scans to nothing
	if metas := ScanModsDir(filepath.Join(dir, "nope")); metas != nil {
		t.Fatalf("missing dir should scan nil, got %+v", metas)
	}
}

// Testing avoid toml parsing panic
func TestModsTomlDeepDepTables(t *testing.T) {
	dir := t.TempDir()
	writeTestJar(t, dir, "deepdeps.jar", map[string]string{
		"META-INF/mods.toml": `[[mods]]
modId = "deepdeps"

[[dependencies.deepdeps.forge]]
modId = "forge"
mandatory = true
`,
	})

	metas := ScanModsDir(dir)
	if len(metas) != 1 {
		t.Fatalf("expected 1 jar scanned, got %d", len(metas))
	}
	if !metas[0].HasModID("deepdeps") {
		t.Fatalf("deepdeps.jar should declare deepdeps, got %+v", metas[0])
	}
	for _, d := range metas[0].Deps {
		if d.ID == "" {
			t.Fatalf("empty dep ids must be skipped, got %+v", metas[0].Deps)
		}
	}
}

func TestScanMcmodInfo(t *testing.T) {
	dir := t.TempDir()
	writeTestJar(t, dir, "legacy.jar", map[string]string{
		"mcmod.info": `[{
			"modid": "IC2",
			"version": "2.8.222",
			"mcversion": "1.12.2",
			"requiredMods": ["Forge", "cofhcore@[4.6.0,)"],
			"useDependencyInformation": true
		}]`,
	})
	writeTestJar(t, dir, "wrapped.jar", map[string]string{
		"mcmod.info": `{"modListVersion": 2, "modList": [{
			"modid": "jei",
			"version": "4.16.1",
			"requiredMods": ["ignoredmod"],
			"useDependencyInformation": false
		}]}`,
	})
	// A jar carrying both formats only speaks toml
	writeTestJar(t, dir, "dual.jar", map[string]string{
		"META-INF/mods.toml": "[[mods]]\nmodId = \"dualmod\"\nversion = \"2.0\"\n",
		"mcmod.info":         `[{"modid": "dualmod", "version": "1.0"}]`,
	})

	byFile := map[string]ModJarMeta{}
	for _, m := range ScanModsDir(dir) {
		byFile[m.FileName] = m
	}

	legacy := byFile["legacy.jar"]
	if !legacy.HasModID("ic2") || legacy.VersionOf("ic2") != "2.8.222" {
		t.Fatalf("legacy.jar should declare ic2 2.8.222, got %+v", legacy)
	}
	var cofh *ModDep
	for i := range legacy.Deps {
		switch legacy.Deps[i].ID {
		case "cofhcore":
			cofh = &legacy.Deps[i]
		case "ignoredmod":
			t.Fatalf("unexpected dep: %+v", legacy.Deps[i])
		}
	}
	if cofh == nil || cofh.Range != "[4.6.0,)" || !cofh.Mandatory || cofh.Dialect != "forge" {
		t.Fatalf("expected mandatory forge dep cofhcore [4.6.0,), got %+v", legacy.Deps)
	}

	// The forge builtin never convicts, cofhcore does
	issues := SolveDeps([]ModJarMeta{legacy}, []string{"forge"})
	if !hasIssue(issues, DepMissing, "ic2", "cofhcore") {
		t.Fatalf("expected missing cofhcore, got %+v", issues)
	}
	if hasIssue(issues, DepMissing, "ic2", "forge") {
		t.Fatalf("forge builtin must not convict, got %+v", issues)
	}

	wrapped := byFile["wrapped.jar"]
	if !wrapped.HasModID("jei") {
		t.Fatalf("wrapped.jar should declare jei, got %+v", wrapped)
	}
	if len(wrapped.Deps) != 0 {
		t.Fatalf("deps need useDependencyInformation, got %+v", wrapped.Deps)
	}

	dual := byFile["dual.jar"]
	if dual.VersionOf("dualmod") != "2.0" {
		t.Fatalf("mods.toml should outrank mcmod.info, got %+v", dual)
	}
	if len(dual.Mods) != 1 {
		t.Fatalf("dual.jar should declare dualmod once, got %+v", dual.Mods)
	}
}

func TestClientOnlySweep(t *testing.T) {
	metas := []ModJarMeta{
		{FileName: "suppsquared.jar", Mods: []ModInfo{{ID: "suppsquared"}},
			Deps: []ModDep{
				{Owner: "suppsquared", ID: "supplementaries", Mandatory: true},
				{Owner: "suppsquared", ID: "clientmod", Mandatory: true, Side: "client"},
			}},
		{FileName: "supplementaries.jar", ClientOnly: true, Mods: []ModInfo{{ID: "supplementaries"}},
			Deps: []ModDep{{Owner: "supplementaries", ID: "chained", Mandatory: true}}},
		{FileName: "chained.jar", ClientOnly: true, Mods: []ModInfo{{ID: "chained"}}},
		{FileName: "clientmod.jar", ClientOnly: true, Mods: []ModInfo{{ID: "clientmod"}}},
		{FileName: "forced.jar", ClientOnly: true, Mods: []ModInfo{{ID: "forced"}}},
	}

	drop := ClientOnlySweep(metas, []string{"forced"})
	if len(drop) != 1 || drop[0].FileName != "clientmod.jar" {
		t.Fatalf("only the unneeded client jar should drop, got %+v", drop)
	}
}

func TestHasReportedModID(t *testing.T) {
	meta := ModJarMeta{Mods: []ModInfo{{ID: "particle-effects"}}}
	if !meta.HasReportedModID("particle-effects") {
		t.Fatal("exact id must match")
	}
	// Connector registers fabric hyphen ids with underscores
	if !meta.HasReportedModID("particle_effects") {
		t.Fatal("folded loader id must match the declared id")
	}
	if meta.HasReportedModID("particle") {
		t.Fatal("prefixes must not match")
	}
	if meta.HasModID("particle_effects") {
		t.Fatal("declared id comparisons must stay exact")
	}
}

func TestNestedServiceJarProvidesModID(t *testing.T) {
	var inner bytes.Buffer
	w := zip.NewWriter(&inner)
	f, err := w.Create("META-INF/mods.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("[[mods]]\nmodId = \"connectormod\"\nversion = \"1.0.0\"\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeTestJar(t, dir, "connector.jar", map[string]string{
		"META-INF/jarjar/connector-mod.jar": inner.String(),
	})

	meta, err := ReadModJar(filepath.Join(dir, "connector.jar"))
	if err != nil {
		t.Fatal(err)
	}
	if !meta.HasModID("connectormod") {
		t.Fatalf("nested service jar mod must be visible, got %+v", meta.Mods)
	}
	for _, m := range meta.Mods {
		if m.ID == "connectormod" && m.Declared {
			t.Fatal("nested mod must not count as declared")
		}
	}
}
