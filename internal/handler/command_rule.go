package handler

import (
	"fmt"
	"regexp"
	"strings"
)

type commandRule struct {
	name    string
	pattern *regexp.Regexp
	reason  string
}

var blockedCommandRules = []commandRule{
	{
		name:    "recursive-force-rm",
		pattern: regexp.MustCompile(`(?i)(^|[;&|]\s*)rm\s+(-[a-z]*[rf][a-z]*\s+|--recursive.*--force|--force.*--recursive)`),
		reason:  "禁止递归强制删除文件",
	},
	{
		name:    "dangerous-rm-path",
		pattern: regexp.MustCompile(`(?i)\brm\b[^;&|]*\s+(\/|~|\*|\.\.?)(\s|$)`),
		reason:  "禁止删除根目录、Home 目录或通配符路径",
	},
	{
		name:    "filesystem-format",
		pattern: regexp.MustCompile(`(?i)\b(mkfs|mkfs\.[a-z0-9]+|mkswap)\b`),
		reason:  "禁止格式化文件系统",
	},
	{
		name:    "disk-write",
		pattern: regexp.MustCompile(`(?i)\bdd\b[^;&|]*\bof\s*=\s*/dev/`),
		reason:  "禁止直接写入磁盘设备",
	},
	{
		name:    "shutdown",
		pattern: regexp.MustCompile(`(?i)\b(shutdown|poweroff|reboot|halt)\b`),
		reason:  "禁止关机或重启系统",
	},
	{
		name:    "fork-bomb",
		pattern: regexp.MustCompile(`:\s*\(\s*\)\s*\{.*:\s*\|\s*:.*\}`),
		reason:  "禁止 fork bomb",
	},
	{
		name:    "chmod-root",
		pattern: regexp.MustCompile(`(?i)\bchmod\b[^;&|]*\s+-[a-z]*R[a-z]*\s+777\s+\/`),
		reason:  "禁止递归修改根目录权限",
	},
}

func validateCommand(line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return ErrEmptyCommand
	}

	for _, rule := range blockedCommandRules {
		if rule.pattern.MatchString(line) {
			return fmt.Errorf(
				"command blocked: %s (%s)",
				rule.name,
				rule.reason,
			)
		}
	}

	return nil
}
