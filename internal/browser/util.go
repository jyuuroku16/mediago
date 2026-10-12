package browser

import "strings"

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
