package engine

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"mediago/internal/model"
)

// ExtractorWorkerEngine 基于受控 yt-dlp 与 ffmpeg 的综合流媒体与复杂站点下载引擎
type ExtractorWorkerEngine struct {
	binPath string
}

// NewExtractorWorkerEngine 初始化引擎
func NewExtractorWorkerEngine() *ExtractorWorkerEngine {
	candidatePaths := []string{
		"/Users/wuzhiying/.pyenv/shims/yt-dlp",
		"/opt/homebrew/bin/yt-dlp",
		"/usr/local/bin/yt-dlp",
	}

	selected := ""
	for _, p := range candidatePaths {
		if _, err := os.Stat(p); err == nil {
			selected = p
			break
		}
	}
	if selected == "" {
		if p, err := exec.LookPath("yt-dlp"); err == nil {
			selected = p
		}
	}

	return &ExtractorWorkerEngine{binPath: selected}
}

// 正则解析 yt-dlp 的下载输出行: [download]  45.2% of ~101.00MiB at  3.50MiB/s ETA 00:15
var (
	progressRegex = regexp.MustCompile(`\[download\]\s+([0-9.]+)%\s+of\s+~?([0-9.]+)([KMG]iB|B)\s+at\s+([0-9.]+)([KMG]iB/s|B/s)`)
	totalEstRegex = regexp.MustCompile(`\[download\]\s+([0-9.]+)%\s+of\s+~?([0-9.]+)([KMG]iB|B)`)
)

// Download 执行音视频流并发下载与 ffmpeg 自动合并
func (e *ExtractorWorkerEngine) Download(ctx context.Context, task *model.Task, opts DownloadOptions) error {
	if e.binPath == "" {
		return fmt.Errorf("系统未安装可用的 yt-dlp")
	}

	if err := os.MkdirAll(task.OutputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	// 最终落盘文件名强制为 mp4 (除非是纯音频)
	finalFilename := task.OutputFilename
	if !strings.HasSuffix(finalFilename, ".mp4") && !strings.HasSuffix(finalFilename, ".m4a") {
		finalFilename += ".mp4"
	}
	finalPath := filepath.Join(task.OutputDir, finalFilename)
	task.FinalPath = finalPath

	// 临时模板路径
	outputTemplate := filepath.Join(task.OutputDir, task.ID+"_tmp.%(ext)s")

	formatSelector := task.DownloadURL
	if formatSelector == "" || strings.HasPrefix(formatSelector, "http://") || strings.HasPrefix(formatSelector, "https://") {
		formatSelector = "bv*+ba/b"
	}

	args := []string{
		"--newline",
		"--no-warnings",
		"-f", formatSelector,
		"--merge-output-format", "mp4",
		"-o", outputTemplate,
		task.SourceURL,
	}

	cmd := exec.CommandContext(ctx, e.binPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("获取 stdout 失败: %w", err)
	}
	cmd.Stderr = cmd.Stdout // 合并 stderr 到 stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动下载器子进程失败: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()

		// 解析进度
		if matches := progressRegex.FindStringSubmatch(line); len(matches) >= 6 {
			pct, _ := strconv.ParseFloat(matches[1], 64)
			totalNum, _ := strconv.ParseFloat(matches[2], 64)
			totalUnit := matches[3]
			speedNum, _ := strconv.ParseFloat(matches[4], 64)
			speedUnit := matches[5]

			totalBytes := parseBytesWithUnit(totalNum, totalUnit)
			speedBytes := parseSpeedWithUnit(speedNum, speedUnit)

			if totalBytes > 0 {
				atomic.StoreInt64(&task.TotalBytes, totalBytes)
				downloaded := int64(float64(totalBytes) * (pct / 100.0))
				atomic.StoreInt64(&task.DownloadedBytes, downloaded)
			}
			atomic.StoreInt64(&task.Speed, speedBytes)
			task.Progress = pct / 100.0

			if opts.OnProgress != nil {
				opts.OnProgress(task.DownloadedBytes, task.TotalBytes, task.Speed)
			}
		} else if matches := totalEstRegex.FindStringSubmatch(line); len(matches) >= 4 {
			pct, _ := strconv.ParseFloat(matches[1], 64)
			totalNum, _ := strconv.ParseFloat(matches[2], 64)
			totalUnit := matches[3]

			totalBytes := parseBytesWithUnit(totalNum, totalUnit)
			if totalBytes > 0 {
				atomic.StoreInt64(&task.TotalBytes, totalBytes)
				downloaded := int64(float64(totalBytes) * (pct / 100.0))
				atomic.StoreInt64(&task.DownloadedBytes, downloaded)
			}
			task.Progress = pct / 100.0
			if opts.OnProgress != nil {
				opts.OnProgress(task.DownloadedBytes, task.TotalBytes, 0)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("下载合并进程退出异常: %w", err)
	}

	// 查找实际生成的临时文件并重命名为最终文件
	matches, _ := filepath.Glob(filepath.Join(task.OutputDir, task.ID+"_tmp.*"))
	if len(matches) > 0 {
		tmpProduced := matches[0]
		_ = os.Remove(finalPath) // 覆盖已有
		if err := os.Rename(tmpProduced, finalPath); err != nil {
			return fmt.Errorf("重命名产物失败: %w", err)
		}
	} else if _, err := os.Stat(finalPath); err != nil {
		return fmt.Errorf("未找到下载产物文件: %s", finalPath)
	}

	return nil
}

func parseBytesWithUnit(num float64, unit string) int64 {
	switch unit {
	case "GiB":
		return int64(num * 1024 * 1024 * 1024)
	case "MiB":
		return int64(num * 1024 * 1024)
	case "KiB":
		return int64(num * 1024)
	default:
		return int64(num)
	}
}

func parseSpeedWithUnit(num float64, unit string) int64 {
	switch unit {
	case "GiB/s":
		return int64(num * 1024 * 1024 * 1024)
	case "MiB/s":
		return int64(num * 1024 * 1024)
	case "KiB/s":
		return int64(num * 1024)
	default:
		return int64(num)
	}
}
