package config

import (
	"bufio"
	"fmt"
	"io/fs"
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

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = unquote(strings.TrimSpace(value))

		if _, set := os.LookupEnv(key); set {
			continue // the existing environment wins over the file
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("setting environment variable %q: %w", key, err)
		}
	}
	return scanner.Err()
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func openEnvFile() (*os.File, error) {
	start, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	dir := start
	for {
		path := filepath.Join(dir, ".env")
		if _, err := os.Stat(path); err == nil {
			return os.Open(path)
		} else if !os.IsNotExist(err) {
			return nil, err
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf(".env not found in %s or its parents: %w", start, fs.ErrNotExist)
		}
		dir = parent
	}
}
