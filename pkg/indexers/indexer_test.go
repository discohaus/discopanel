package indexers

import (
	"testing"

	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
)

func TestRegisterIndexerMetadata(t *testing.T) {
	RegisterIndexer("test-beta",
		func(_, _ string) ModpackIndexer { return nil },
		WithCredentialProperty("betaKey"),
		WithPackSource(optionsv1.PackSource_PACK_SOURCE_MODRINTH),
		WithForceIncludeProperty("betaForce"),
	)
	RegisterIndexer("test-alpha", func(_, _ string) ModpackIndexer { return nil })

	byName := map[string]IndexerInfo{}
	last := ""
	for _, info := range Indexers() {
		if info.Name < last {
			t.Fatalf("names must sort, %q after %q", info.Name, last)
		}
		last = info.Name
		byName[info.Name] = info
	}

	b := byName["test-beta"]
	if b.CredentialProperty != "betaKey" || b.PackSource != optionsv1.PackSource_PACK_SOURCE_MODRINTH || b.ForceIncludeProperty != "betaForce" {
		t.Fatalf("metadata lost, got %+v", b)
	}
	a := byName["test-alpha"]
	if a.CredentialProperty != "" || a.PackSource != optionsv1.PackSource_PACK_SOURCE_UNSPECIFIED || a.ForceIncludeProperty != "" {
		t.Fatalf("unexpected metadata %+v", a)
	}
}

func TestNewIndexerUnknown(t *testing.T) {
	if _, err := NewIndexer("test-missing", "", "ua"); err == nil {
		t.Fatal("unknown indexer must error")
	}
}

func TestOrderByPackSourcePrefersPackSource(t *testing.T) {
	infos := []IndexerInfo{
		{Name: "aaa"},
		{Name: "mmm", PackSource: optionsv1.PackSource_PACK_SOURCE_CURSEFORGE},
		{Name: "zzz", PackSource: optionsv1.PackSource_PACK_SOURCE_MODRINTH},
	}

	got := OrderByPackSource(infos, optionsv1.PackSource_PACK_SOURCE_MODRINTH)
	if len(got) != 3 || got[0].Name != "zzz" || got[1].Name != "aaa" || got[2].Name != "mmm" {
		t.Fatalf("pack source must lead, got %+v", got)
	}

	got = OrderByPackSource(infos, optionsv1.PackSource_PACK_SOURCE_UNSPECIFIED)
	if len(got) != 3 || got[0].Name != "aaa" || got[1].Name != "mmm" || got[2].Name != "zzz" {
		t.Fatalf("no pack source keeps registry order, got %+v", got)
	}
}
