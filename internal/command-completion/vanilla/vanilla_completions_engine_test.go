package vanilla

import (
	"reflect"
	"strings"
	"testing"
)

// Hilfsfunktion: Teilt den String an Leerzeichen und parst jedes Token
func parseTokens(input string) []VanillaToken {
	var result []VanillaToken
	for raw := range strings.FieldsSeq(input) {
		for _, tok := range ComposeTokens(raw, false, false) {
			if tok != nil {
				result = append(result, *tok) // Zeiger dereferenzieren!
			}
		}
	}
	return result
}

func TestComposeTokens_CleanInputs(t *testing.T) {
	tests := []struct {
		name     string
		input    string // Reiner Token-Input ohne / oder ->
		expected []VanillaToken
	}{
		{
			name:  "Einfaches Argument: <targets>",
			input: "<targets>",
			expected: []VanillaToken{
				{Text: "targets", isOptional: false, isArgument: true},
			},
		},
		{
			name:  "Optionales Argument: [<targets>]",
			input: "[<targets>]",
			expected: []VanillaToken{
				{Text: "targets", isOptional: true, isArgument: true, isExpanded: true},
			},
		},
		{
			name:  "Mehrere getrennte Argumente: <targets> <message>",
			input: "<targets> <message>",
			expected: []VanillaToken{
				{Text: "targets", isOptional: false, isArgument: true},
				{Text: "message", isOptional: false, isArgument: true},
			},
		},
		{
			name:  "Alternativen-Gruppe: (structure|biome|poi)",
			input: "(structure|biome|poi)",
			expected: []VanillaToken{
				{Text: "structure", isOptional: false, isArgument: false},
				{Text: "biome", isOptional: false, isArgument: false},
				{Text: "poi", isOptional: false, isArgument: false},
			},
		},
		{
			name:  "Optionale Alternativen: [destroy|keep|replace|strict]",
			input: "[destroy|keep|replace|strict]",
			expected: []VanillaToken{
				{Text: "destroy", isOptional: true, isArgument: false, isExpanded: true},
				{Text: "keep", isOptional: true, isArgument: false, isExpanded: true},
				{Text: "replace", isOptional: true, isArgument: false, isExpanded: true},
				{Text: "strict", isOptional: true, isArgument: false, isExpanded: true},
			},
		},
		{
			name:  "Verschachtelte Gruppe mit Argument: (<value>|fail|run)",
			input: "(<value>|fail|run)",
			expected: []VanillaToken{
				{Text: "value", isOptional: false, isArgument: true},
				{Text: "fail", isOptional: false, isArgument: false},
				{Text: "run", isOptional: false, isArgument: false},
			},
		},
		{
			name:  "Komplexe Kombination: <center> <spreadDistance> (<respectTeams>|under)",
			input: "<center> <spreadDistance> (<respectTeams>|under)",
			expected: []VanillaToken{
				{Text: "center", isOptional: false, isArgument: true},
				{Text: "spreadDistance", isOptional: false, isArgument: true},
				{Text: "respectTeams", isOptional: false, isArgument: true},
				{Text: "under", isOptional: false, isArgument: false},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTokens(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("\nEingabe:  %s\nGefunden: %+v\nErwartet: %+v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestVanillaEngine_LoadCompletions(t *testing.T) {
	// Ausschnitt aus deinem Original-Helpstring inkl. Aliase, Argumente und Gruppen
	rawHelp := `/reload/kill [<targets>]/locate (structure|biome|poi)/msg <targets> <message>/tell -> msg/w -> msg/return (<value>|fail|run)/setblock <pos> <block> [destroy|keep|replace|strict]`

	engine := &VanillaEngine{}
	commands := engine.loadCommandsFromRawHelp(rawHelp)

	// Helper-Map für den schnellen Zugriff im Test
	cmdMap := make(map[string]*VanillaCommand, len(commands))
	for _, cmd := range commands {
		cmdMap[cmd.Text] = cmd
	}

	t.Run("Einfacher Befehl ohne Argumente (/reload)", func(t *testing.T) {
		cmd, exists := cmdMap["reload"]
		if !exists {
			t.Fatalf("Befehl 'reload' wurde nicht gefunden")
		}
		if len(cmd.Children) != 0 {
			t.Errorf("Erwartet 0 Children für 'reload', got %d", len(cmd.Children))
		}
	})

	t.Run("Optionales Argument (/kill [<targets>])", func(t *testing.T) {
		cmd, exists := cmdMap["kill"]
		if !exists {
			t.Fatalf("Befehl 'kill' wurde nicht gefunden")
		}
		if len(cmd.Children) != 1 {
			t.Fatalf("Erwartet 1 Child für 'kill', got %d", len(cmd.Children))
		}

		targetsTok := cmd.Children[0]
		if targetsTok.Text != "targets" || !targetsTok.isOptional || !targetsTok.isArgument {
			t.Errorf("Unerwarteter Token-Zustand für 'targets': %+v", targetsTok)
		}
	})

	t.Run("Gruppe mit Alternativen (/locate (structure|biome|poi))", func(t *testing.T) {
		cmd, exists := cmdMap["locate"]
		if !exists {
			t.Fatalf("Befehl 'locate' wurde nicht gefunden")
		}
		if len(cmd.Children) != 3 {
			t.Fatalf("Erwartet 3 Children (structure, biome, poi), got %d", len(cmd.Children))
		}

		expected := []string{"structure", "biome", "poi"}
		for i, name := range expected {
			if cmd.Children[i].Text != name {
				t.Errorf("Kind %d: Erwartet Text %q, got %q", i, name, cmd.Children[i].Text)
			}
		}
	})

	t.Run("Mehrere hintereinanderfolgende Argumente (/msg <targets> <message>)", func(t *testing.T) {
		cmd, exists := cmdMap["msg"]
		if !exists {
			t.Fatalf("Befehl 'msg' wurde nicht gefunden")
		}
		if len(cmd.Children) != 1 {
			t.Fatalf("Erwartet 1 erstes Child ('targets'), got %d", len(cmd.Children))
		}

		targetsTok := cmd.Children[0]
		if targetsTok.Text != "targets" {
			t.Errorf("Erwartet 'targets', got %q", targetsTok.Text)
		}

		if len(targetsTok.Children) != 1 {
			t.Fatalf("Erwartet 1 Kind unter 'targets' ('message'), got %d", len(targetsTok.Children))
		}

		msgTok := targetsTok.Children[0]
		if msgTok.Text != "message" || !msgTok.isArgument {
			t.Errorf("Unerwartetes Kind unter 'targets': %+v", msgTok)
		}
	})

	t.Run("Alias-Auflösung (/tell -> msg & /w -> msg)", func(t *testing.T) {
		// 'msg' muss vorhanden sein
		msgCmd, msgExists := cmdMap["msg"]
		if !msgExists {
			t.Fatalf("Zielbefehl 'msg' nicht gefunden")
		}

		// 'tell' muss als eigener Command mit den gleichen Children wie 'msg' existieren
		tellCmd, tellExists := cmdMap["tell"]
		if !tellExists {
			t.Fatalf("Alias 'tell' wurde nicht erzeugt")
		}
		if len(tellCmd.Children) != len(msgCmd.Children) {
			t.Errorf("Alias 'tell' hat nicht die gleiche Anzahl Children wie 'msg'")
		}

		// 'w' muss ebenfalls als Alias existieren
		if _, wExists := cmdMap["w"]; !wExists {
			t.Fatalf("Alias 'w' wurde nicht erzeugt")
		}
	})

	t.Run("Verzweigung mit Argument (/return (<value>|fail|run))", func(t *testing.T) {
		cmd, exists := cmdMap["return"]
		if !exists {
			t.Fatalf("Befehl 'return' wurde nicht gefunden")
		}
		if len(cmd.Children) != 3 {
			t.Fatalf("Erwartet 3 Alternativen unter 'return', got %d", len(cmd.Children))
		}

		valTok := cmd.Children[0]
		if valTok.Text != "value" || !valTok.isArgument {
			t.Errorf("Erwartet Argument-Token 'value', got %+v", valTok)
		}
	})
}

func TestGetPredictions(t *testing.T) {
	engine := &VanillaEngine{
		Commands: []*VanillaCommand{
			{
				Text:    "kill",
				Aliases: []string{},
				Children: []*VanillaToken{
					{Text: "<targets>", isArgument: true},
				},
			},
			{
				Text:    "locate",
				Aliases: []string{},
				Children: []*VanillaToken{
					{Text: "biome"},
					{Text: "poi"},
					{Text: "structure"},
				},
			},
			{
				Text:    "msg",
				Aliases: []string{"tell", "w"},
				Children: []*VanillaToken{
					{
						Text:       "<targets>",
						isArgument: true,
						Children: []*VanillaToken{
							{Text: "<message>", isArgument: true},
						},
					},
				},
			},
			{
				Text:    "teleport",
				Aliases: []string{"tp"},
				Children: []*VanillaToken{
					{Text: "<location>", isArgument: true},
					{Text: "<destination>", isArgument: true},
					{Text: "<targets>", isArgument: true},
				},
			},
			{
				Text:    "setblock",
				Aliases: []string{},
				Children: []*VanillaToken{
					{
						Text:       "<pos_x>",
						isArgument: true,
						Children: []*VanillaToken{
							{
								Text:       "<pos_y>",
								isArgument: true,
								Children: []*VanillaToken{
									{
										Text:       "<pos_z>",
										isArgument: true,
										Children: []*VanillaToken{
											{
												Text:       "<block>",
												isArgument: true,
												Children: []*VanillaToken{
													{Text: "destroy"},
													{Text: "keep"},
													{Text: "replace"},
													{Text: "strict"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Basisbefehl Präfix-Suche 'k'",
			input:    "k",
			expected: []string{"kill"},
		},
		{
			name:     "Alias Präfix-Suche 't'",
			input:    "t",
			expected: []string{"teleport", "tell", "tp"}, // Alphabetisch sortiert
		},
		{
			name:     "Exakter Basisbefehl mit Leerzeichen zeigt Argument-Platzhalter",
			input:    "kill ",
			expected: []string{"<targets>"},
		},
		{
			name:     "Optionen für 'locate '",
			input:    "locate ",
			expected: []string{"biome", "poi", "structure"},
		},
		{
			name:     "Präfix-Filterung für Subbefehl 'locate s'",
			input:    "locate s",
			expected: []string{"structure"},
		},
		{
			name:     "Argument-Traversierung bei 'msg PlayerOne '",
			input:    "msg PlayerOne ",
			expected: []string{"<message>"},
		},
		{
			name:     "Alias-Traversierung mit 'tp '",
			input:    "tp ",
			expected: []string{"<destination>", "<location>", "<targets>"},
		},
		{
			name:     "Tief verschachtelte Optionen 'setblock ~ ~ ~ stone '",
			input:    "setblock ~ ~ ~ stone ",
			expected: []string{"destroy", "keep", "replace", "strict"},
		},
		{
			name:     "Unbekannter Befehl liefert leeres Resultat",
			input:    "unknowncmd ",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predictions, err := engine.GetPredictions(tt.input)
			if err != nil {
				t.Fatalf("Unerwarteter Fehler: %v", err)
			}

			got := make([]string, len(predictions))
			for i, p := range predictions {
				got[i] = p.Text
			}

			if len(got) == 0 && len(tt.expected) == 0 {
				return
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GetPredictions(%q) = %v, erwarte %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetPredictions_WithArgumentMappings(t *testing.T) {
	playerListProvider := PlayerListProviderFunc(func() ([]string, error) {
		return nil, nil
	})
	engine := CreateEmptyVanillaEngine(playerListProvider)
	engine.Commands = []*VanillaCommand{
		{
			Text: "kill",
			Children: []*VanillaToken{
				{Text: "targets", isArgument: true}, // "targets" statt "<targets>"
			},
		},
		{
			Text: "gamemode",
			Children: []*VanillaToken{
				{Text: "gamemode", isArgument: true}, // "gamemode" statt "<gamemode>"
			},
		},
		{
			Text: "summon",
			Children: []*VanillaToken{
				{Text: "entity", isArgument: true}, // "entity" statt "<entity>"
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Gamemode ohne Eingabe -> Werte alphabetisch + Platzhalter am Ende",
			input:    "gamemode ",
			expected: []string{"adventure", "creative", "spectator", "survival", "gamemode"},
		},
		{
			name:     "Gamemode mit Präfix 's' -> Filtert Werte + Platzhalter",
			input:    "gamemode s",
			expected: []string{"spectator", "survival", "gamemode"}, // <gamemode> startet mit '<', nicht 's'
		},
		{
			name:     "Kill Targets mit Präfix '@' -> Gefilterte Targets + Platzhalter am Ende",
			input:    "kill @",
			expected: []string{"@a", "@e", "@n", "@p", "@r", "@s", "targets"},
		},
		{
			name:     "Kill Targets mit Präfix '@p' -> Nur @p (Platzhalter fällt durch Präfix weg)",
			input:    "kill @p",
			expected: []string{"@p", "targets"},
		},
		{
			name:     "Ohne Mapping Fallback -> Nur der Platzhalter selbst",
			input:    "summon ",
			expected: []string{"entity"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predictions, err := engine.GetPredictions(tt.input)
			if err != nil {
				t.Fatalf("Unerwarteter Fehler: %v", err)
			}

			got := make([]string, len(predictions))
			for i, p := range predictions {
				got[i] = p.Text
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GetPredictions(%q) = %v, erwarte %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetPredictions_WithMappingRealPlayers(t *testing.T) {
	playerListProvider := PlayerListProviderFunc(func() ([]string, error) {
		return []string{"timex05"}, nil
	})
	engine := CreateEmptyVanillaEngine(playerListProvider)
	engine.Commands = []*VanillaCommand{
		{
			Text: "kill",
			Children: []*VanillaToken{
				{Text: "targets", isArgument: true}, // "targets" statt "<targets>"
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Kill Targets mit Präfix '@' -> Nur Target-Selektoren mit @ + Platzhalter",
			input:    "kill @",
			expected: []string{"@a", "@e", "@n", "@p", "@r", "@s", "targets"},
		},
		{
			name:     "Kill Targets mit Präfix 'tim' -> Gefilterter Spielername + Platzhalter",
			input:    "kill tim",
			expected: []string{"timex05", "targets"},
		},
		{
			name:     "Kill Targets ohne Präfix -> Alle Spieler, Selektoren + Platzhalter",
			input:    "kill ",
			expected: []string{"@a", "@e", "@n", "@p", "@r", "@s", "timex05", "targets"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predictions, err := engine.GetPredictions(tt.input)
			if err != nil {
				t.Fatalf("Unerwarteter Fehler: %v", err)
			}

			got := make([]string, len(predictions))
			for i, p := range predictions {
				got[i] = p.Text
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GetPredictions(%q) = %v, erwarte %v", tt.input, got, tt.expected)
			}
		})
	}
}

type CommandProviderFunc func(command string) (string, error)

func (f CommandProviderFunc) Execute(command string) (string, error) {
	return f(command)
}

type PlayerListProviderFunc func() ([]string, error)

func (f PlayerListProviderFunc) GetPlayers() ([]string, error) {
	return f()
}

func CreateEmptyVanillaEngine(playerListProvider PlayerListProviderFunc) *VanillaEngine {
	commandProvider := CommandProviderFunc(func(command string) (string, error) {
		return "", nil
	})

	return CreateVanillaEngine(commandProvider, playerListProvider)
}
