package mcconsole

import (
	"fmt"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Runs one console command and returns its output
type CommandProvider interface {
	Execute(command string) (string, error)
}

// Lists the players currently online
type PlayerListProvider interface {
	GetPlayers() ([]string, error)
}

// Adapts a plain function into a CommandProvider
type CommandFunc func(command string) (string, error)

func (f CommandFunc) Execute(command string) (string, error) {
	return f(command)
}

// Adapts a plain function into a PlayerListProvider
type PlayerListFunc func() ([]string, error)

func (f PlayerListFunc) GetPlayers() ([]string, error) {
	return f()
}

// One top level command a server knows
type BaseCommand struct {
	Name        string
	Description *string
	Aliases     []string
}

// One known value an argument accepts
type MappedValue struct {
	Text     string
	IsPlayer bool
}

// One completion suggestion for the typed command
type Token struct {
	Text       string
	IsOptional bool
	IsArgument bool
	IsStatic   bool
	IsPlayer   bool
}

// Predicts console commands from server help output
type CompletionEngine interface {
	GetPredictions(command string) ([]*Token, error)
	GetBaseCommands() ([]*BaseCommand, error)
	LoadCommands() error
}

// Help output dialect a server prints
type HelpSyntax int

const (
	HelpSyntaxUnknown HelpSyntax = iota
	// Brigadier help from Minecraft 1.13 and newer
	HelpSyntaxVanilla
	// Paginated Bukkit help printed by Paper forks
	HelpSyntaxBukkit
)

// Oldest Minecraft version whose vanilla help parses
const VanillaHelpMinVersion = "1.13"

// Picks the help syntax a mod loader prints
func HelpSyntaxFor(loader v1.ModLoader) HelpSyntax {
	switch loader {
	case v1.ModLoader_MOD_LOADER_VANILLA,
		v1.ModLoader_MOD_LOADER_FORGE,
		v1.ModLoader_MOD_LOADER_NEOFORGE,
		v1.ModLoader_MOD_LOADER_FABRIC,
		v1.ModLoader_MOD_LOADER_QUILT:
		return HelpSyntaxVanilla
	case v1.ModLoader_MOD_LOADER_PAPER,
		v1.ModLoader_MOD_LOADER_PURPUR,
		v1.ModLoader_MOD_LOADER_FOLIA:
		return HelpSyntaxBukkit
	default:
		return HelpSyntaxUnknown
	}
}

// Builds the completion engine for a mod loader
func NewCompletionEngine(loader v1.ModLoader, commands CommandProvider, players PlayerListProvider) (CompletionEngine, error) {
	switch HelpSyntaxFor(loader) {
	case HelpSyntaxVanilla:
		return NewVanillaEngine(commands, players), nil
	case HelpSyntaxBukkit:
		return NewPaperEngine(commands), nil
	default:
		return nil, fmt.Errorf("unsupported mod loader: %v", loader)
	}
}

// Wraps a command provider into a help page lookup
func helpLookup(commandProvider CommandProvider) func(command string) (string, error) {
	return func(command string) (string, error) {
		if command == "" {
			return commandProvider.Execute("help")
		}
		return commandProvider.Execute("help " + command)
	}
}
