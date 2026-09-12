package engine

type CommandProvider interface {
	Execute(command string) (string, error)
}

type PlayListProvider interface {
	GetPlayers() ([]string, error)
}

type BaseCommand struct {
	Name        string
	Description *string
	Aliases     []string
}

type Token struct {
	Text       string
	IsOptional bool
	IsArgument bool
	IsStatic   bool
}

type CompletionEngine interface {
	GetPredictions(command string) ([]*Token, error)
	GetBaseCommands() ([]*BaseCommand, error)
	LoadCommands() error
}
