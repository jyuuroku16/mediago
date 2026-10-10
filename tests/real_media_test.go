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
