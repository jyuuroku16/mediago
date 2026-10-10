package library

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Library 本地媒体资源库管理器
type Library struct {
	baseDir string
}

// NewLibrary 初始化媒体库
func NewLibrary() (*Library, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(home, "Movies", "MediaGo")
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, fmt.Errorf("创建媒体库目录失败: %w", err)
	}
	return &Library{baseDir: base}, nil
}

// BaseDir 获取存储根路径
func (l *Library) BaseDir() string {
	return l.baseDir
}

// OpenInFinder 在 macOS Finder 中定位并高亮选定文件
func (l *Library) OpenInFinder(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	return exec.Command("open", "-R", filePath).Run()
}

// QuickLook 使用 macOS Quick Look 预览选定文件
func (l *Library) QuickLook(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	return exec.Command("qlmanage", "-p", filePath).Start()
}

// MoveToTrash 将文件安全移动到 macOS 废纸篓
func (l *Library) MoveToTrash(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return nil
	}
	// 利用 AppleScript 安全移入废纸篓
	script := fmt.Sprintf(`tell application "Finder" to delete POSIX file "%s"`, filePath)
	return exec.Command("osascript", "-e", script).Run()
}
