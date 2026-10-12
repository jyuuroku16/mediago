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
	"time"

	"mediago/internal/discovery"
	"mediago/internal/model"
)

// ExtractorWorkerEngine 基于受控 yt-dlp 与 ffmpeg 的综合流媒体与复杂站点下载引擎
type ExtractorWorkerEngine struct {
	binPath string
}

// NewExtractorWorkerEngine 初始化引擎
func NewExtractorWorkerEngine() *ExtractorWorkerEngine {
	return &ExtractorWorkerEngine{binPath: discovery.FindYtDlp()}
}

// 正则解析 yt-dlp 的下载输出行: [download]  45.2% of ~101.00MiB at  3.50MiB/s ETA 00:15
var (
	progressRegex = regexp.MustCompile(`\[download\]\s+([0-9.]+)%\s+of\s+~?([0-9.]+)([KMG]iB|B)\s+at\s+([0-9.]+)([KMG]iB/s|B/s)`)
	totalEstRegex = regexp.MustCompile(`\[download\]\s+([0-9.]+)%\s+of\s+~?([0-9.]+)([KMG]iB|B)`)
)

// Download 执行音视频流并发下载与 ffmpeg 自动合并
func (e *ExtractorWorkerEngine) Download(ctx context.Context, task *model.Task, opts DownloadOptions) error {
	if e.binPath == "" {
		e.binPath = discovery.FindYtDlp()
	}
	if e.binPath == "" {
		return fmt.Errorf("系统未安装可用的 yt-dlp，请在偏好设置中一键下载安装")
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
		"--concurrent-fragments", "4", // HLS/DASH 分片并发拉取，显著提升 X / B站等流媒体站点速度
		"--retries", "10",
		"--fragment-retries", "10",
		"-f", formatSelector,
		"--merge-output-format", "mp4",
		"-o", outputTemplate,
	}

	if ff := discovery.FindFFmpeg(); ff != "" {
		args = append(args, "--ffmpeg-location", ff)
	}

	args = append(args, task.SourceURL)

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
	streamIndex := 0 // 0: 视频流 (权重 88%), 1: 音频流 (权重 12%)
	for scanner.Scan() {
		line := scanner.Text()

		// 侦测流切换与合并阶段
		if strings.Contains(line, "[download] Destination:") {
			if streamIndex > 0 || task.Progress > 0.05 {
				streamIndex++
			}
		} else if strings.Contains(line, "[Merger]") || strings.Contains(line, "[ffmpeg]") {
			task.Progress = 1.0
			continue
		}

		// 解析进度
		if matches := progressRegex.FindStringSubmatch(line); len(matches) >= 6 {
			pct, _ := strconv.ParseFloat(matches[1], 64)
			totalNum, _ := strconv.ParseFloat(matches[2], 64)
			totalUnit := matches[3]
			speedNum, _ := strconv.ParseFloat(matches[4], 64)
			speedUnit := matches[5]

			totalBytes := parseBytesWithUnit(totalNum, totalUnit)
			speedBytes := parseSpeedWithUnit(speedNum, speedUnit)

			var overallProgress float64
			if streamIndex == 0 {
				overallProgress = (pct / 100.0) * 0.88
			} else {
				overallProgress = 0.88 + (pct/100.0)*0.12
			}
			if overallProgress < task.Progress {
				overallProgress = task.Progress // 单调不减
			}
			if overallProgress > 1.0 {
				overallProgress = 1.0
			}

			if totalBytes > 0 {
				atomic.StoreInt64(&task.TotalBytes, totalBytes)
				downloaded := int64(float64(totalBytes) * overallProgress)
				atomic.StoreInt64(&task.DownloadedBytes, downloaded)
			}
			atomic.StoreInt64(&task.Speed, speedBytes)
			task.Progress = overallProgress

			if opts.OnProgress != nil {
				opts.OnProgress(task.DownloadedBytes, task.TotalBytes, task.Speed)
			}
		} else if matches := totalEstRegex.FindStringSubmatch(line); len(matches) >= 4 {
			pct, _ := strconv.ParseFloat(matches[1], 64)
			totalNum, _ := strconv.ParseFloat(matches[2], 64)
			totalUnit := matches[3]

			totalBytes := parseBytesWithUnit(totalNum, totalUnit)
			var overallProgress float64
			if streamIndex == 0 {
				overallProgress = (pct / 100.0) * 0.88
			} else {
				overallProgress = 0.88 + (pct/100.0)*0.12
			}
			if overallProgress < task.Progress {
				overallProgress = task.Progress
			}
			if overallProgress > 1.0 {
				overallProgress = 1.0
			}

			if totalBytes > 0 {
				atomic.StoreInt64(&task.TotalBytes, totalBytes)
				downloaded := int64(float64(totalBytes) * overallProgress)
				atomic.StoreInt64(&task.DownloadedBytes, downloaded)
			}
			task.Progress = overallProgress
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

	// 查找实际生成的产物并重命名为最终文件
	produced, pickErr := pickProducedArtifact(task.OutputDir, task.ID)
	if pickErr != nil {
		// 兜底: 产物可能已直接落在最终路径
		if _, statErr := os.Stat(finalPath); statErr != nil {
			return fmt.Errorf("未找到下载产物文件: %s", finalPath)
		}
	} else {
		_ = os.Remove(finalPath) // 覆盖已有
		if err := os.Rename(produced, finalPath); err != nil {
			return fmt.Errorf("重命名产物失败: %w", err)
		}
	}

	// 清理本次任务遗留的 .part / .ytdl 等中间文件
	cleanupTempArtifacts(task.OutputDir, task.ID, "")

	return nil
}

// pickProducedArtifact 从下载目录中挑选真正的合并产物
// 优先精确匹配 taskID_tmp.mp4 / .m4a，其次回退到排除 .part / .ytdl 等中间文件后的最新文件
// (修复历史缺陷: 直接取 glob 首个结果时，.part 因字典序靠前会被误当成最终产物)
func pickProducedArtifact(dir, taskID string) (string, error) {
	for _, ext := range []string{".mp4", ".m4a"} {
		p := filepath.Join(dir, taskID+"_tmp"+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return p, nil
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, taskID+"_tmp.*"))
	var best string
	var bestTime time.Time
	for _, m := range matches {
		lower := strings.ToLower(m)
		if strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".ytdl") || strings.HasSuffix(lower, ".temp") {
			continue
		}
		fi, err := os.Stat(m)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			continue
		}
		if best == "" || fi.ModTime().After(bestTime) {
			best = m
			bestTime = fi.ModTime()
		}
	}
	if best == "" {
		return "", fmt.Errorf("未找到下载产物文件")
	}
	return best, nil
}

// cleanupTempArtifacts 清理指定任务残留的中间文件 (keep 非空时保留该路径)
func cleanupTempArtifacts(dir, taskID, keep string) {
	matches, _ := filepath.Glob(filepath.Join(dir, taskID+"_tmp.*"))
	for _, m := range matches {
		if keep != "" && m == keep {
			continue
		}
		_ = os.Remove(m)
	}
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
