package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mediago/internal/discovery"
	"mediago/internal/model"
)

// HLSEngine HLS 流媒体下载与合并引擎
type HLSEngine struct {
	client     *http.Client
	ffmpegPath string
}

// NewHLSEngine 创建 HLS 引擎，自动查找 ffmpeg 路径
func NewHLSEngine() *HLSEngine {
	return &HLSEngine{
		client:     &http.Client{Timeout: 30 * time.Second},
		ffmpegPath: discovery.FindFFmpeg(),
	}
}

// Download 下载 HLS 流并合并为最终视频
func (h *HLSEngine) Download(ctx context.Context, task *model.Task, opts DownloadOptions) error {
	if err := os.MkdirAll(task.OutputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	finalPath := filepath.Join(task.OutputDir, task.OutputFilename)
	task.FinalPath = finalPath

	// 临时目录存放分片
	tmpDir, err := os.MkdirTemp(task.OutputDir, "hls_tmp_*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. 获取并解析媒体播放列表中的分片 URL 列表
	segments, err := h.resolveSegments(ctx, task.DownloadURL)
	if err != nil {
		return fmt.Errorf("解析 HLS 播放列表失败: %w", err)
	}
	if len(segments) == 0 {
		return fmt.Errorf("HLS 播放列表中未找到任何分片")
	}

	totalSegments := int64(len(segments))
	task.TotalBytes = totalSegments * 64 * 1024 // 估算字节数
	var downloadedCount int64

	// 2. 并发下载分片
	maxConcurrent := 4
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errCh := make(chan error, len(segments))

	segFiles := make([]string, len(segments))

	for i, segURL := range segments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		segFilePath := filepath.Join(tmpDir, fmt.Sprintf("seg_%05d.ts", i))
		segFiles[i] = segFilePath

		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, targetURL, outPath string) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := h.downloadFile(ctx, targetURL, outPath); err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}

			done := atomic.AddInt64(&downloadedCount, 1)
			atomic.StoreInt64(&task.DownloadedBytes, done*64*1024)
			if opts.OnProgress != nil {
				opts.OnProgress(done, totalSegments, 0)
			}
		}(i, segURL, segFilePath)
	}

	wg.Wait()

	select {
	case err := <-errCh:
		return fmt.Errorf("分片下载失败: %w", err)
	default:
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 3. 生成 concat 文件列表
	concatListFile := filepath.Join(tmpDir, "concat.txt")
	f, err := os.Create(concatListFile)
	if err != nil {
		return fmt.Errorf("创建 concat 列表文件失败: %w", err)
	}
	for _, segFile := range segFiles {
		absPath, _ := filepath.Abs(segFile)
		_, _ = fmt.Fprintf(f, "file '%s'\n", absPath)
	}
	_ = f.Close()

	// 4. 调用 ffmpeg 进行无损 remux 封装
	if h.ffmpegPath == "" {
		h.ffmpegPath = discovery.FindFFmpeg()
	}
	if h.ffmpegPath == "" {
		return fmt.Errorf("系统未找到可用的 FFmpeg 工具，无法合并分片。请在偏好设置中一键下载安装")
	}
	cmd := exec.CommandContext(ctx, h.ffmpegPath, "-y", "-f", "concat", "-safe", "0", "-i", concatListFile, "-c", "copy", finalPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg 合并失败 (%v): %s", err, string(output))
	}

	return nil
}

func (h *HLSEngine) resolveSegments(ctx context.Context, playlistURL string) ([]string, error) {
	lines, err := h.fetchLines(ctx, playlistURL)
	if err != nil {
		return nil, err
	}

	isMaster := false
	for _, l := range lines {
		if strings.HasPrefix(l, "#EXT-X-STREAM-INF") {
			isMaster = true
			break
		}
	}

	if isMaster {
		// 是 master playlist，找到第一个子 playlist URL
		for i, l := range lines {
			if strings.HasPrefix(l, "#EXT-X-STREAM-INF") && i+1 < len(lines) {
				variantURL := lines[i+1]
				resolvedVariant := resolveRelativeURL(playlistURL, variantURL)
				return h.resolveSegments(ctx, resolvedVariant)
			}
		}
	}

	// 是 media playlist，提取分片
	var segments []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		segments = append(segments, resolveRelativeURL(playlistURL, trimmed))
	}

	return segments, nil
}

func (h *HLSEngine) fetchLines(ctx context.Context, u string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("请求播放列表失败，状态码: %d", resp.StatusCode)
	}

	var lines []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		lines = append(lines, strings.TrimSpace(scanner.Text()))
	}
	return lines, scanner.Err()
}

func (h *HLSEngine) downloadFile(ctx context.Context, u, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)")

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("下载分片状态码异常: %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func resolveRelativeURL(baseURL, targetURL string) string {
	b, err := url.Parse(baseURL)
	if err != nil {
		return targetURL
	}
	t, err := url.Parse(targetURL)
	if err != nil {
		return targetURL
	}
	return b.ResolveReference(t).String()
}
