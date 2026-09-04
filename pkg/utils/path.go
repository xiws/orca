package utils

import "os"

func GetCurrentPath() string {
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
