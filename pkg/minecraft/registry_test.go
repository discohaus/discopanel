package minecraft

import (
	"testing"

	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// Rows must match the proto enum exactly
func TestRegistryCoversProtoEnum(t *testing.T) {
	for val, name := range v1.ModLoader_name {
		p := v1.ModLoader(val)
		if p == v1.ModLoader_MOD_LOADER_UNSPECIFIED {
			continue
		}
		if _, ok := loaderIndex[p]; !ok {
			t.Errorf("enum value %s has no registry row", name)
		}
	}
	if len(registry) != len(v1.ModLoader_name)-1 {
		t.Errorf("registry has %d rows, enum has %d values", len(registry), len(v1.ModLoader_name)-1)
	}
}

func TestRegistryRowsConsistent(t *testing.T) {
	seenProto := map[v1.ModLoader]bool{}
	for _, row := range registry {
		l := row.Loader()
		if l == v1.ModLoader_MOD_LOADER_UNSPECIFIED {
			t.Errorf("row %s declares no loader", row.Info.DisplayName)
		}
		if seenProto[l] {
			t.Errorf("proto value %s mapped twice", l)
		}
		seenProto[l] = true
		if row.Info.Name == "" || row.Info.DisplayName == "" || row.Info.Description == "" ||
			row.Info.Category == optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_UNSPECIFIED {
			t.Errorf("loader %s misses display facts", l)
		}
		for _, d := range row.Dialects {
			if definingLoader(d) == nil {
				t.Errorf("loader %s reads unknown dialect %q", l, d)
			}
		}
		if len(row.Dialects) > 0 && row.Info.ModsDirectory == "" {
			t.Errorf("loader %s reads mods but stores none", l)
		}
		defining := len(row.Dialects) > 0 && row.Dialects[0] == protometa.Name(l)
		if !defining && (len(row.Builtins) > 0 || len(row.Facets) > 0 || len(row.Markers) > 0 || row.MavenRanges) {
			t.Errorf("loader %s carries format facts without defining one", l)
		}
		if defining && (len(row.Facets) == 0 || len(row.Markers) == 0) {
			t.Errorf("defining loader %s misses facets or markers", l)
		}
	}
}

// Names must parse back to their own enum value
func TestLoaderNameRoundTrip(t *testing.T) {
	for _, row := range registry {
		back, ok := protometa.FromName[v1.ModLoader](protometa.Name(row.Loader()))
		if !ok || back != row.Loader() {
			t.Errorf("round trip broke for %s", row.Loader())
		}
	}
	if _, ok := protometa.FromName[v1.ModLoader]("nonsense"); ok {
		t.Error("unknown name parsed to a loader")
	}
}
