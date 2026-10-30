package db

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// config.example.yaml ships in the image and is copied as config.yaml, and
// its global_config overrides the annotated defaults when the global
// settings are first seeded. Values there must match the panel defaults,
// or every new server inherits them (issue #101: max-world-size=0 and
// enable-status=false ended up in server.properties).
func TestExampleConfigMatchesDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Minecraft struct {
			GlobalConfig map[string]any `yaml:"global_config"`
		} `yaml:"minecraft"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Minecraft.GlobalConfig) == 0 {
		t.Fatal("example config has no minecraft.global_config")
	}

	base := &v1.ServerProperties{}
	seedAnnotationDefaults(base)
	decoded := proto.Clone(base).(*v1.ServerProperties)
	if err := mapstructure.WeakDecode(doc.Minecraft.GlobalConfig, decoded); err != nil {
		t.Fatal(err)
	}

	var bad []string
	bm, dm := base.ProtoReflect(), decoded.ProtoReflect()
	for _, p := range protometa.Props(bm.Descriptor()) {
		if p.Meta.DefaultValue == "" || p.Meta.Ephemeral {
			continue
		}
		want, _ := protometa.ScalarString(bm, p.Field)
		got, _ := protometa.ScalarString(dm, p.Field)
		if got != want {
			bad = append(bad, string(p.Field.Name())+": example="+got+" default="+want)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("config.example.yaml overrides %d defaults:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
}
