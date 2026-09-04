package agent

import (
	"orca/pkg/utils"
)

// Settings mirrors the structure of .orca/setting.json, one field per setting.
type Settings struct {
	DefaultProvider string `json:"defaultProvider"`
	DefaultModel    string `json:"defaultModel"`
}

// settings is the process-wide configuration backing Get and Set.
var settings Settings = LoadSettings()

// Names of the well-known settings, matching the JSON field names.
const (
	KeyDefaultProvider = "defaultProvider"
	KeyDefaultModel    = "defaultModel"
	setting            = "setting.json"
)

// Get returns the value of the named setting, or "" when the key is unknown.
func Get(key string) string {
	switch key {
	case KeyDefaultProvider:
		return settings.DefaultProvider
	case KeyDefaultModel:
		return settings.DefaultModel
	default:
		return ""
	}
}

// Set stores value under the named setting, ignoring unknown keys.
func Set(key, value string) {
	switch key {
	case KeyDefaultProvider:
		settings.DefaultProvider = value
	case KeyDefaultModel:
		settings.DefaultModel = value
	}
}

// LoadSettings reads setting.json from the user home and the project directory,
// merging them so project-level entries override home-level ones. Each file is
// unmarshalled straight onto settings, so only the keys a file actually declares
// are overwritten and a missing or malformed file is skipped.
func LoadSettings() Settings {
	var res Settings

	if err := utils.GetModel(setting, &res); err != nil {
		panic(err)
	}

	return res
}
