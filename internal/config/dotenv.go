package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func SetUpVariables() error {
	file, err := openEnvFile()
	if err != nil {
		return err
	}
	defer file.Close()

	env := make(map[string]string)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		env[key] = value
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("setting environment variable %q: %w", key, err)
		}
	}

	return nil
}

func openEnvFile() (*os.File, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	for {
		path := filepath.Join(dir, ".env")

		if _, err := os.Stat(path); err == nil {
			return os.Open(path)
		} else if !os.IsNotExist(err) {
			return nil, err
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf(".env file not found in current directory or its parents")
		}

		dir = parent
	}
}
