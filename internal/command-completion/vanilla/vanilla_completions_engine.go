package vanilla

import (
	"slices"
	"sort"
	"strings"

	"github.com/discohaus/discopanel/internal/command-completion/engine"
	"github.com/discohaus/discopanel/pkg/utils"
)

type VanillaToken struct {
	Children       []*VanillaToken
	Text           string
	isArgument     bool
	isOptional     bool
	isExpanded     bool
	redirectTarget string
	isWildcard     bool
}

type VanillaCommand struct {
	Text     string
	Children []*VanillaToken
	Aliases  []string
}

type VanillaEngine struct {
	Commands         []*VanillaCommand
	helpFunc         func(command string) (string, error)
	argumentMappings map[string]func() []engine.MappedValue
	expandedPaths    map[string]bool
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
		expandedPaths:    make(map[string]bool),
	}
}

func GetMappings(playerListProvider engine.PlayListProvider) map[string]func() []engine.MappedValue {
	return map[string]func() []engine.MappedValue{
		"gamemode": func() []engine.MappedValue {
			return []engine.MappedValue{
				{Text: "adventure"}, {Text: "survival"}, {Text: "creative"}, {Text: "spectator"},
			}
		},
		"targets": func() []engine.MappedValue {
			var targets []engine.MappedValue

			if playerListProvider != nil {
				players, err := playerListProvider.GetPlayers()
				if err == nil && len(players) > 0 {
					for _, p := range players {
						targets = append(targets, engine.MappedValue{Text: p, IsPlayer: true})
					}
				}
			}
			for _, sel := range []string{"@a", "@e", "@n", "@s", "@p", "@r"} {
				targets = append(targets, engine.MappedValue{Text: sel, IsPlayer: false})
			}

			return targets
		},
		"target": func() []engine.MappedValue {
			var targets []engine.MappedValue

			if playerListProvider != nil {
				players, err := playerListProvider.GetPlayers()
				if err == nil && len(players) > 0 {
					for _, p := range players {
						targets = append(targets, engine.MappedValue{Text: p, IsPlayer: true})
					}
				}
			}
			for _, sel := range []string{"@a", "@e", "@s", "@p", "@r"} {
				targets = append(targets, engine.MappedValue{Text: sel, IsPlayer: false})
			}

			return targets
		},
		"dimension": func() []engine.MappedValue {
			return []engine.MappedValue{
				{Text: "minecraft:overworld"}, {Text: "minecraft:the_nether"}, {Text: "minecraft:the_end"},
			}
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

func (e *VanillaEngine) expandPath(path []string) {
	if e.expandedPaths == nil {
		e.expandedPaths = make(map[string]bool)
	}
	pathKey := strings.Join(path, " ")
	if pathKey == "" || e.expandedPaths[pathKey] {
		return
	}

	e.expandedPaths[pathKey] = true
	if e.helpFunc == nil {
		return
	}

	rawHelp, err := e.helpFunc(pathKey)
	if err != nil || strings.TrimSpace(rawHelp) == "" {
		return
	}

	e.Commands = e.loadCommandsFromRawHelpWithQuery(rawHelp, path)
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

	tokens := strings.Split(command, " ")
	firstToken := tokens[0]
	remainingTokens := tokens[1:]

	// 1. Base command suggestions
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

	// 2. Find matching base command
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

	if targetCmd == nil {
		return []*engine.Token{}, nil
	}

	if !e.expandedPaths[targetCmd.Text] {
		e.expandPath([]string{targetCmd.Text})
	}

	currentNodes := targetCmd.Children
	path := []string{targetCmd.Text}

	for i, token := range remainingTokens {
		isLastToken := i == len(remainingTokens)-1

		// Check for wildcard (...)
		hasWildcard := false
		for _, node := range currentNodes {
			if node.isWildcard || node.Text == "..." {
				hasWildcard = true
				break
			}
		}

		if hasWildcard {
			if isLastToken {
				baseCmds, err := e.GetBaseCommands()
				if err != nil {
					return nil, err
				}
				predictions := make([]*engine.Token, 0)
				for _, baseCmd := range baseCmds {
					if strings.HasPrefix(baseCmd.Name, token) {
						predictions = append(predictions, &engine.Token{Text: baseCmd.Name})
					}
				}
				sort.Slice(predictions, func(i, j int) bool {
					return predictions[i].Text < predictions[j].Text
				})
				return predictions, nil
			} else {
				// Delegate remaining command tokens
				subCommand := strings.Join(remainingTokens[i:], " ")
				return e.GetPredictions(subCommand)
			}
		}

		if len(currentNodes) == 0 {
			e.expandPath(path)
			currentNodes = e.getNodesAtPath(path)
		}

		if isLastToken {
			if len(currentNodes) == 0 {
				e.expandPath(path)
				currentNodes = e.getNodesAtPath(path)
			}

			predictions := make([]*engine.Token, 0)
			effectiveNodes := currentNodes

			for _, node := range effectiveNodes {
				if node.isArgument {
					hasMappedMatches := false

					if mappingFunc, exists := e.argumentMappings[node.Text]; exists {
						for _, mv := range mappingFunc() {
							if strings.HasPrefix(mv.Text, token) {
								hasMappedMatches = true
								predictions = append(predictions, &engine.Token{
									Text:       mv.Text,
									IsArgument: true,
									IsOptional: node.isOptional,
									IsStatic:   true,
									IsPlayer:   mv.IsPlayer,
								})
							}
						}
					}

					if token == "" || strings.HasPrefix(node.Text, token) || hasMappedMatches {
						predictions = append(predictions, &engine.Token{
							Text:       node.Text,
							IsArgument: node.isArgument,
							IsOptional: node.isOptional,
							IsStatic:   false,
						})
					}
				} else if strings.HasPrefix(node.Text, token) {
					predictions = append(predictions, &engine.Token{
						Text:       node.Text,
						IsArgument: node.isArgument,
						IsOptional: node.isOptional,
					})
				}
			}

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

		var nextNodes []*VanillaToken
		var matchedNode *VanillaToken

		for _, node := range currentNodes {
			if node.redirectTarget != "" {
				var redirCmd *VanillaCommand
				for _, cmd := range e.Commands {
					if cmd.Text == node.redirectTarget {
						redirCmd = cmd
						break
					}
				}
				if redirCmd != nil {
					nextNodes = append(nextNodes, redirCmd.Children...)
					matchedNode = node
				}
			} else if node.isArgument || node.Text == token {
				nextNodes = append(nextNodes, node.Children...)
				if matchedNode == nil || node.Text == token {
					matchedNode = node
				}
			}
		}

		if matchedNode != nil {
			path = append(path, matchedNode.Text)
		} else {
			path = append(path, token)
		}

		currentNodes = nextNodes

		if len(currentNodes) == 0 {
			e.expandPath(path)
			currentNodes = e.getNodesAtPath(path)
		}
	}

	return []*engine.Token{}, nil
}

func (e *VanillaEngine) getNodesAtPath(path []string) []*VanillaToken {
	if len(path) == 0 {
		return nil
	}

	var targetCmd *VanillaCommand
	for _, cmd := range e.Commands {
		if cmd.Text == path[0] || slices.Contains(cmd.Aliases, path[0]) {
			targetCmd = cmd
			break
		}
	}

	if targetCmd == nil {
		return nil
	}

	currentNodes := targetCmd.Children
	for idx := 1; idx < len(path); idx++ {
		step := path[idx]
		var nextNodes []*VanillaToken
		for _, node := range currentNodes {
			if node.redirectTarget != "" {
				var redirCmd *VanillaCommand
				for _, c := range e.Commands {
					if c.Text == node.redirectTarget {
						redirCmd = c
						break
					}
				}
				if redirCmd != nil {
					nextNodes = append(nextNodes, redirCmd.Children...)
				}
			} else if node.Text == step || node.isArgument {
				nextNodes = append(nextNodes, node.Children...)
			}
		}
		currentNodes = nextNodes
		if len(currentNodes) == 0 {
			break
		}
	}
	return currentNodes
}

func cleanHelpOutput(rawHelpString string) string {
	lines := strings.Split(rawHelpString, "\n")
	var cleanedLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if idx := strings.Index(trimmed, "> "); idx != -1 && strings.HasPrefix(trimmed, "RCON@") {
			trimmed = strings.TrimSpace(trimmed[idx+2:])
		}
		if trimmed != "" {
			cleanedLines = append(cleanedLines, trimmed)
		}
	}
	joined := strings.Join(cleanedLines, "")
	return strings.TrimSpace(utils.StripMinecraftColors(joined))
}

func cleanTokenText(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	for len(raw) > 1 && ((raw[0] == '<' && raw[len(raw)-1] == '>') || (raw[0] == '[' && raw[len(raw)-1] == ']') || (raw[0] == '(' && raw[len(raw)-1] == ')')) {
		raw = raw[1 : len(raw)-1]
	}
	return raw
}

func (e *VanillaEngine) loadCommandsFromRawHelp(rawHelpString string) []*VanillaCommand {
	return e.loadCommandsFromRawHelpWithQuery(rawHelpString, nil)
}

func (e *VanillaEngine) loadCommandsFromRawHelpWithQuery(rawHelpString string, queryPath []string) []*VanillaCommand {
	cleaned := cleanHelpOutput(rawHelpString)
	rawCommands := strings.Split(cleaned, "/")

	commands := e.Commands
	if commands == nil {
		commands = make([]*VanillaCommand, 0)
	}

	aliasesMap := make(map[string]string)

	for _, rawCommand := range rawCommands {
		normalizedRawCommand := strings.TrimSpace(rawCommand)
		if normalizedRawCommand == "" {
			continue
		}

		rawTokens := strings.Fields(normalizedRawCommand)
		if len(rawTokens) == 0 {
			continue
		}

		if len(rawTokens) >= 3 && rawTokens[1] == "->" {
			aliasesMap[rawTokens[0]] = rawTokens[2]
			continue
		}

		redirectTarget := ""
		if len(rawTokens) >= 4 && rawTokens[len(rawTokens)-2] == "->" {
			redirectTarget = rawTokens[len(rawTokens)-1]
			rawTokens = rawTokens[:len(rawTokens)-2]
		}

		// If a queryPath is provided (e.g. ["fill", "from", "to", "block"]),
		// verify if rawTokens start with queryPath and strip redundant schema parameter prefixes.
		if len(queryPath) > 0 && len(rawTokens) >= len(queryPath) {
			matchesQuery := true
			for qIdx := 0; qIdx < len(queryPath); qIdx++ {
				if cleanTokenText(rawTokens[qIdx]) != queryPath[qIdx] {
					matchesQuery = false
					break
				}
			}

			if matchesQuery {
				remaining := rawTokens[len(queryPath):]
				// Remove leading tokens in remaining that repeat query path arguments (e.g. <from> <to> <block>)
				for len(remaining) > 0 {
					tokClean := cleanTokenText(remaining[0])
					matchedQueryArg := false
					for _, qArg := range queryPath[1:] {
						if tokClean == qArg {
							matchedQueryArg = true
							break
						}
					}
					if matchedQueryArg {
						remaining = remaining[1:]
					} else {
						break
					}
				}
				// Check if remaining tokens repeat the last query token (indicating fallback help output)
				if len(remaining) > 0 {
					lastQueryToken := queryPath[len(queryPath)-1]
					firstRemTokens := ComposeTokens(remaining[0], false, false)
					isFallback := false
					for _, t := range firstRemTokens {
						if t.Text == lastQueryToken {
							isFallback = true
							break
						}
					}
					if isFallback {
						remaining = nil
					}
				}

				// Reassemble rawTokens from queryPath + remaining tokens
				rawTokens = append(slices.Clone(queryPath), remaining...)
			}
		}

		baseCmdText := rawTokens[0]
		var cmd *VanillaCommand
		for _, existingCmd := range commands {
			if existingCmd.Text == baseCmdText {
				cmd = existingCmd
				break
			}
		}

		if cmd == nil {
			cmd = &VanillaCommand{
				Text:     baseCmdText,
				Children: make([]*VanillaToken, 0),
			}
			commands = append(commands, cmd)
		}

		if len(rawTokens) == 1 {
			continue
		}

		currentLevelNodes := []*VanillaToken(nil)

		for index := 1; index < len(rawTokens); index++ {
			rawToken := rawTokens[index]
			isWildcard := (rawToken == "...")
			tokens := ComposeTokens(rawToken, false, false)

			for _, tok := range tokens {
				if isWildcard {
					tok.isWildcard = true
				}
				if index == len(rawTokens)-1 && redirectTarget != "" {
					tok.redirectTarget = redirectTarget
				}
			}

			if index == 1 {
				cmd.Children = mergeTokenList(cmd.Children, tokens)
				currentLevelNodes = findMatchingTokens(cmd.Children, tokens)
			} else {
				var nextLevel []*VanillaToken
				for _, parent := range currentLevelNodes {
					parent.Children = mergeTokenList(parent.Children, tokens)
					nextLevel = append(nextLevel, findMatchingTokens(parent.Children, tokens)...)
				}
				currentLevelNodes = nextLevel
			}
		}
	}

	if len(aliasesMap) > 0 {
		cmdLookup := make(map[string]*VanillaCommand)
		for _, c := range commands {
			cmdLookup[c.Text] = c
		}

		for alias, target := range aliasesMap {
			if targetCmd, exists := cmdLookup[target]; exists {
				var aliasCmd *VanillaCommand
				for _, c := range commands {
					if c.Text == alias {
						aliasCmd = c
						break
					}
				}
				if aliasCmd == nil {
					aliasCmd = &VanillaCommand{
						Text:     alias,
						Children: targetCmd.Children,
					}
					commands = append(commands, aliasCmd)
				} else {
					aliasCmd.Children = targetCmd.Children
				}
			}
		}
	}

	return commands
}

func mergeTokenList(existing []*VanillaToken, incoming []*VanillaToken) []*VanillaToken {
	result := existing
	for _, inc := range incoming {
		var matched *VanillaToken
		for _, ext := range result {
			if ext.Text == inc.Text {
				matched = ext
				break
			}
		}
		if matched != nil {
			if inc.redirectTarget != "" {
				matched.redirectTarget = inc.redirectTarget
			}
			if inc.isWildcard {
				matched.isWildcard = true
			}
		} else {
			result = append(result, inc)
		}
	}
	return result
}

func findMatchingTokens(haystack []*VanillaToken, query []*VanillaToken) []*VanillaToken {
	var matches []*VanillaToken
	for _, q := range query {
		for _, h := range haystack {
			if h.Text == q.Text {
				matches = append(matches, h)
			}
		}
	}
	return matches
}

// Helper: Finds all deepest tokens in the current tree
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
			isExpanded: false,
		}
		return []*VanillaToken{token}
	}
	return tokens
}
