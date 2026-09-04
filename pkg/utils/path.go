package utils

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

var (
	workspaceEnv = "WORKSPACE"
)

func GetCurrentPath() string {
	if envPath := GetEnv(workspaceEnv); envPath != "" {
		return envPath
	}
	path, err := os.Getwd()
	if err != nil {
		return ""
	}
	return path
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func GetConfigPaths(filename string) []string {
	var paths []string

	if envPath := GetEnv(workspaceEnv); envPath != "" {
		paths = append(paths, filepath.Join(envPath, ".orca", filename))
	}

	if wd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(wd, ".orca", filename))
	}

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".orca", filename))
	}

	return paths
}

func GetModel(filename string, res any) error {
	for _, path := range GetConfigPaths(filename) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return json.Unmarshal(data, &res)
	}

	return errors.New("could not find configuration file")
}
