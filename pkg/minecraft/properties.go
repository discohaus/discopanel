package minecraft

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Represents the Minecraft server.properties file
type PropertiesFile map[string]string

// Loads the server.properties file into a map
func LoadPropertiesFile(serverDataPath string) (PropertiesFile, error) {
	data, err := os.ReadFile(filepath.Join(serverDataPath, "server.properties"))
	if err != nil {
		return nil, err
	}
	props := PropertiesFile{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			props[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return props, nil
}

// Saves the server.properties file
func SavePropertiesFile(serverDataPath string, properties PropertiesFile) error {
	propertiesPath := filepath.Join(serverDataPath, "server.properties")

	// Ensure directory exists
	if err := os.MkdirAll(serverDataPath, 0755); err != nil {
		return fmt.Errorf("failed to create server data directory: %w", err)
	}

	// Read the original file to preserve comments and ordering
	originalLines := []string{}
	if file, err := os.Open(propertiesPath); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			originalLines = append(originalLines, scanner.Text())
		}
		err = scanner.Err()
		if err != nil {
			return fmt.Errorf("failed to scan server properties file: %w", err)
		}
		file.Close()
	}

	// Temp file plus rename keeps a crash from eating the file
	tmpPath := propertiesPath + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create server.properties: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	updatedKeys := make(map[string]bool)

	// Update existing lines
	for _, line := range originalLines {
		trimmed := strings.TrimSpace(line)

		// Keep comments and empty lines
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			fmt.Fprintf(writer, "%s\n", line)
			continue
		}

		// Check if this is a property line
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			if newValue, exists := properties[key]; exists {
				fmt.Fprintf(writer, "%s=%s\n", key, newValue)
				updatedKeys[key] = true
			} else {
				fmt.Fprintf(writer, "%s\n", line)
			}
		} else {
			fmt.Fprintf(writer, "%s\n", line)
		}
	}

	// New keys append in stable sorted order
	newKeys := make([]string, 0, len(properties))
	for key := range properties {
		if !updatedKeys[key] {
			newKeys = append(newKeys, key)
		}
	}
	sort.Strings(newKeys)
	for _, key := range newKeys {
		fmt.Fprintf(writer, "%s=%s\n", key, properties[key])
	}

	if err := writer.Flush(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := file.Sync(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, propertiesPath)
}
