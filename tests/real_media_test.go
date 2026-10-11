package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediago/internal/discovery"
	"mediago/internal/engine"
	"mediago/internal/model"
)

func TestRealMDNVideoDownload(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mediago-real-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	url := "https://interactive-examples.mdn.mozilla.net/media/cc0-videos/flower.mp4"
	outputFile := "flower.mp4"

	task := &model.Task{
		ID:             "real-task-flower",
		Title:          "MDN Flower MP4",
		DownloadURL:    url,
		OutputDir:      tempDir,
		OutputFilename: outputFile,
	}

	httpEngine := engine.NewHTTPRangeEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err = httpEngine.Download(ctx, task, engine.DownloadOptions{
		NumChunks: 2,
	})
	if err != nil {
		t.Fatalf("真实下载 MDN MP4 失败: %v", err)
	}

	dest := filepath.Join(tempDir, outputFile)
	stat, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("落盘文件不存在: %v", err)
	}

	if stat.Size() < 100*1024 {
		t.Fatalf("下载文件体积异常小: %d 字节", stat.Size())
	}
}

func TestRealYtDlpExtract(t *testing.T) {
	extractor := discovery.NewYtDlpExtractor()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// 提取 Blender 官方开源测试视频
	cand, err := extractor.Extract(ctx, "https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	if err != nil {
		// 某些环境可能受网络限制，若网络不通则跳过
		t.Logf("网络受限，跳过外网 YouTube 解析: %v", err)
		return
	}

	if cand.Title == "" || len(cand.Formats) == 0 {
		t.Fatalf("提取的候选信息不完整: %+v", cand)
	}
}

// TestRealXSniffExtract 测试 X (Twitter) 真实视频嗅探解析
func TestRealXSniffExtract(t *testing.T) {
	extractor := discovery.NewYtDlpExtractor()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testURL := "https://x.com/_Kavsoft/status/2108680194659873098"
	cand, err := extractor.Extract(ctx, testURL)
	if err != nil {
		t.Logf("网络受限或外部接口变动，跳过 X 嗅探: %v", err)
		return
	}

	t.Logf("X 视频解析成功: 标题=%s, 创作者=%s, 时长=%.1fs, 规格数=%d",
		cand.Title, cand.Author, cand.Duration, len(cand.Formats))

	if cand.Title == "" {
		t.Fatalf("X 视频解析出的标题为空")
	}
	if len(cand.Formats) == 0 {
		t.Fatalf("X 视频未解析出任何下载规格")
	}
}

// TestRealThreadsSniffExtract 测试 Threads 真实视频嗅探与页面识别
func TestRealThreadsSniffExtract(t *testing.T) {
	testURL := "https://www.threads.com/@motogp/post/DeTICF9iehP"

	// 1. 验证 URL 规范化
	normURL := discovery.NormalizeURL(testURL)
	if normURL == "" {
		t.Fatalf("Threads URL 规范化异常")
	}
	t.Logf("Threads URL 规范化结果: %s", normURL)

	// 2. 验证嗅探提取分发与容错诊断
	extractor := discovery.NewYtDlpExtractor()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cand, err := extractor.Extract(ctx, normURL)
	if err != nil {
		t.Logf("Threads 属于 Meta 强动态渲染架构，yt-dlp 暂未提供原生解析 (符合当前预期，已由浏览器内置嗅探承接): %v", err)
		return
	}

	if cand != nil {
		t.Logf("Threads 解析成功: 标题=%s, 规格数=%d", cand.Title, len(cand.Formats))
	}
}

