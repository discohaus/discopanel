package paper

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/internal/command-completion/engine"
	"github.com/discohaus/discopanel/pkg/utils"
)

type PaperCommand struct {
	Name        string
	Description string
	Aliases     []string
}

type PaperEngine struct {
	Commands []*PaperCommand
	helpFunc func(command string) (string, error)
}

func CreatePaperEngine(commandProvider engine.CommandProvider) *PaperEngine {
	helpFunc := func(command string) (string, error) {
		if command == "" {
			return commandProvider.Execute("help")
		}
		return commandProvider.Execute(strings.Join([]string{"help", command}, " "))
	}
	return &PaperEngine{
		helpFunc: helpFunc,
	}
}

func (e *PaperEngine) GetPredictions(command string) ([]*engine.Token, error) {
	if err := e.EnsureCommandsLoaded(); err != nil {
		return nil, err
	}
	predictions := make([]*engine.Token, 0, len(e.Commands))
	for _, cmd := range e.Commands {
		if strings.HasPrefix(cmd.Name, command) {
			predictions = append(predictions, &engine.Token{Text: cmd.Name})
		}
		for _, alias := range cmd.Aliases {
			if strings.HasPrefix(alias, command) {
				predictions = append(predictions, &engine.Token{Text: alias})
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
	normalizedRawHelp := utils.StripMinecraftColors(strings.TrimSpace(rawHelp))
	nameSpaces := parseHelpNamespaces(normalizedRawHelp)
	e.Commands = make([]*PaperCommand, 0, len(nameSpaces)*10)
	for _, namespace := range nameSpaces {
		if namespace == "Aliases" {
			continue
		}
		commands, err := e.GetCommandsForNameSpace(namespace)
		if err != nil {
			return err
		}
		e.Commands = append(e.Commands, commands...)
	}
	return nil
}

var helpNumberRegex = regexp.MustCompile(`(?m)^-+\s*Help:\s*.*\((\d+)\/(\d+)\)`)

func (e *PaperEngine) GetCommandsForNameSpace(namespace string) ([]*PaperCommand, error) {
	rawHelp, err := e.helpFunc(namespace)
	if err != nil {
		return nil, err
	}
	normalizedRawHelp := utils.StripMinecraftColors(strings.TrimSpace(rawHelp))

	matches := helpNumberRegex.FindStringSubmatch(normalizedRawHelp)
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
		normalizedRawHelp := utils.StripMinecraftColors(strings.TrimSpace(rawHelp))
		commands = append(convertHelpCommandsToPaperCommands(parseHelpCommands(normalizedRawHelp)), commands...)
	}

	return commands, nil
}

func (e *PaperEngine) GetBaseCommands() ([]*engine.BaseCommand, error) {
	err := e.EnsureCommandsLoaded()
	if err != nil {
		return nil, err
	}

	commands := make([]*engine.BaseCommand, 0, len(e.Commands))
	for _, cmd := range e.Commands {
		commands = append(commands, &engine.BaseCommand{
			Name:        cmd.Name,
			Description: nil,
			Aliases:     cmd.Aliases,
		})
	}
	return commands, nil
}

func parseHelpNamespaces(input string) []string {
	cleanInput := utils.StripMinecraftColors(input)
	re := regexp.MustCompile(`(?m)^\s*([a-zA-Z0-9][a-zA-Z0-9_\-]*):`)

	matches := re.FindAllStringSubmatch(cleanInput, -1)
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
	cleanInput := utils.StripMinecraftColors(input)
	re := regexp.MustCompile(`(?m)^\s*/([^\s:]+(?::[^\s:]+)*):`)

	matches := re.FindAllStringSubmatch(cleanInput, -1)
	results := make([]string, 0, len(matches))

	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		results = append(results, key)
	}

	return results
}
