package browser

import (
	"encoding/json"
	"strings"
)

// sanitizeFilename 清理嗅探标题中的路径分隔符与非法文件名字符，
// 避免外部页面标题造成目录穿越或非法文件名
func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "：",
		"*", "_",
		"?", "？",
		"\"", "'",
		"<", "（",
		">", "）",
		"|", "_",
		"\n", " ",
		"\r", " ",
		"\t", " ",
	)
	cleaned := strings.TrimSpace(replacer.Replace(name))
	cleaned = strings.Trim(cleaned, ". ")
	if len([]rune(cleaned)) > 120 {
		cleaned = string([]rune(cleaned)[:120])
	}
	return cleaned
}

// jsString 将任意 Go 字符串安全编码为 JS 字符串字面量 (含双引号，转义换行与控制字符)，
// 避免手工 ReplaceAll 转义遗漏导致的脚本语法错误或注入风险
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
