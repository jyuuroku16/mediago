package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	toolsMu         sync.RWMutex
	cachedYtDlpVer  string
	cachedFFmpegVer string
	hasDetected     bool
)

// GetToolsDir 获取跨平台标准的用户工具存储目录
// - Windows: %LocalAppData%\MediaGo\bin 或 ~\AppData\Local\MediaGo\bin
// - macOS:   ~/Library/Application Support/MediaGo/bin
// - Linux:   $XDG_DATA_HOME/MediaGo/bin 或 ~/.local/share/MediaGo/bin
func GetToolsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}

	switch runtime.GOOS {
	case "windows":
		localAppData := os.Getenv("LocalAppData")
		if localAppData != "" {
			return filepath.Join(localAppData, "MediaGo", "bin")
		}
		return filepath.Join(home, "AppData", "Local", "MediaGo", "bin")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "MediaGo", "bin")
	default: // linux and others
		if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
			return filepath.Join(xdgData, "MediaGo", "bin")
		}
		return filepath.Join(home, ".local", "share", "MediaGo", "bin")
	}
}

// ToolExecutableName 根据平台返回可执行文件标准名称 (Windows 自动补齐 .exe)
func ToolExecutableName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

// FindTool 按照跨平台统一优先级探测指定工具的可执行路径
// 优先级 1: MediaGo 专用工具目录 (GetToolsDir())
// 优先级 2: 系统 PATH 环境变量 (exec.LookPath)
// 优先级 3: 常见系统预置路径 (Homebrew / /usr/local / /usr/bin 等)
func FindTool(name string) string {
	exeName := ToolExecutableName(name)

	// 1. 优先探测 MediaGo 独立管理目录 (具备最高优先级，方便热更新与隔离)
	managedPath := filepath.Join(GetToolsDir(), exeName)
	if fi, err := os.Stat(managedPath); err == nil && !fi.IsDir() {
		return managedPath
	}

	// 2. 探测系统环境变量 PATH
	if p, err := exec.LookPath(exeName); err == nil {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	home, _ := os.UserHomeDir()

	// 3. 常见平台系统备用目录与环境管理器路径探测 (覆盖 pyenv, pip, mise, asdf, conda, brew, scoop 等)
	var systemCandidates []string
	switch runtime.GOOS {
	case "darwin":
		systemCandidates = []string{
			filepath.Join(home, ".pyenv", "shims", exeName),
			filepath.Join(home, ".local", "bin", exeName),
			filepath.Join(home, "bin", exeName),
			filepath.Join(home, ".cargo", "bin", exeName),
			filepath.Join(home, ".asdf", "shims", exeName),
			filepath.Join(home, ".local", "share", "mise", "shims", exeName),
			filepath.Join(home, "miniconda3", "bin", exeName),
			filepath.Join(home, "anaconda3", "bin", exeName),
			filepath.Join("/opt/homebrew", "bin", exeName),
			filepath.Join("/usr/local", "bin", exeName),
			filepath.Join("/opt/local", "bin", exeName),
			filepath.Join("/usr", "bin", exeName),
		}
		if matches, err := filepath.Glob(filepath.Join(home, "Library", "Python", "*", "bin", exeName)); err == nil {
			systemCandidates = append(systemCandidates, matches...)
		}
	case "windows":
		userProfile := os.Getenv("USERPROFILE")
		localAppData := os.Getenv("LOCALAPPDATA")
		appData := os.Getenv("APPDATA")
		systemCandidates = []string{
			filepath.Join(userProfile, "scoop", "shims", exeName),
			filepath.Join(userProfile, ".cargo", "bin", exeName),
			`C:\Program Files\ffmpeg\bin\` + exeName,
			`C:\ProgramData\chocolatey\bin\` + exeName,
		}
		if localAppData != "" {
			if matches, err := filepath.Glob(filepath.Join(localAppData, "Programs", "Python", "Python*", "Scripts", exeName)); err == nil {
				systemCandidates = append(systemCandidates, matches...)
			}
		}
		if appData != "" {
			if matches, err := filepath.Glob(filepath.Join(appData, "Python", "Python*", "Scripts", exeName)); err == nil {
				systemCandidates = append(systemCandidates, matches...)
			}
		}
	case "linux":
		systemCandidates = []string{
			filepath.Join(home, ".local", "bin", exeName),
			filepath.Join(home, "bin", exeName),
			filepath.Join(home, ".pyenv", "shims", exeName),
			filepath.Join(home, ".cargo", "bin", exeName),
			filepath.Join(home, ".asdf", "shims", exeName),
			filepath.Join("/usr/local", "bin", exeName),
			filepath.Join("/usr", "bin", exeName),
			filepath.Join("/bin", exeName),
			filepath.Join("/snap", "bin", exeName),
		}
	}

	for _, cand := range systemCandidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}

	return ""
}

// FindYtDlp 探测可用的 yt-dlp 路径
func FindYtDlp() string {
	return FindTool("yt-dlp")
}

// FindFFmpeg 探测可用的 ffmpeg 路径
func FindFFmpeg() string {
	return FindTool("ffmpeg")
}

// detectToolVersions 执行版本探测
func detectToolVersions() {
	toolsMu.Lock()
	defer toolsMu.Unlock()

	// 1. yt-dlp 版本探测
	ytPath := FindYtDlp()
	cachedYtDlpVer = ""
	if ytPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, ytPath, "--version").Output()
		cancel()
		if err == nil {
			cachedYtDlpVer = strings.TrimSpace(string(out))
		}
	}
	if cachedYtDlpVer == "" {
		cachedYtDlpVer = "未安装"
	}

	// 2. FFmpeg 版本探测
	ffPath := FindFFmpeg()
	cachedFFmpegVer = ""
	if ffPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, ffPath, "-version").Output()
		cancel()
		if err == nil {
			lines := strings.Split(string(out), "\n")
			if len(lines) > 0 {
				re := regexp.MustCompile(`ffmpeg version ([^\s]+)`)
				m := re.FindStringSubmatch(lines[0])
				if len(m) > 1 {
					cachedFFmpegVer = m[1]
				} else {
					cachedFFmpegVer = strings.TrimSpace(lines[0])
				}
			}
		}
	}
	if cachedFFmpegVer == "" {
		cachedFFmpegVer = "未安装"
	}

	hasDetected = true
}

// RefreshToolVersions 强制重新探测工具版本并更新缓存
func RefreshToolVersions() (ytDlpVer, ffmpegVer string) {
	detectToolVersions()
	toolsMu.RLock()
	defer toolsMu.RUnlock()
	return cachedYtDlpVer, cachedFFmpegVer
}

// GetToolVersions 获取当前已探测的 yt-dlp 和 FFmpeg 版本
func GetToolVersions() (ytDlpVer, ffmpegVer string) {
	toolsMu.RLock()
	ready := hasDetected
	y := cachedYtDlpVer
	f := cachedFFmpegVer
	toolsMu.RUnlock()

	if !ready {
		detectToolVersions()
		toolsMu.RLock()
		y = cachedYtDlpVer
		f = cachedFFmpegVer
		toolsMu.RUnlock()
	}
	return y, f
}
