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
func NewLibrary(dir ...string) (*Library, error) {
	base := ""
	if len(dir) > 0 && dir[0] != "" {
		base = dir[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, "Movies", "MediaGo")
	}

	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, fmt.Errorf("创建媒体库目录失败: %w", err)
	}
	return &Library{baseDir: base}, nil
}

// BaseDir 获取存储根路径
func (l *Library) BaseDir() string {
	return l.baseDir
}

// SetBaseDir 动态更新媒体库根存储路径
func (l *Library) SetBaseDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("目录路径不能为空")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	l.baseDir = dir
	return nil
}

// OpenInFinder 在 macOS Finder 中定位并高亮选定文件
func (l *Library) OpenInFinder(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	return exec.Command("open", "-R", filePath).Run()
}

// QuickLook 使用 macOS 系统原生播放器打开媒体 (不调用 qlmanage 调试工具)
func (l *Library) QuickLook(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	return exec.Command("open", filePath).Start()
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
