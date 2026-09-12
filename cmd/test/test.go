package main

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"github.com/discohaus/discopanel/internal/command-completion/paper"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/rcon"
)

func main() {
	host := "localhost"
	port := 25576
	password := "1234"

	appLogger := logger.New()
	client := rcon.NewClient(host, port, password, appLogger)

	fmt.Printf("Verbunden mit %s:%d!\n", host, port)
	fmt.Println("Gib ein Kommando ein, um Predictions zu testen (tippe 'exit' zum Beenden):")
	fmt.Println("-----------------------------------------------------------------------")

	engine := paper.CreatePaperEngine(client)
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		if scanner.Err() != nil {
			fmt.Printf("Fehler beim Lesen der Eingabe: %v\n", scanner.Err())
			continue
		}

		input := scanner.Text()
		if input == "exit" || input == "quit" {
			fmt.Println("CLI beendet.")
			break
		}

		// Predictions für die Eingabe abrufen

		start := time.Now()
		predictions, err := engine.GetPredictions(input)
		duration := time.Since(start)
		fmt.Printf("Dauer: %v\n", duration)
		if err != nil {
			fmt.Printf("Fehler beim Abrufen der Predictions: %v\n\n", err)
			continue
		}

		// Ergebnisse ausgeben
		fmt.Printf("Ergebnis (%d Vorschläge):\n", len(predictions))
		for _, pred := range predictions {
			fmt.Printf(" - %s\n", pred.Text)
		}
		fmt.Println()
	}
}
