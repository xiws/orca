package utils

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

var (
	// workspaceEnv 工作空间环境变量名
	workspaceEnv = "WORKSPACE"
)

// GetCurrentPath 获取当前工作路径，优先使用 WORKSPACE 环境变量，否则返回当前工作目录
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

// Exists 检查路径是否存在
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// GetConfigPaths 获取配置文件的所有可能路径，按优先级排序：
// 1. WORKSPACE 环境变量/.orca/filename
// 2. 当前工作目录/.orca/filename
// 3. 用户主目录/.orca/filename
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

// GetModel 从配置文件中加载 JSON 数据到指定结构
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
