package mcconsole

import (
	"errors"
	"reflect"
	"testing"
)

func TestNewPaperEngine(t *testing.T) {
	provider := CommandFunc(func(cmd string) (string, error) {
		if cmd == "help" {
			return "help output", nil
		}
		if cmd == "help Bukkit" {
			return "bukkit output", nil
		}
		return "", errors.New("unknown command")
	})

	engine := NewPaperEngine(provider)
	if engine == nil {
		t.Fatal("Expected: Engine-instance, Got: nil")
	}

	res, err := engine.helpFunc("")
	if err != nil || res != "help output" {
		t.Errorf("helpFunc(\"\") error: got %q, err %v", res, err)
	}

	res, err = engine.helpFunc("Bukkit")
	if err != nil || res != "bukkit output" {
		t.Errorf("helpFunc(\"Bukkit\") error: got %q, err %v", res, err)
	}
}

func TestParseHelpNamespaces(t *testing.T) {
	input := StripMinecraftColors(`§e--------- §fHelp: §rIndex (1/23) §e--------------------------
§7Use /help [n] to get page n of help.
§7§6Aliases: §fLists command aliases
§f§6Bukkit: §fAll commands for Bukkit
§f§6Minecraft: §fAll commands for Minecraft
§f§6Paper: §fAll commands for Paper
§f§6/about: §fGets the version of this server including any plugins in use`)

	expected := []string{"Aliases", "Bukkit", "Minecraft", "Paper"}
	if got := parseHelpNamespaces(input); !reflect.DeepEqual(got, expected) {
		t.Errorf("parseHelpNamespaces = %v, want %v", got, expected)
	}
}

func TestParseHelpCommands(t *testing.T) {
	input := StripMinecraftColors(`§e--------- §fHelp: §rPaper (1/3) §e---------------------------
§7Below is a list of all Paper commands:
§7§6/about: §fGets the version of this server including any §fplugins in use
§f§6/bukkit:about: §fGets the version of this server
§f§6/plugins: §fGets a list of plugins`)

	expected := []string{"about", "bukkit:about", "plugins"}
	if got := parseHelpCommands(input); !reflect.DeepEqual(got, expected) {
		t.Errorf("parseHelpCommands = %v, want %v", got, expected)
	}
}

func TestParseHelpAliases(t *testing.T) {
	input := StripMinecraftColors(`§e--------- §fHelp: §rAliases (1/1) §e-------------------------
§7Below is a list of all command aliases:
§6/pl: §f§eAlias for §f/plugins
§6/ver: §f§eAlias for §f/version
§6/about: §fGets the version of this server`)

	expected := [][2]string{{"pl", "plugins"}, {"ver", "version"}}
	if got := parseHelpAliases(input); !reflect.DeepEqual(got, expected) {
		t.Errorf("parseHelpAliases = %v, want %v", got, expected)
	}
}

// Serves canned help pages keyed by the exact help query
func pagedHelp(pages map[string]string) func(string) (string, error) {
	return func(query string) (string, error) {
		page, ok := pages[query]
		if !ok {
			return "", errors.New("no help for " + query)
		}
		return page, nil
	}
}

func commandNames(pages []string) [][]string {
	names := make([][]string, 0, len(pages))
	for _, page := range pages {
		names = append(names, parseHelpCommands(page))
	}
	return names
}

func TestPaperEngine_helpPages(t *testing.T) {
	t.Run("Single page without counter", func(t *testing.T) {
		engine := &PaperEngine{helpFunc: pagedHelp(map[string]string{
			"Bukkit": "§e--------- §fHelp: §rBukkit §e--------------------------------\n§6/help: §fShows the help menu\n§6/reload: §fA Mojang provided command.",
		})}
		pages, err := engine.helpPages("Bukkit")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		want := [][]string{{"help", "reload"}}
		if got := commandNames(pages); !reflect.DeepEqual(got, want) {
			t.Errorf("helpPages(Bukkit) commands = %v, want %v", got, want)
		}
	})

	t.Run("Index pages use bare page numbers", func(t *testing.T) {
		engine := &PaperEngine{helpFunc: pagedHelp(map[string]string{
			"":  "§e--------- §fHelp: §rIndex (1/2) §e-----\n§6Aliases: §fLists command aliases",
			"2": "§e--------- §fHelp: §rIndex (2/2) §e-----\n§6Paper: §fAll commands for Paper",
		})}
		pages, err := engine.helpPages("")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(pages) != 2 {
			t.Fatalf("Expected 2 index pages, got %d", len(pages))
		}
		want := []string{"Paper"}
		if got := parseHelpNamespaces(pages[1]); !reflect.DeepEqual(got, want) {
			t.Errorf("second index page namespaces = %v, want %v", got, want)
		}
	})

	t.Run("Namespace pages stay in page order", func(t *testing.T) {
		engine := &PaperEngine{helpFunc: pagedHelp(map[string]string{
			"Paper":   "§e--------- §fHelp: §rPaper (1/2) §e-----\n§6/about: §fGets version",
			"Paper 2": "§e--------- §fHelp: §rPaper (2/2) §e-----\n§6/mspt: §fView server tick times",
		})}
		pages, err := engine.helpPages("Paper")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		want := [][]string{{"about"}, {"mspt"}}
		if got := commandNames(pages); !reflect.DeepEqual(got, want) {
			t.Errorf("helpPages(Paper) commands = %v, want %v", got, want)
		}
	})

	t.Run("Failing later page returns the error", func(t *testing.T) {
		engine := &PaperEngine{helpFunc: pagedHelp(map[string]string{
			"Paper": "§e--------- §fHelp: §rPaper (1/2) §e-----\n§6/about: §fGets version",
		})}
		if _, err := engine.helpPages("Paper"); err == nil {
			t.Error("Expected error for missing second page")
		}
	})
}

func TestPaperEngine_LoadCommands(t *testing.T) {
	t.Run("Collects index, namespaces, and aliases", func(t *testing.T) {
		engine := &PaperEngine{helpFunc: pagedHelp(map[string]string{
			"": `§e--------- §fHelp: §rIndex (1/2) §e-----
§7Use /help [n] to get page n of help.
§6Aliases: §fLists command aliases
§6Bukkit: §fAll commands for Bukkit
§6/about: §fGets the version`,
			"2": `§e--------- §fHelp: §rIndex (2/2) §e-----
§6Paper: §fAll commands for Paper
§6/plugins: §fGets a list of plugins`,
			"Bukkit": `§e--------- §fHelp: §rBukkit §e-----
§6/plugins: §fGets a list of plugins
§6/version: §fGets the version`,
			"Paper": `§e--------- §fHelp: §rPaper (1/2) §e-----
§6/about: §fGets the version`,
			"Paper 2": `§e--------- §fHelp: §rPaper (2/2) §e-----
§6/mspt: §fView server tick times`,
			"Aliases": `§e--------- §fHelp: §rAliases §e-----
§6/pl: §f§eAlias for §f/plugins
§6/ver: §f§eAlias for §f/version
§6/nope: §f§eAlias for §f/missing`,
		})}

		if err := engine.LoadCommands(); err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		want := []*PaperCommand{
			{Name: "about"},
			{Name: "plugins", Aliases: []string{"pl"}},
			{Name: "version", Aliases: []string{"ver"}},
			{Name: "mspt"},
		}
		if !reflect.DeepEqual(engine.Commands, want) {
			t.Errorf("Commands = %+v, want %+v", engine.Commands, want)
		}
	})

	t.Run("Help error", func(t *testing.T) {
		engine := &PaperEngine{
			helpFunc: func(cmd string) (string, error) {
				return "", errors.New("help failed")
			},
		}

		if err := engine.LoadCommands(); err == nil {
			t.Error("Expected Error wasn't returned")
		}
	})
}

func TestPaperEngine_EnsureCommandsLoaded(t *testing.T) {
	engine := &PaperEngine{
		helpFunc: func(cmd string) (string, error) {
			return `§f§6Aliases: §fLists command aliases`, nil
		},
	}

	err := engine.EnsureCommandsLoaded()
	if err != nil {
		t.Fatalf("Error on first load: %v", err)
	}

	engine.Commands = []*PaperCommand{{Name: "manual"}}
	err = engine.EnsureCommandsLoaded()
	if err != nil {
		t.Fatalf("Error on second load: %v", err)
	}
	if len(engine.Commands) != 1 || engine.Commands[0].Name != "manual" {
		t.Errorf("EnsureCommandsLoaded overwrote commands")
	}
}

func TestPaperEngine_GetPredictions(t *testing.T) {
	engine := &PaperEngine{Commands: []*PaperCommand{
		{Name: "version", Aliases: []string{"ver"}},
		{Name: "plugins", Aliases: []string{"pl"}},
		{Name: "about"},
	}}

	cases := []struct {
		input string
		want  []string
	}{
		{"", []string{"about", "pl", "plugins", "ver", "version"}},
		{"p", []string{"pl", "plugins"}},
		{"ver", []string{"ver", "version"}},
		{"zzz", []string{}},
	}
	for _, c := range cases {
		predictions, err := engine.GetPredictions(c.input)
		if err != nil {
			t.Fatalf("GetPredictions(%q) error: %v", c.input, err)
		}
		got := make([]string, 0, len(predictions))
		for _, p := range predictions {
			got = append(got, p.Text)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("GetPredictions(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}
