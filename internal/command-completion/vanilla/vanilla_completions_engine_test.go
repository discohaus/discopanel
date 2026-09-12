package vanilla

import (
	"reflect"
	"strings"
	"testing"
)

// Helper: Splits the string by spaces and parses each token
func parseTokens(input string) []VanillaToken {
	var result []VanillaToken
	for raw := range strings.FieldsSeq(input) {
		for _, tok := range ComposeTokens(raw, false, false) {
			if tok != nil {
				result = append(result, *tok) // Dereference pointer
			}
		}
	}
	return result
}

func TestComposeTokens_CleanInputs(t *testing.T) {
	tests := []struct {
		name     string
		input    string // Pure token input without / or ->
		expected []VanillaToken
	}{
		{
			name:  "Simple argument: <targets>",
			input: "<targets>",
			expected: []VanillaToken{
				{Text: "targets", isOptional: false, isArgument: true},
			},
		},
		{
			name:  "Optional argument: [<targets>]",
			input: "[<targets>]",
			expected: []VanillaToken{
				{Text: "targets", isOptional: true, isArgument: true, isExpanded: false},
			},
		},
		{
			name:  "Multiple separated arguments: <targets> <message>",
			input: "<targets> <message>",
			expected: []VanillaToken{
				{Text: "targets", isOptional: false, isArgument: true},
				{Text: "message", isOptional: false, isArgument: true},
			},
		},
		{
			name:  "Alternatives group: (structure|biome|poi)",
			input: "(structure|biome|poi)",
			expected: []VanillaToken{
				{Text: "structure", isOptional: false, isArgument: false},
				{Text: "biome", isOptional: false, isArgument: false},
				{Text: "poi", isOptional: false, isArgument: false},
			},
		},
		{
			name:  "Optional alternatives: [destroy|keep|replace|strict]",
			input: "[destroy|keep|replace|strict]",
			expected: []VanillaToken{
				{Text: "destroy", isOptional: true, isArgument: false, isExpanded: false},
				{Text: "keep", isOptional: true, isArgument: false, isExpanded: false},
				{Text: "replace", isOptional: true, isArgument: false, isExpanded: false},
				{Text: "strict", isOptional: true, isArgument: false, isExpanded: false},
			},
		},
		{
			name:  "Nested group with argument: (<value>|fail|run)",
			input: "(<value>|fail|run)",
			expected: []VanillaToken{
				{Text: "value", isOptional: false, isArgument: true},
				{Text: "fail", isOptional: false, isArgument: false},
				{Text: "run", isOptional: false, isArgument: false},
			},
		},
		{
			name:  "Complex combination: <center> <spreadDistance> (<respectTeams>|under)",
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
				t.Errorf("\nInput:  %s\nGot: %+v\nExpected: %+v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestVanillaEngine_LoadCompletions(t *testing.T) {
	// Extract from original help string including aliases, arguments, and groups
	rawHelp := `/reload/kill [<targets>]/locate (structure|biome|poi)/msg <targets> <message>/tell -> msg/w -> msg/return (<value>|fail|run)/setblock <pos> <block> [destroy|keep|replace|strict]`

	engine := &VanillaEngine{}
	commands := engine.loadCommandsFromRawHelp(rawHelp)

	// Helper map for fast lookup in test
	cmdMap := make(map[string]*VanillaCommand, len(commands))
	for _, cmd := range commands {
		cmdMap[cmd.Text] = cmd
	}

	t.Run("Simple command without arguments (/reload)", func(t *testing.T) {
		cmd, exists := cmdMap["reload"]
		if !exists {
			t.Fatalf("Command 'reload' was not found")
		}
		if len(cmd.Children) != 0 {
			t.Errorf("Expected 0 children for 'reload', got %d", len(cmd.Children))
		}
	})

	t.Run("Optional argument (/kill [<targets>])", func(t *testing.T) {
		cmd, exists := cmdMap["kill"]
		if !exists {
			t.Fatalf("Command 'kill' was not found")
		}
		if len(cmd.Children) != 1 {
			t.Fatalf("Expected 1 child for 'kill', got %d", len(cmd.Children))
		}

		targetsTok := cmd.Children[0]
		if targetsTok.Text != "targets" || !targetsTok.isOptional || !targetsTok.isArgument {
			t.Errorf("Unexpected token state for 'targets': %+v", targetsTok)
		}
	})

	t.Run("Group with alternatives (/locate (structure|biome|poi))", func(t *testing.T) {
		cmd, exists := cmdMap["locate"]
		if !exists {
			t.Fatalf("Command 'locate' was not found")
		}
		if len(cmd.Children) != 3 {
			t.Fatalf("Expected 3 children (structure, biome, poi), got %d", len(cmd.Children))
		}

		expected := []string{"structure", "biome", "poi"}
		for i, name := range expected {
			if cmd.Children[i].Text != name {
				t.Errorf("Child %d: Expected text %q, got %q", i, name, cmd.Children[i].Text)
			}
		}
	})

	t.Run("Multiple consecutive arguments (/msg <targets> <message>)", func(t *testing.T) {
		cmd, exists := cmdMap["msg"]
		if !exists {
			t.Fatalf("Command 'msg' was not found")
		}
		if len(cmd.Children) != 1 {
			t.Fatalf("Expected 1 first child ('targets'), got %d", len(cmd.Children))
		}

		targetsTok := cmd.Children[0]
		if targetsTok.Text != "targets" {
			t.Errorf("Expected 'targets', got %q", targetsTok.Text)
		}

		if len(targetsTok.Children) != 1 {
			t.Fatalf("Expected 1 child under 'targets' ('message'), got %d", len(targetsTok.Children))
		}

		msgTok := targetsTok.Children[0]
		if msgTok.Text != "message" || !msgTok.isArgument {
			t.Errorf("Unexpected child under 'targets': %+v", msgTok)
		}
	})

	t.Run("Alias resolution (/tell -> msg & /w -> msg)", func(t *testing.T) {
		msgCmd, msgExists := cmdMap["msg"]
		if !msgExists {
			t.Fatalf("Target command 'msg' not found")
		}

		tellCmd, tellExists := cmdMap["tell"]
		if !tellExists {
			t.Fatalf("Alias 'tell' was not created")
		}
		if len(tellCmd.Children) != len(msgCmd.Children) {
			t.Errorf("Alias 'tell' does not have the same number of children as 'msg'")
		}

		if _, wExists := cmdMap["w"]; !wExists {
			t.Fatalf("Alias 'w' was not created")
		}
	})

	t.Run("Branch with argument (/return (<value>|fail|run))", func(t *testing.T) {
		cmd, exists := cmdMap["return"]
		if !exists {
			t.Fatalf("Command 'return' was not found")
		}
		if len(cmd.Children) != 3 {
			t.Fatalf("Expected 3 alternatives under 'return', got %d", len(cmd.Children))
		}

		valTok := cmd.Children[0]
		if valTok.Text != "value" || !valTok.isArgument {
			t.Errorf("Expected argument token 'value', got %+v", valTok)
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
			name:     "Base command prefix search 'k'",
			input:    "k",
			expected: []string{"kill"},
		},
		{
			name:     "Alias prefix search 't'",
			input:    "t",
			expected: []string{"teleport", "tell", "tp"},
		},
		{
			name:     "Exact base command with trailing space shows argument placeholder",
			input:    "kill ",
			expected: []string{"<targets>"},
		},
		{
			name:     "Options for 'locate '",
			input:    "locate ",
			expected: []string{"biome", "poi", "structure"},
		},
		{
			name:     "Prefix filtering for subcommand 'locate s'",
			input:    "locate s",
			expected: []string{"structure"},
		},
		{
			name:     "Argument traversal for 'msg PlayerOne '",
			input:    "msg PlayerOne ",
			expected: []string{"<message>"},
		},
		{
			name:     "Alias traversal with 'tp '",
			input:    "tp ",
			expected: []string{"<destination>", "<location>", "<targets>"},
		},
		{
			name:     "Deeply nested options 'setblock ~ ~ ~ stone '",
			input:    "setblock ~ ~ ~ stone ",
			expected: []string{"destroy", "keep", "replace", "strict"},
		},
		{
			name:     "Unknown command returns empty result",
			input:    "unknowncmd ",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predictions, err := engine.GetPredictions(tt.input)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			got := make([]string, len(predictions))
			for i, p := range predictions {
				got[i] = p.Text
			}

			if len(got) == 0 && len(tt.expected) == 0 {
				return
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GetPredictions(%q) = %v, want %v", tt.input, got, tt.expected)
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
				{Text: "targets", isArgument: true},
			},
		},
		{
			Text: "gamemode",
			Children: []*VanillaToken{
				{Text: "gamemode", isArgument: true},
			},
		},
		{
			Text: "summon",
			Children: []*VanillaToken{
				{Text: "entity", isArgument: true},
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Gamemode without input -> values sorted + placeholder at end",
			input:    "gamemode ",
			expected: []string{"adventure", "creative", "spectator", "survival", "gamemode"},
		},
		{
			name:     "Gamemode with prefix 's' -> filters values + placeholder",
			input:    "gamemode s",
			expected: []string{"spectator", "survival", "gamemode"},
		},
		{
			name:     "Kill targets with prefix '@' -> filtered targets + placeholder at end",
			input:    "kill @",
			expected: []string{"@a", "@e", "@n", "@p", "@r", "@s", "targets"},
		},
		{
			name:     "Kill targets with prefix '@p' -> only @p",
			input:    "kill @p",
			expected: []string{"@p", "targets"},
		},
		{
			name:     "Without mapping fallback -> placeholder only",
			input:    "summon ",
			expected: []string{"entity"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predictions, err := engine.GetPredictions(tt.input)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			got := make([]string, len(predictions))
			for i, p := range predictions {
				got[i] = p.Text
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GetPredictions(%q) = %v, want %v", tt.input, got, tt.expected)
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
				{Text: "targets", isArgument: true},
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Kill targets with prefix '@' -> target selectors with @ + placeholder",
			input:    "kill @",
			expected: []string{"@a", "@e", "@n", "@p", "@r", "@s", "targets"},
		},
		{
			name:     "Kill targets with prefix 'tim' -> filtered player name + placeholder",
			input:    "kill tim",
			expected: []string{"timex05", "targets"},
		},
		{
			name:     "Kill targets without prefix -> all players, selectors + placeholder",
			input:    "kill ",
			expected: []string{"@a", "@e", "@n", "@p", "@r", "@s", "timex05", "targets"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			predictions, err := engine.GetPredictions(tt.input)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			got := make([]string, len(predictions))
			for i, p := range predictions {
				got[i] = p.Text
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GetPredictions(%q) = %v, want %v", tt.input, got, tt.expected)
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

func TestDynamicExpansion_Advancement(t *testing.T) {
	helpResponses := map[string]string{
		"help": "/advancement (grant|revoke)",
		"help advancement grant": "/advancement grant <targets> (only|from|until|through|everything)",
		"help advancement grant targets only": "/advancement grant targets only <advancement> [<criterion>]",
		"help advancement grant targets only advancement criterion": "",
	}

	commandProvider := CommandProviderFunc(func(cmd string) (string, error) {
		if resp, ok := helpResponses[cmd]; ok {
			return resp, nil
		}
		return "", nil
	})

	engine := CreateVanillaEngine(commandProvider, nil)

	// Step 1: Base command expansion "advancement "
	preds1, err := engine.GetPredictions("advancement ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected1 := []string{"grant", "revoke"}
	got1 := make([]string, len(preds1))
	for i, p := range preds1 {
		got1[i] = p.Text
	}
	if !reflect.DeepEqual(got1, expected1) {
		t.Errorf("Step 1: got %v, want %v", got1, expected1)
	}

	// Step 2: Sub-help expansion "advancement grant "
	preds2, err := engine.GetPredictions("advancement grant ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected2 := []string{"@a", "@e", "@n", "@p", "@r", "@s", "targets"}
	got2 := make([]string, len(preds2))
	for i, p := range preds2 {
		got2[i] = p.Text
	}
	if !reflect.DeepEqual(got2, expected2) {
		t.Errorf("Step 2: got %v, want %v", got2, expected2)
	}

	// Step 3: Traversal after target selector "advancement grant @a "
	preds3, err := engine.GetPredictions("advancement grant @a ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected3 := []string{"everything", "from", "only", "through", "until"}
	got3 := make([]string, len(preds3))
	for i, p := range preds3 {
		got3[i] = p.Text
	}
	if !reflect.DeepEqual(got3, expected3) {
		t.Errorf("Step 3: got %v, want %v", got3, expected3)
	}

	// Step 4: Deeper expansion "advancement grant @a only "
	preds4, err := engine.GetPredictions("advancement grant @a only ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected4 := []string{"advancement"}
	got4 := make([]string, len(preds4))
	for i, p := range preds4 {
		got4[i] = p.Text
	}
	if !reflect.DeepEqual(got4, expected4) {
		t.Errorf("Step 4: got %v, want %v", got4, expected4)
	}

	// Step 5: Final argument level "advancement grant @a only minecraft:story/root "
	preds5, err := engine.GetPredictions("advancement grant @a only minecraft:story/root ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected5 := []string{"criterion"}
	got5 := make([]string, len(preds5))
	for i, p := range preds5 {
		got5[i] = p.Text
	}
	if !reflect.DeepEqual(got5, expected5) {
		t.Errorf("Step 5: got %v, want %v", got5, expected5)
	}

	// Step 6: End of command output -> empty predictions
	preds6, err := engine.GetPredictions("advancement grant @a only minecraft:story/root my_criterion ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(preds6) != 0 {
		t.Errorf("Step 6: expected empty predictions, got %v", preds6)
	}
}

func TestDynamicExpansion_ExecuteRedirect(t *testing.T) {
	helpResponses := map[string]string{
		"help": "/execute (run|as)/kill [<targets>]",
		"help execute": "/execute run .../execute as <targets> -> execute",
	}

	commandProvider := CommandProviderFunc(func(cmd string) (string, error) {
		if resp, ok := helpResponses[cmd]; ok {
			return resp, nil
		}
		return "", nil
	})

	engine := CreateVanillaEngine(commandProvider, nil)

	// "execute as @a " should redirect to "execute" subcommands ("as", "run")
	preds, err := engine.GetPredictions("execute as @a ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got := make([]string, len(preds))
	for i, p := range preds {
		got[i] = p.Text
	}

	expected := []string{"as", "run"}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Redirect predictions: got %v, want %v", got, expected)
	}
}

func TestDynamicExpansion_ExecuteWildcard(t *testing.T) {
	helpResponses := map[string]string{
		"help": "/execute (run|as)/kill [<targets>]",
		"help execute": "/execute run .../execute as <targets> -> execute",
	}

	commandProvider := CommandProviderFunc(func(cmd string) (string, error) {
		if resp, ok := helpResponses[cmd]; ok {
			return resp, nil
		}
		return "", nil
	})

	engine := CreateVanillaEngine(commandProvider, nil)

	// 1. "execute run " should offer base commands ("execute", "kill")
	preds1, err := engine.GetPredictions("execute run ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	got1 := make([]string, len(preds1))
	for i, p := range preds1 {
		got1[i] = p.Text
	}
	expected1 := []string{"execute", "kill"}
	if !reflect.DeepEqual(got1, expected1) {
		t.Errorf("Wildcard base commands: got %v, want %v", got1, expected1)
	}

	// 2. "execute run kill " should delegate to "kill " subcommands
	preds2, err := engine.GetPredictions("execute run kill ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	got2 := make([]string, len(preds2))
	for i, p := range preds2 {
		got2[i] = p.Text
	}
	expected2 := []string{"@a", "@e", "@n", "@p", "@r", "@s", "targets"}
	if !reflect.DeepEqual(got2, expected2) {
		t.Errorf("Wildcard delegated predictions: got %v, want %v", got2, expected2)
	}
}

func TestDynamicExpansion_Clear(t *testing.T) {
	helpResponses := map[string]string{
		"help":                    "/clear [<targets>] [<item>]",
		"help clear":              "/clear [<targets>] [<item>]",
		"help clear targets":      "/clear targets [<item>] [<maxCount>]",
		"help clear targets item": "/clear targets item [<maxCount>]",
	}

	commandProvider := CommandProviderFunc(func(cmd string) (string, error) {
		if resp, ok := helpResponses[cmd]; ok {
			return resp, nil
		}
		return "", nil
	})

	engine := CreateVanillaEngine(commandProvider, nil)

	// Step 1: "clear " -> targets
	preds1, err := engine.GetPredictions("clear ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected1 := []string{"@a", "@e", "@n", "@p", "@r", "@s", "targets"}
	got1 := make([]string, len(preds1))
	for i, p := range preds1 {
		got1[i] = p.Text
	}
	if !reflect.DeepEqual(got1, expected1) {
		t.Errorf("Step 1: got %v, want %v", got1, expected1)
	}

	// Step 2: "clear @a " -> item (and maxCount after sub-help expansion)
	preds2, err := engine.GetPredictions("clear @a ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected2 := []string{"item"}
	got2 := make([]string, len(preds2))
	for i, p := range preds2 {
		got2[i] = p.Text
	}
	if !reflect.DeepEqual(got2, expected2) {
		t.Errorf("Step 2: got %v, want %v", got2, expected2)
	}

	// Step 3: "clear @a diamond " -> maxCount (from sub-help expansion of clear targets item)
	preds3, err := engine.GetPredictions("clear @a diamond ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	expected3 := []string{"maxCount"}
	got3 := make([]string, len(preds3))
	for i, p := range preds3 {
		got3[i] = p.Text
	}
	if !reflect.DeepEqual(got3, expected3) {
		t.Errorf("Step 3: got %v, want %v", got3, expected3)
	}
}

func TestDynamicExpansion_Fill(t *testing.T) {
	helpResponses := map[string]string{
		"help":                            "/fill <from> <to> <block> [outline|hollow|destroy|strict|replace|keep]",
		"help fill":                       "/fill <from> <to> <block> [outline|hollow|destroy|strict|replace|keep]",
		"help fill from to block":         "/fill from to block <from> <to> <block> [outline|hollow|destroy|strict|replace|keep]",
		"help fill from to block replace": "/fill from to block replace <from> <to> <block> [outline|hollow|destroy|strict|replace|keep]",
	}

	commandProvider := CommandProviderFunc(func(cmd string) (string, error) {
		if resp, ok := helpResponses[cmd]; ok {
			return resp, nil
		}
		return "", nil
	})

	engine := CreateVanillaEngine(commandProvider, nil)

	// "fill from to block " should offer optional modes (destroy, hollow, keep, outline, replace, strict)
	preds, err := engine.GetPredictions("fill from to block ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got := make([]string, len(preds))
	for i, p := range preds {
		got[i] = p.Text
	}

	expected := []string{"destroy", "hollow", "keep", "outline", "replace", "strict"}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Fill optional modes: got %v, want %v", got, expected)
	}

	// "fill from to block replace " should not repeat optional modes due to help fallback
	predsReplace, err := engine.GetPredictions("fill from to block replace ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(predsReplace) != 0 {
		gotReplace := make([]string, len(predsReplace))
		for i, p := range predsReplace {
			gotReplace[i] = p.Text
		}
		t.Errorf("Fill replace predictions: got %v, want empty", gotReplace)
	}
}

func TestDynamicExpansion_ExecuteIn(t *testing.T) {
	helpResponses := map[string]string{
		"help":            "/execute (run|in|as)/kill [<targets>]",
		"help execute":    "/execute run .../execute in <dimension> -> execute/execute as <targets> -> execute",
		"help execute in": "/execute in <dimension> -> execute",
	}

	commandProvider := CommandProviderFunc(func(cmd string) (string, error) {
		if resp, ok := helpResponses[cmd]; ok {
			return resp, nil
		}
		return "", nil
	})

	engine := CreateVanillaEngine(commandProvider, nil)

	// Step 1: "execute in " should return dimension suggestions (not execute subcommands!)
	preds1, err := engine.GetPredictions("execute in ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got1 := make([]string, len(preds1))
	for i, p := range preds1 {
		got1[i] = p.Text
	}

	expected1 := []string{"minecraft:overworld", "minecraft:the_end", "minecraft:the_nether", "dimension"}
	if !reflect.DeepEqual(got1, expected1) {
		t.Errorf("Step 1: got %v, want %v", got1, expected1)
	}

	// Step 2: "execute in minecraft:overworld " should redirect to execute subcommands ("as", "in", "run")
	preds2, err := engine.GetPredictions("execute in minecraft:overworld ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got2 := make([]string, len(preds2))
	for i, p := range preds2 {
		got2[i] = p.Text
	}

	expected2 := []string{"as", "in", "run"}
	if !reflect.DeepEqual(got2, expected2) {
		t.Errorf("Step 2: got %v, want %v", got2, expected2)
	}
}

func TestDynamicExpansion_RconPromptPrefix(t *testing.T) {
	helpResponses := map[string]string{
		"help":                "RCON@localhost> /bossbar (add|set)",
		"help bossbar":        "RCON@localhost> /bossbar set <id> (name|color)",
		"help bossbar set id": "RCON@localhost> /bossbar set id name <name>/bossbar set id color (pink|blue|red)",
	}

	commandProvider := CommandProviderFunc(func(cmd string) (string, error) {
		if resp, ok := helpResponses[cmd]; ok {
			return resp, nil
		}
		return "", nil
	})

	engine := CreateVanillaEngine(commandProvider, nil)

	preds, err := engine.GetPredictions("bossbar set my_bossbar ")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got := make([]string, len(preds))
	for i, p := range preds {
		got[i] = p.Text
	}

	expected := []string{"color", "name"}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("RCON prompt predictions: got %v, want %v", got, expected)
	}
}
