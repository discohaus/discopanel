package mcconsole

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Page counter in a Bukkit help header
var paperHelpPageRegex = regexp.MustCompile(`(?m)^-+\s*Help:\s*.*\((\d+)\/(\d+)\)`)

// Namespace headings in the Bukkit help index
var paperHelpNamespaceRegex = regexp.MustCompile(`(?m)^\s*([a-zA-Z0-9][a-zA-Z0-9_\-]*):`)

// Slash commands listed on a Bukkit help page
var paperHelpCommandRegex = regexp.MustCompile(`(?m)^\s*/([^\s:]+(?::[^\s:]+)*):`)

// Alias entries listed under the Bukkit Aliases topic
var paperHelpAliasRegex = regexp.MustCompile(`(?m)^\s*/([^\s:]+(?::[^\s:]+)*):\s*Alias for\s+/(\S+)`)

// Help topic Bukkit fills with command aliases
const paperAliasesTopic = "Aliases"

// One command parsed from Bukkit help pages
type PaperCommand struct {
	Name    string
	Aliases []string
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
	sort.Slice(predictions, func(i, j int) bool {
		return predictions[i].Text < predictions[j].Text
	})
	return predictions, nil
}

func (e *PaperEngine) EnsureCommandsLoaded() error {
	if len(e.Commands) == 0 {
		return e.LoadCommands()
	}
	return nil
}

// Reads the index and every namespace, then attaches aliases
func (e *PaperEngine) LoadCommands() error {
	indexPages, err := e.helpPages("")
	if err != nil {
		return err
	}

	byName := make(map[string]*PaperCommand)
	commands := make([]*PaperCommand, 0)
	add := func(name string) {
		if _, ok := byName[name]; ok {
			return
		}
		cmd := &PaperCommand{Name: name}
		byName[name] = cmd
		commands = append(commands, cmd)
	}

	var namespaces []string
	for _, page := range indexPages {
		namespaces = append(namespaces, parseHelpNamespaces(page)...)
		for _, name := range parseHelpCommands(page) {
			add(name)
		}
	}

	for _, namespace := range namespaces {
		if namespace == paperAliasesTopic {
			continue
		}
		pages, err := e.helpPages(namespace)
		if err != nil {
			return err
		}
		for _, page := range pages {
			for _, name := range parseHelpCommands(page) {
				add(name)
			}
		}
	}

	if slices.Contains(namespaces, paperAliasesTopic) {
		pages, err := e.helpPages(paperAliasesTopic)
		if err != nil {
			return err
		}
		for _, page := range pages {
			for _, pair := range parseHelpAliases(page) {
				cmd, ok := byName[pair[1]]
				if ok && !slices.Contains(cmd.Aliases, pair[0]) {
					cmd.Aliases = append(cmd.Aliases, pair[0])
				}
			}
		}
	}

	e.Commands = commands
	return nil
}

// Fetches every page of one help topic with colors stripped
func (e *PaperEngine) helpPages(topic string) ([]string, error) {
	first, err := e.helpPage(topic, 1)
	if err != nil {
		return nil, err
	}
	pages := []string{first}
	total := 1
	if match := paperHelpPageRegex.FindStringSubmatch(first); len(match) == 3 {
		total, _ = strconv.Atoi(match[2])
	}
	for page := 2; page <= total; page++ {
		text, err := e.helpPage(topic, page)
		if err != nil {
			return nil, err
		}
		pages = append(pages, text)
	}
	return pages, nil
}

// Requests one help page, the first page needs no number
func (e *PaperEngine) helpPage(topic string, page int) (string, error) {
	query := topic
	if page > 1 {
		query = strings.TrimSpace(topic + " " + strconv.Itoa(page))
	}
	raw, err := e.helpFunc(query)
	if err != nil {
		return "", err
	}
	return StripMinecraftColors(strings.TrimSpace(raw)), nil
}

func parseHelpNamespaces(page string) []string {
	matches := paperHelpNamespaceRegex.FindAllStringSubmatch(page, -1)
	results := make([]string, 0, len(matches))
	for _, match := range matches {
		results = append(results, match[1])
	}
	return results
}

func parseHelpCommands(page string) []string {
	matches := paperHelpCommandRegex.FindAllStringSubmatch(page, -1)
	results := make([]string, 0, len(matches))
	for _, match := range matches {
		results = append(results, match[1])
	}
	return results
}

// Pairs each alias with the command it stands for
func parseHelpAliases(page string) [][2]string {
	matches := paperHelpAliasRegex.FindAllStringSubmatch(page, -1)
	pairs := make([][2]string, 0, len(matches))
	for _, match := range matches {
		pairs = append(pairs, [2]string{match[1], match[2]})
	}
	return pairs
}
