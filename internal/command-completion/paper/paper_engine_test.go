package paper

import (
	"errors"
	"testing"
)

type mockCommandProvider struct {
	executeFunc func(cmd string) (string, error)
}

func (m *mockCommandProvider) Execute(cmd string) (string, error) {
	if m.executeFunc != nil {
		return m.executeFunc(cmd)
	}
	return "", nil
}

func TestGreatePaperEngine(t *testing.T) {
	mock := &mockCommandProvider{
		executeFunc: func(cmd string) (string, error) {
			if cmd == "help" {
				return "help output", nil
			}
			if cmd == "help Bukkit" {
				return "bukkit output", nil
			}
			return "", errors.New("unknown command")
		},
	}

	engine := CreatePaperEngine(mock)
	if engine == nil {
		t.Fatal("Erwartet: Engine-Instanz, Got: nil")
	}

	res, err := engine.helpFunc("")
	if err != nil || res != "help output" {
		t.Errorf("helpFunc(\"\") fehlgeschlagen: got %q, err %v", res, err)
	}

	res, err = engine.helpFunc("Bukkit")
	if err != nil || res != "bukkit output" {
		t.Errorf("helpFunc(\"Bukkit\") fehlgeschlagen: got %q, err %v", res, err)
	}
}

func TestParseHelpNamespaces(t *testing.T) {
	input := `§e--------- §fHelp: §rIndex (1/23) §e--------------------------
§7Use /help [n] to get page n of help.
§7§6Aliases: §fLists command aliases
§f§6Bukkit: §fAll commands for Bukkit
§f§6Minecraft: §fAll commands for Minecraft
§f§6Paper: §fAll commands for Paper
§f§6/about: §fGets the version of this server including any plugins in use`

	expected := []string{"Aliases", "Bukkit", "Minecraft", "Paper"}
	results := parseHelpNamespaces(input)

	if len(results) != len(expected) {
		t.Fatalf("Erwartet: %d Namespaces, Got: %d", len(expected), len(results))
	}
	for i, name := range expected {
		if results[i] != name {
			t.Errorf("Index %d: Erwartet %q, Got %q", i, name, results[i])
		}
	}
}

func TestParseHelpCommands(t *testing.T) {
	input := `§e--------- §fHelp: §rPaper (1/3) §e---------------------------
§7Below is a list of all Paper commands:
§7§6/about: §fGets the version of this server including any §fplugins in use
§f§6/bukkit:about: §fGets the version of this server
§f§6/plugins: §fGets a list of plugins`

	expected := []string{"about", "bukkit:about", "plugins"}
	results := parseHelpCommands(input)

	if len(results) != len(expected) {
		t.Fatalf("Erwartet: %d Commands, Got: %d", len(expected), len(results))
	}
	for i, cmd := range expected {
		if results[i] != cmd {
			t.Errorf("Index %d: Erwartet %q, Got %q", i, cmd, results[i])
		}
	}
}

func TestConvertHelpCommandsToPaperCommands(t *testing.T) {
	input := []string{"about", "reload"}
	results := convertHelpCommandsToPaperCommands(input)

	if len(results) != 2 {
		t.Fatalf("Erwartet: 2 PaperCommands, Got: %d", len(results))
	}
	if results[0].Name != "about" || results[1].Name != "reload" {
		t.Errorf("Unerwartete Command-Namen: %+v", results)
	}
}

func TestGetCommandsForNameSpace(t *testing.T) {
	t.Run("Single Page Namespace", func(t *testing.T) {
		engine := &PaperEngine{
			helpFunc: func(cmd string) (string, error) {
				return `§e--------- §fHelp: §rBukkit §e--------------------------------
§7§6/help: §fShows the help menu
§f§6/reload: §fA Mojang provided command.`, nil
			},
		}

		cmds, err := engine.GetCommandsForNameSpace("Bukkit")
		if err != nil {
			t.Fatalf("Unerwarteter Fehler: %v", err)
		}
		if len(cmds) != 2 {
			t.Fatalf("Erwartet: 2 Commands, Got: %d", len(cmds))
		}
		if cmds[0].Name != "help" || cmds[1].Name != "reload" {
			t.Errorf("Falsche Commands ausgelesen: %v, %v", cmds[0].Name, cmds[1].Name)
		}
	})

	t.Run("Multi Page Namespace", func(t *testing.T) {
		engine := &PaperEngine{
			helpFunc: func(cmd string) (string, error) {
				switch cmd {
				case "Aliases":
					return `§e--------- §fHelp: §rPaper (1/2) §e---------------------------
§7§6/about: §fGets version`, nil
				case "Aliases 2":
					return `§e--------- §fHelp: §rPaper (2/2) §e---------------------------
§f§6/mspt: §fView server tick times`, nil
				default:
					return "", errors.New("command not found")
				}
			},
		}

		cmds, err := engine.GetCommandsForNameSpace("Aliases")
		if err != nil {
			t.Fatalf("Unerwarteter Fehler: %v", err)
		}
		if len(cmds) != 2 {
			t.Fatalf("Erwartet: 2 Commands, Got: %d", len(cmds))
		}
		if cmds[0].Name != "mspt" || cmds[1].Name != "about" {
			t.Errorf("Unerwartete Command-Reihenfolge: %v, %v", cmds[0].Name, cmds[1].Name)
		}
	})
}

func TestLoadCommands(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		engine := &PaperEngine{
			helpFunc: func(cmd string) (string, error) {
				if cmd == "" {
					return `§e--------- §fHelp: §rIndex (1/1) §e--------------------------
§f§6Bukkit: §fLists command aliases`, nil
				}
				if cmd == "Bukkit" {
					return `§e--------- §fHelp: §rBukkit §e--------------------------------
§7§6/alias1: §fTest Alias`, nil
				}
				return "", nil
			},
		}

		err := engine.LoadCommands()
		if err != nil {
			t.Fatalf("Unerwarteter Fehler: %v", err)
		}
		if len(engine.Commands) != 1 {
			t.Fatalf("Erwartet: 1 Command, Got: %d", len(engine.Commands))
		}
		if engine.Commands[0].Name != "alias1" {
			t.Errorf("Erwartet: 'alias1', Got: %q", engine.Commands[0].Name)
		}
	})

	t.Run("Help Error", func(t *testing.T) {
		engine := &PaperEngine{
			helpFunc: func(cmd string) (string, error) {
				return "", errors.New("help failed")
			},
		}

		err := engine.LoadCommands()
		if err == nil {
			t.Error("Erwarteter Fehler wurde nicht zurückgegeben")
		}
	})
}

func TestEnsureCommandsLoaded(t *testing.T) {
	engine := &PaperEngine{
		helpFunc: func(cmd string) (string, error) {
			return `§f§6Aliases: §fLists command aliases`, nil
		},
	}

	err := engine.EnsureCommandsLoaded()
	if err != nil {
		t.Fatalf("Fehler beim ersten Laden: %v", err)
	}

	engine.Commands = []*PaperCommand{{Name: "manual"}}
	err = engine.EnsureCommandsLoaded()
	if err != nil {
		t.Fatalf("Fehler beim zweites Laden: %v", err)
	}
	if len(engine.Commands) != 1 || engine.Commands[0].Name != "manual" {
		t.Errorf("EnsureCommandsLoaded hat geladene Befehle überschrieben")
	}
}

func TestGetBaseCommands(t *testing.T) {
	engine := &PaperEngine{
		Commands: []*PaperCommand{
			{Name: "tp", Aliases: []string{"teleport"}},
			{Name: "ban"},
		},
	}

	baseCmds, err := engine.GetBaseCommands()
	if err != nil {
		t.Fatalf("Unerwarteter Fehler: %v", err)
	}

	if len(baseCmds) != 2 {
		t.Fatalf("Erwartet: 2 BaseCommands, Got: %d", len(baseCmds))
	}
	if baseCmds[0].Name != "tp" || len(baseCmds[0].Aliases) != 1 {
		t.Errorf("Erster Command unvollständig: %+v", baseCmds[0])
	}
}
