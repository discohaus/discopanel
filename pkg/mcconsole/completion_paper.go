package mcconsole

import (
	"regexp"
	"strconv"
	"strings"
)

// Page counter in a Bukkit help header
var paperHelpPageRegex = regexp.MustCompile(`(?m)^-+\s*Help:\s*.*\((\d+)\/(\d+)\)`)

// Namespace headings in the Bukkit help index
var paperHelpNamespaceRegex = regexp.MustCompile(`(?m)^\s*([a-zA-Z0-9][a-zA-Z0-9_\-]*):`)

// Slash commands listed on a Bukkit help page
var paperHelpCommandRegex = regexp.MustCompile(`(?m)^\s*/([^\s:]+(?::[^\s:]+)*):`)

// One command parsed from Bukkit help pages
type PaperCommand struct {
	Name        string
	Description string
	Aliases     []string
}

// Predicts commands from paginated Bukkit help output
type PaperEngine struct {
	Commands []*PaperCommand
	helpFunc func(command string) (string, error)
}

// Builds an engine that reads help over the command provider
func NewPaperEngine(commandProvider CommandProvider) *PaperEngine {
	return &PaperEngine{
		helpFunc: helpLookup(commandProvider),
	}
}

func (e *PaperEngine) GetPredictions(command string) ([]*Token, error) {
	if err := e.EnsureCommandsLoaded(); err != nil {
		return nil, err
	}
	predictions := make([]*Token, 0, len(e.Commands))
	for _, cmd := range e.Commands {
		if strings.HasPrefix(cmd.Name, command) {
			predictions = append(predictions, &Token{Text: cmd.Name})
		}
		for _, alias := range cmd.Aliases {
			if strings.HasPrefix(alias, command) {
				predictions = append(predictions, &Token{Text: alias})
			}
		}
	}

	return predictions, nil
}

func (e *PaperEngine) EnsureCommandsLoaded() error {
	if len(e.Commands) == 0 {
		return e.LoadCommands()
	}
	return nil
}

func (e *PaperEngine) LoadCommands() error {
	rawHelp, err := e.helpFunc("")
	if err != nil {
		return err
	}
	normalizedRawHelp := StripMinecraftColors(strings.TrimSpace(rawHelp))
	nameSpaces := parseHelpNamespaces(normalizedRawHelp)
	e.Commands = make([]*PaperCommand, 0, len(nameSpaces)*10)
	for _, namespace := range nameSpaces {
		if namespace == "Aliases" {
			continue
		}
		commands, err := e.GetCommandsForNamespace(namespace)
		if err != nil {
			return err
		}
		e.Commands = append(e.Commands, commands...)
	}
	return nil
}

func (e *PaperEngine) GetCommandsForNamespace(namespace string) ([]*PaperCommand, error) {
	rawHelp, err := e.helpFunc(namespace)
	if err != nil {
		return nil, err
	}
	normalizedRawHelp := StripMinecraftColors(strings.TrimSpace(rawHelp))

	matches := paperHelpPageRegex.FindStringSubmatch(normalizedRawHelp)
	if len(matches) < 3 {
		return convertHelpCommandsToPaperCommands(parseHelpCommands(normalizedRawHelp)), nil
	}
	total, _ := strconv.Atoi(matches[2])

	commands := convertHelpCommandsToPaperCommands(parseHelpCommands(normalizedRawHelp))

	for i := 2; i <= total; i++ {
		rawHelp, err := e.helpFunc(strings.Join([]string{namespace, strconv.Itoa(i)}, " "))
		if err != nil {
			return nil, err
		}
		normalizedRawHelp := StripMinecraftColors(strings.TrimSpace(rawHelp))
		commands = append(convertHelpCommandsToPaperCommands(parseHelpCommands(normalizedRawHelp)), commands...)
	}

	return commands, nil
}

func (e *PaperEngine) GetBaseCommands() ([]*BaseCommand, error) {
	err := e.EnsureCommandsLoaded()
	if err != nil {
		return nil, err
	}

	commands := make([]*BaseCommand, 0, len(e.Commands))
	for _, cmd := range e.Commands {
		commands = append(commands, &BaseCommand{
			Name:        cmd.Name,
			Description: nil,
			Aliases:     cmd.Aliases,
		})
	}
	return commands, nil
}

func parseHelpNamespaces(input string) []string {
	cleanInput := StripMinecraftColors(input)

	matches := paperHelpNamespaceRegex.FindAllStringSubmatch(cleanInput, -1)
	results := make([]string, 0, len(matches))

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		results = append(results, key)
	}

	return results
}

func convertHelpCommandsToPaperCommands(commands []string) []*PaperCommand {
	baseCommands := make([]*PaperCommand, 0, len(commands))
	for _, cmd := range commands {
		baseCommands = append(baseCommands, &PaperCommand{
			Name:        cmd,
			Description: "",
			Aliases:     nil,
		})
	}
	return baseCommands
}

func parseHelpCommands(input string) []string {
	cleanInput := StripMinecraftColors(input)

	matches := paperHelpCommandRegex.FindAllStringSubmatch(cleanInput, -1)
	results := make([]string, 0, len(matches))

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		results = append(results, key)
	}

	return results
}
