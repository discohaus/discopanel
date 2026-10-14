package mcconsole

import (
	"slices"
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestHelpSyntaxFor(t *testing.T) {
	cases := []struct {
		loader v1.ModLoader
		want   HelpSyntax
	}{
		{v1.ModLoader_MOD_LOADER_VANILLA, HelpSyntaxVanilla},
		{v1.ModLoader_MOD_LOADER_FORGE, HelpSyntaxVanilla},
		{v1.ModLoader_MOD_LOADER_NEOFORGE, HelpSyntaxVanilla},
		{v1.ModLoader_MOD_LOADER_FABRIC, HelpSyntaxVanilla},
		{v1.ModLoader_MOD_LOADER_QUILT, HelpSyntaxVanilla},
		{v1.ModLoader_MOD_LOADER_PAPER, HelpSyntaxBukkit},
		{v1.ModLoader_MOD_LOADER_PURPUR, HelpSyntaxBukkit},
		{v1.ModLoader_MOD_LOADER_FOLIA, HelpSyntaxBukkit},
		{v1.ModLoader_MOD_LOADER_UNSPECIFIED, HelpSyntaxUnknown},
	}
	for _, c := range cases {
		if got := HelpSyntaxFor(c.loader); got != c.want {
			t.Errorf("HelpSyntaxFor(%v) = %v, want %v", c.loader, got, c.want)
		}
	}
}

func TestNewCompletionEngine(t *testing.T) {
	commands := CommandFunc(func(string) (string, error) { return "", nil })

	engine, err := NewCompletionEngine(v1.ModLoader_MOD_LOADER_FABRIC, commands, nil)
	if err != nil {
		t.Fatalf("fabric engine error: %v", err)
	}
	if _, ok := engine.(*VanillaEngine); !ok {
		t.Errorf("fabric engine = %T, want *VanillaEngine", engine)
	}

	engine, err = NewCompletionEngine(v1.ModLoader_MOD_LOADER_PURPUR, commands, nil)
	if err != nil {
		t.Fatalf("purpur engine error: %v", err)
	}
	if _, ok := engine.(*PaperEngine); !ok {
		t.Errorf("purpur engine = %T, want *PaperEngine", engine)
	}

	engine, err = NewCompletionEngine(v1.ModLoader_MOD_LOADER_UNSPECIFIED, commands, nil)
	if err == nil || engine != nil {
		t.Errorf("unspecified loader gave engine %v, err %v, want error", engine, err)
	}
}

func TestHelpLookup(t *testing.T) {
	var sent []string
	help := helpLookup(CommandFunc(func(command string) (string, error) {
		sent = append(sent, command)
		return "", nil
	}))
	for _, query := range []string{"", "Bukkit", "execute as"} {
		if _, err := help(query); err != nil {
			t.Fatalf("help(%q) error: %v", query, err)
		}
	}
	want := []string{"help", "help Bukkit", "help execute as"}
	if !slices.Equal(sent, want) {
		t.Errorf("help commands sent = %v, want %v", sent, want)
	}
}
