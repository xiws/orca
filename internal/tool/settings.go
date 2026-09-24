// Package tool 提供进程级配置和设置管理。
package tool

import (
	"github.com/xiws/orca/pkg/utils"
)

// Settings 镜像 .orca/setting.json 的结构，每个设置一个字段。
type Settings struct {
	DefaultProvider string `json:"defaultProvider"`
	DefaultModel    string `json:"defaultModel"`
	Debug           string `json:"debug"`
}

// settings 是进程级配置，支撑 Get 和 Set。导入包时不读取配置；加载由调用方显式负责。
var settings Settings

// 已知设置的名称，与 JSON 字段名匹配。
const (
	KeyDefaultProvider = "defaultProvider"
	KeyDefaultModel    = "defaultModel"
	KeyDebug           = "debug"
	setting            = "setting.json"
)

// Get 返回指定设置的值，未知键返回 ""。
func Get(key string) string {
	switch key {
	case KeyDefaultProvider:
		return settings.DefaultProvider
	case KeyDefaultModel:
		return settings.DefaultModel
	case KeyDebug:
		return settings.Debug
	default:
		return ""
	}
}

// Set 在指定设置下存储值，忽略未知键。
func Set(key, value string) {
	switch key {
	case KeyDefaultProvider:
		settings.DefaultProvider = value
	case KeyDefaultModel:
		settings.DefaultModel = value
	}
}

// LoadSettings 从用户主目录和项目目录读取 setting.json，
// 合并它们使项目级条目覆盖主目录级条目。每个文件直接反序列化到 settings，
// 因此只有文件实际声明的键会被覆盖，缺失或格式错误的文件会被跳过。
func LoadSettings() Settings {
	var res Settings

	if err := utils.GetModel(setting, &res); err != nil {
		panic(err)
	}

	return res
}
