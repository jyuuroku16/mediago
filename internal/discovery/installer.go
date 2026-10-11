package discovery

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// InstallStatus 安装状态枚举
type InstallStatus string

const (
	StatusIdle        InstallStatus = "idle"
	StatusDownloading InstallStatus = "downloading"
	StatusExtracting  InstallStatus = "extracting"
	StatusCompleted   InstallStatus = "completed"
	StatusFailed      InstallStatus = "failed"
)

// InstallProgress 安装进度与详情
type InstallProgress struct {
	ToolName   string        `json:"toolName"`   // "yt-dlp", "ffmpeg", "all"
	Status     InstallStatus `json:"status"`     // idle, downloading, extracting, completed, failed
	Downloaded int64         `json:"downloaded"` // 已下载字节数
	Total      int64         `json:"total"`      // 总大小 (-1 为未知)
	Percent    float64       `json:"percent"`    // 0.0 ~ 1.0
	Message    string        `json:"message"`    // 界面展示状态
	Error      string        `json:"error"`
}

// ToolInstaller 跨平台工具下载与安装器
type ToolInstaller struct {
	mu        sync.RWMutex
	client    *http.Client
	state     InstallProgress
	listeners []func(InstallProgress)
}

var (
	defaultInstaller     *ToolInstaller
	defaultInstallerOnce sync.Once
)

// DefaultInstaller 获取全局默认安装器单例
func DefaultInstaller() *ToolInstaller {
	defaultInstallerOnce.Do(func() {
		defaultInstaller = &ToolInstaller{
			client: &http.Client{
				Timeout: 15 * time.Minute,
			},
			state: InstallProgress{
				Status:  StatusIdle,
				Message: "就绪",
			},
		}
	})
	return defaultInstaller
}

// OnProgress 注册进度回调监听
func (ti *ToolInstaller) OnProgress(fn func(InstallProgress)) {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	ti.listeners = append(ti.listeners, fn)
}

// State 获取当前安装器状态
func (ti *ToolInstaller) State() InstallProgress {
	ti.mu.RLock()
	defer ti.mu.RUnlock()
	return ti.state
}

func (ti *ToolInstaller) emit(p InstallProgress) {
	ti.mu.Lock()
	ti.state = p
	listeners := make([]func(InstallProgress), len(ti.listeners))
	copy(listeners, ti.listeners)
	ti.mu.Unlock()

	for _, l := range listeners {
		l(p)
	}
}

// GetDownloadURLs 根据系统与 CPU 架构计算官方及备用镜像下载地址
func GetDownloadURLs(tool string) (directURL, mirrorURL string, err error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch tool {
	case "yt-dlp":
		switch osName {
		case "windows":
			directURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
		case "darwin":
			directURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos"
		case "linux":
			if arch == "arm64" {
				directURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux_aarch64"
			} else {
				directURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux"
			}
		default:
			return "", "", fmt.Errorf("暂不支持的操作系统: %s", osName)
		}
	case "ffmpeg":
		switch osName {
		case "windows":
			directURL = "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip"
		case "darwin":
			if arch == "arm64" {
				directURL = "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-osxarm64-gpl.zip"
			} else {
				directURL = "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-osx64-gpl.zip"
			}
		case "linux":
			if arch == "arm64" {
				directURL = "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-linuxarm64-gpl.zip"
			} else {
				directURL = "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-linux64-gpl.zip"
			}
		default:
			return "", "", fmt.Errorf("暂不支持的操作系统: %s", osName)
		}
	default:
		return "", "", fmt.Errorf("未知工具组件: %s", tool)
	}

	// 备用镜像加速 (自动通过开源加速通道分发，提高国内用户成功率)
	mirrorURL = "https://ghfast.top/" + directURL
	return directURL, mirrorURL, nil
}

// InstallTool 下载并安装单个工具组件 ("yt-dlp" 或 "ffmpeg")
func (ti *ToolInstaller) InstallTool(ctx context.Context, tool string) error {
	ti.mu.RLock()
	if ti.state.Status == StatusDownloading || ti.state.Status == StatusExtracting {
		ti.mu.RUnlock()
		return fmt.Errorf("当前已有组件正在下载安装中")
	}
	ti.mu.RUnlock()

	directURL, mirrorURL, err := GetDownloadURLs(tool)
	if err != nil {
		ti.emit(InstallProgress{
			ToolName: tool,
			Status:   StatusFailed,
			Message:  "架构不支持",
			Error:    err.Error(),
		})
		return err
	}

	targetDir := GetToolsDir()
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		ti.emit(InstallProgress{
			ToolName: tool,
			Status:   StatusFailed,
			Message:  "创建目录失败",
			Error:    err.Error(),
		})
		return err
	}

	tmpFile := filepath.Join(targetDir, fmt.Sprintf("%s.download.tmp", tool))
	defer func() { _ = os.Remove(tmpFile) }()

	ti.emit(InstallProgress{
		ToolName: tool,
		Status:   StatusDownloading,
		Percent:  0.0,
		Message:  fmt.Sprintf("正在下载 %s...", tool),
	})

	// 尝试官方直连，若连接超时或失败则自动尝试备用镜像
	err = ti.downloadWithProgress(ctx, directURL, tmpFile, tool)
	if err != nil {
		ti.emit(InstallProgress{
			ToolName: tool,
			Status:   StatusDownloading,
			Percent:  0.0,
			Message:  fmt.Sprintf("直连失败，正在尝试通过镜像加速通道下载 %s...", tool),
		})
		err = ti.downloadWithProgress(ctx, mirrorURL, tmpFile, tool)
	}

	if err != nil {
		ti.emit(InstallProgress{
			ToolName: tool,
			Status:   StatusFailed,
			Message:  fmt.Sprintf("%s 下载失败", tool),
			Error:    err.Error(),
		})
		return err
	}

	// 下载完成，开始安装或解压
	ti.emit(InstallProgress{
		ToolName: tool,
		Status:   StatusExtracting,
		Percent:  0.95,
		Message:  fmt.Sprintf("正在配置 %s 权限与组件...", tool),
	})

	finalExe := filepath.Join(targetDir, ToolExecutableName(tool))

	if tool == "ffmpeg" {
		// 解压 zip 包中的 ffmpeg 二进制
		if err := extractFFmpegFromZip(tmpFile, finalExe); err != nil {
			ti.emit(InstallProgress{
				ToolName: tool,
				Status:   StatusFailed,
				Message:  "解压 FFmpeg 失败",
				Error:    err.Error(),
			})
			return err
		}
	} else {
		// yt-dlp 单文件覆盖写入
		_ = os.Remove(finalExe) // Windows 下若存在先清理
		if err := copyFile(tmpFile, finalExe); err != nil {
			ti.emit(InstallProgress{
				ToolName: tool,
				Status:   StatusFailed,
				Message:  "保存文件失败",
				Error:    err.Error(),
			})
			return err
		}
	}

	// 在 macOS / Linux 赋予执行权限
	if runtime.GOOS != "windows" {
		_ = os.Chmod(finalExe, 0755)
	}

	// 刷新检测缓存
	RefreshToolVersions()

	ti.emit(InstallProgress{
		ToolName: tool,
		Status:   StatusCompleted,
		Percent:  1.0,
		Message:  fmt.Sprintf("%s 安装成功", tool),
	})

	return nil
}

// InstallAll 依次下载安装缺失的所有组件 (yt-dlp 与 ffmpeg)
func (ti *ToolInstaller) InstallAll(ctx context.Context) error {
	ti.mu.RLock()
	if ti.state.Status == StatusDownloading || ti.state.Status == StatusExtracting {
		ti.mu.RUnlock()
		return fmt.Errorf("当前已有组件正在下载安装中")
	}
	ti.mu.RUnlock()

	// 1. 安装 yt-dlp
	if FindYtDlp() == "" || !hasToolInManagedDir("yt-dlp") {
		if err := ti.InstallTool(ctx, "yt-dlp"); err != nil {
			return err
		}
	}

	// 2. 安装 ffmpeg
	if FindFFmpeg() == "" || !hasToolInManagedDir("ffmpeg") {
		if err := ti.InstallTool(ctx, "ffmpeg"); err != nil {
			return err
		}
	}

	ti.emit(InstallProgress{
		ToolName: "all",
		Status:   StatusCompleted,
		Percent:  1.0,
		Message:  "所有核心工具组件已安装完毕",
	})
	return nil
}

func hasToolInManagedDir(name string) bool {
	path := filepath.Join(GetToolsDir(), ToolExecutableName(name))
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func (ti *ToolInstaller) downloadWithProgress(ctx context.Context, urlStr, dstPath, toolName string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "MediaGo-Installer/1.0")

	resp, err := ti.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("服务器响应异常状态码: %d %s", resp.StatusCode, resp.Status)
	}

	total := resp.ContentLength
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 32*1024)
	var downloaded int64
	lastUpdate := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return wErr
			}
			downloaded += int64(n)

			if time.Since(lastUpdate) > 100*time.Millisecond || rErr != nil {
				percent := 0.0
				msg := ""
				if total > 0 {
					percent = float64(downloaded) / float64(total)
					if percent > 0.95 {
						percent = 0.95
					}
					msg = fmt.Sprintf("正在下载 %s: %.1f%% (%.1f / %.1f MB)",
						toolName, percent*100,
						float64(downloaded)/(1024*1024),
						float64(total)/(1024*1024))
				} else {
					msg = fmt.Sprintf("正在下载 %s: %.1f MB",
						toolName, float64(downloaded)/(1024*1024))
				}

				ti.emit(InstallProgress{
					ToolName:   toolName,
					Status:     StatusDownloading,
					Downloaded: downloaded,
					Total:      total,
					Percent:    percent,
					Message:    msg,
				})
				lastUpdate = time.Now()
			}
		}

		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			return rErr
		}
	}

	return nil
}

// extractFFmpegFromZip 从静态包 zip 中提取 ffmpeg / ffmpeg.exe
func extractFFmpegFromZip(zipPath, dstExe string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开 zip 失败: %w", err)
	}
	defer r.Close()

	exeName := filepath.Base(dstExe)
	var targetFile *zip.File

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(f.Name)
		if strings.EqualFold(base, exeName) || (runtime.GOOS != "windows" && base == "ffmpeg") {
			targetFile = f
			break
		}
	}

	if targetFile == nil {
		return fmt.Errorf("在下载的压缩包内未找到可执行文件 %s", exeName)
	}

	rc, err := targetFile.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	_ = os.Remove(dstExe)
	out, err := os.OpenFile(dstExe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return err
	}

	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
