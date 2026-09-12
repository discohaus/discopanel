package vanilla

import (
	"slices"
	"sort"
	"strings"

	"github.com/discohaus/discopanel/internal/command-completion/engine"
	"github.com/discohaus/discopanel/pkg/utils"
)

type VanillaToken struct {
	Children   []*VanillaToken
	Text       string
	isArgument bool
	isOptional bool
	isExpanded bool
}

type VanillaCommand struct {
	Text     string
	Children []*VanillaToken
	Aliases  []string
}

type VanillaEngine struct {
	Commands         []*VanillaCommand
	helpFunc         func(command string) (string, error)
	argumentMappings map[string]func() []string
}

func CreateVanillaEngine(commandProvider engine.CommandProvider, playerListProvider engine.PlayListProvider) *VanillaEngine {
	helpFunc := func(command string) (string, error) {
		if command == "" {
			return commandProvider.Execute("help")
		}
		return commandProvider.Execute(strings.Join([]string{"help", command}, " "))
	}
	return &VanillaEngine{
		helpFunc:         helpFunc,
		argumentMappings: GetMappings(playerListProvider),
	}
}

func GetMappings(playerListProvider engine.PlayListProvider) map[string]func() []string {
	return map[string]func() []string{
		"gamemode": func() []string {
			return []string{"adventure", "survival", "creative", "spectator"}
		},
		"targets": func() []string {
			var targets []string

			if playerListProvider != nil {
				players, err := playerListProvider.GetPlayers()
				if err == nil && len(players) > 0 {
					targets = players
				}
			}
			targets = append(targets, []string{"@a", "@e", "@n", "@s", "@p", "@r"}...)

			return targets
		},
		"target": func() []string {
			var targets []string

			if playerListProvider != nil {
				players, err := playerListProvider.GetPlayers()
				if err == nil && len(players) > 0 {
					targets = players
				}
			}
			targets = append(targets, []string{"@a", "@e", "@s", "@p", "@r"}...)

			return targets
		},
	}
}

func (e *VanillaEngine) EnsureCommandsLoaded() error {
	if len(e.Commands) == 0 {
		return e.LoadCommands()
	}
	return nil
}

func (e *VanillaEngine) LoadCommands() error {
	rawHelp, err := e.helpFunc("")
	if err != nil {
		return err
	}
	e.Commands = e.loadCommandsFromRawHelp(rawHelp)
	return nil
}

func (e *VanillaEngine) GetBaseCommands() ([]*engine.BaseCommand, error) {
	err := e.EnsureCommandsLoaded()
	if err != nil {
		return nil, err
	}

	commands := make([]*engine.BaseCommand, 0, len(e.Commands))
	for _, cmd := range e.Commands {
		commands = append(commands, &engine.BaseCommand{
			Name:        cmd.Text,
			Description: nil,
			Aliases:     cmd.Aliases,
		})
	}
	return commands, nil
}

func (e *VanillaEngine) GetPredictions(command string) ([]*engine.Token, error) {
	err := e.EnsureCommandsLoaded()
	if err != nil {
		return nil, err
	}

	// Behält Trailing Spaces bei ("advancement " -> ["advancement", ""])
	tokens := strings.Split(command, " ")
	firstToken := tokens[0]
	remainingTokens := tokens[1:]

	// 1. Fall: Nur das erste Token wird eingegeben (Basisbefehl-Vorschläge)
	if len(remainingTokens) == 0 {
		predictions := make([]*engine.Token, 0)
		for _, cmd := range e.Commands {
			if strings.HasPrefix(cmd.Text, firstToken) {
				predictions = append(predictions, &engine.Token{Text: cmd.Text})
			}
			for _, alias := range cmd.Aliases {
				if strings.HasPrefix(alias, firstToken) {
					predictions = append(predictions, &engine.Token{Text: alias})
				}
			}
		}
		sort.Slice(predictions, func(i, j int) bool {
			return predictions[i].Text < predictions[j].Text
		})
		return predictions, nil
	}

	// 2. Finde den exakt passenden Basisbefehl
	var targetCmd *VanillaCommand
	for _, cmd := range e.Commands {
		if cmd.Text == firstToken {
			targetCmd = cmd
			break
		}
		if slices.Contains(cmd.Aliases, firstToken) {
			targetCmd = cmd
		}
		if targetCmd != nil {
			break
		}
	}

	// Wenn der Basisbefehl nicht existiert, gibt es keine Unter-Vorschläge
	if targetCmd == nil {
		return []*engine.Token{}, nil
	}

	// 3. Traversiere den Parameter-Baum (Children)
	currentNodes := targetCmd.Children

	for i, token := range remainingTokens {
		isLastToken := i == len(remainingTokens)-1

		if isLastToken {
			predictions := make([]*engine.Token, 0)

			for _, node := range currentNodes {
				if node.isArgument {
					hasMappedMatches := false

					// 1. Statische Mappings hinzufügen
					if mappingFunc, exists := e.argumentMappings[node.Text]; exists {
						for _, val := range mappingFunc() {
							if strings.HasPrefix(val, token) {
								hasMappedMatches = true
								predictions = append(predictions, &engine.Token{
									Text:       val,
									IsArgument: true,
									IsOptional: node.isOptional,
									IsStatic:   true,
								})
							}
						}
					}

					// 2. Platzhalter (z. B. "targets") als Option mitgeben
					if token == "" || strings.HasPrefix(node.Text, token) || hasMappedMatches {
						predictions = append(predictions, &engine.Token{
							Text:       node.Text,
							IsArgument: node.isArgument,
							IsOptional: node.isOptional,
							IsStatic:   false,
						})
					}
				} else if strings.HasPrefix(node.Text, token) {
					// Subbefehle
					predictions = append(predictions, &engine.Token{
						Text:       node.Text,
						IsArgument: node.isArgument,
						IsOptional: node.isOptional,
					})
				}
			}

			// Sortierung: Static -> Normale Werte / Platzhalter -> Optional
			sort.Slice(predictions, func(i, j int) bool {
				iIsStatic := predictions[i].IsStatic
				jIsStatic := predictions[j].IsStatic
				iIsArgument := predictions[i].IsArgument
				jIsArgument := predictions[j].IsArgument

				if iIsStatic != jIsStatic {
					return iIsStatic && !jIsStatic
				}
				if iIsArgument != jIsArgument {
					return !iIsArgument && jIsArgument
				}
				return predictions[i].Text < predictions[j].Text
			})

			return predictions, nil
		}

		// Noch nicht beim letzten Token -> Navigiere eine Ebene tiefer
		var nextNodes []*VanillaToken
		for _, node := range currentNodes {
			// Passt, wenn der Text übereinstimmt ODER wenn es ein beliebiges Argument (<...>) akzeptiert
			if node.isArgument || node.Text == token {
				nextNodes = append(nextNodes, node.Children...)
			}
		}
		currentNodes = nextNodes

		// Sackgasse im Baum erreicht
		if len(currentNodes) == 0 {
			return []*engine.Token{}, nil
		}
	}

	return []*engine.Token{}, nil
}

func (e *VanillaEngine) loadCommandsFromRawHelp(rawHelpString string) []*VanillaCommand {
	normalizedRawHelp := strings.ReplaceAll(strings.TrimSpace(utils.StripMinecraftColors(rawHelpString)), "\n", "")
	rawCommands := strings.Split(normalizedRawHelp, "/")
	commands := make([]*VanillaCommand, 0, len(rawCommands))

	// Map für Aliase: AliasName -> ZielName (z.B. "tell" -> "msg")
	aliasesMap := make(map[string]string)

	for _, rawCommand := range rawCommands {
		normalizedRawCommand := strings.TrimSpace(rawCommand)
		if normalizedRawCommand == "" {
			continue
		}

		rawTokens := strings.Fields(normalizedRawCommand) // Handles multiple spaces automatically
		if len(rawTokens) == 0 {
			continue
		}

		// Alias-Sonderfall sicher prüfen (z.B. ["tell", "->", "msg"])
		if len(rawTokens) >= 3 && rawTokens[1] == "->" {
			aliasesMap[rawTokens[0]] = rawTokens[2]
			continue
		}

		command := &VanillaCommand{
			Text:     rawTokens[0],
			Children: make([]*VanillaToken, 0),
		}

		// Baumstruktur für Argumente aufbauen
		for index := 1; index < len(rawTokens); index++ {
			rawToken := rawTokens[index]
			tokens := ComposeTokens(rawToken, false, false)

			if index == 1 {
				command.Children = append(command.Children, tokens...)
			} else {
				// Blätter der untersten Ebene ermitteln
				leafs := getLeafTokens(command.Children)
				for _, leaf := range leafs {
					// Wichtig: Kopien/neue Instanzen anfügen, falls "tokens" mehrfach verwendet wird
					leaf.Children = append(leaf.Children, tokens...)
				}
			}
		}

		// WICHTIG: Befehl zum Array hinzufügen!
		commands = append(commands, command)
	}

	// Aliase verarbeiten (Kopieren des Ziel-Befehls unter dem Alias-Namen)
	if len(aliasesMap) > 0 {
		cmdLookup := make(map[string]*VanillaCommand)
		for _, cmd := range commands {
			cmdLookup[cmd.Text] = cmd
		}

		for alias, target := range aliasesMap {
			if targetCmd, exists := cmdLookup[target]; exists {
				aliasCmd := &VanillaCommand{
					Text:     alias,
					Children: targetCmd.Children, // teilt sich die Argument-Struktur
				}
				commands = append(commands, aliasCmd)
			}
		}
	}

	return commands
}

// Hilfsfunktion: Findet alle tiefsten Tokens im aktuellen Baum
func getLeafTokens(tokens []*VanillaToken) []*VanillaToken {
	var leafs []*VanillaToken
	for _, token := range tokens {
		if len(token.Children) == 0 {
			leafs = append(leafs, token)
		} else {
			leafs = append(leafs, getLeafTokens(token.Children)...)
		}
	}
	return leafs
}

func ComposeTokens(rawToken string, isOptional bool, isArgument bool) []*VanillaToken {
	// switch on first character
	tokens := make([]*VanillaToken, 0, 1)
	switch rawToken[0] {
	case '(':
		cleaned := rawToken[1 : len(rawToken)-1]
		splittet := strings.Split(cleaned, "|")
		for _, rawSubToken := range splittet {
			tokens = append(tokens, ComposeTokens(rawSubToken, isOptional, isArgument)...)
		}
	case '[':
		cleaned := rawToken[1 : len(rawToken)-1]
		if strings.Contains(cleaned, "|") {
			splittet := strings.SplitSeq(cleaned, "|")
			for rawSubToken := range splittet {
				tokens = append(tokens, ComposeTokens(rawSubToken, true, isArgument)...)
			}
		} else {
			tokens = append(tokens, ComposeTokens(cleaned, true, isArgument)...)
		}
	case '<':
		cleaned := rawToken[1 : len(rawToken)-1]
		if strings.Contains(cleaned, "|") {
			splittet := strings.SplitSeq(cleaned, "|")
			for rawSubToken := range splittet {
				tokens = append(tokens, ComposeTokens(rawSubToken, isOptional, true)...)
			}
		} else {
			tokens = append(tokens, ComposeTokens(cleaned, isOptional, true)...)
		}
	default:
		token := &VanillaToken{
			Text:       rawToken,
			isOptional: isOptional,
			isArgument: isArgument,
			isExpanded: isOptional,
		}
		return []*VanillaToken{token}
	}
	return tokens
}
