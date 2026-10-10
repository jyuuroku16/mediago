package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediago/internal/model"
	"mediago/internal/storage"
	"mediago/internal/testmedia"
)

func TestHTTPRangeDownload(t *testing.T) {
	srv := testmedia.NewServer()
	defer srv.Close()

	tempDir, err := os.MkdirTemp("", "mediago-engine-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	engine := NewHTTPRangeEngine()

	task := &model.Task{
		ID:             "test-task-range",
		Title:          "Range测试视频",
		DownloadURL:    srv.URL() + "/mp4/range",
		OutputDir:      tempDir,
		OutputFilename: "range_output.mp4",
	}

	opts := DownloadOptions{
		NumChunks: 4,
	}

	err = engine.Download(context.Background(), task, opts)
	if err != nil {
		t.Fatalf("Range 下载失败: %v", err)
	}

	// 校验产物哈希
	outBytes, err := os.ReadFile(filepath.Join(tempDir, "range_output.mp4"))
	if err != nil {
		t.Fatalf("读取输出文件失败: %v", err)
	}
	h := sha256.Sum256(outBytes)
	if hex.EncodeToString(h[:]) != srv.DataSHA256 {
		t.Fatalf("下载文件与源数据 SHA256 哈希不一致")
	}
}

func TestNoRangeDownload(t *testing.T) {
	srv := testmedia.NewServer()
	defer srv.Close()

	tempDir, err := os.MkdirTemp("", "mediago-engine-norange-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	engine := NewHTTPRangeEngine()

	task := &model.Task{
		ID:             "test-task-norange",
		Title:          "无Range测试视频",
		DownloadURL:    srv.URL() + "/mp4/no-range",
		OutputDir:      tempDir,
		OutputFilename: "norange_output.mp4",
	}

	err = engine.Download(context.Background(), task, DownloadOptions{NumChunks: 4})
	if err != nil {
		t.Fatalf("无 Range 下载失败: %v", err)
	}

	outBytes, err := os.ReadFile(filepath.Join(tempDir, "norange_output.mp4"))
	if err != nil {
		t.Fatalf("读取输出文件失败: %v", err)
	}
	h := sha256.Sum256(outBytes)
	if hex.EncodeToString(h[:]) != srv.DataSHA256 {
		t.Fatalf("下载文件与源数据 SHA256 哈希不一致")
	}
}

func TestSchedulerLifecycle(t *testing.T) {
	srv := testmedia.NewServer()
	defer srv.Close()

	tempDir, err := os.MkdirTemp("", "mediago-scheduler-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "scheduler.db")
	st, err := storage.NewStorage(dbPath)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	defer st.Close()

	sc := NewScheduler(st, 2)
	sc.Start()
	defer sc.Stop()

	completedCh := make(chan string, 1)
	sc.OnUpdate(func(task *model.Task) {
		if task.Status == model.StatusCompleted {
			select {
			case completedCh <- task.ID:
			default:
			}
		}
	})

	task := &model.Task{
		ID:             "sched-task-01",
		Title:          "调度测试视频",
		DownloadURL:    srv.URL() + "/mp4/range",
		OutputDir:      tempDir,
		OutputFilename: "sched_output.mp4",
		Engine:         model.EngineHTTPRange,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := sc.Submit(task); err != nil {
		t.Fatalf("提交任务失败: %v", err)
	}

	select {
	case id := <-completedCh:
		if id != "sched-task-01" {
			t.Fatalf("完成的任务 ID 不匹配: %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("调度器执行任务超时")
	}

	// 验证最终落盘文件
	if _, err := os.Stat(filepath.Join(tempDir, "sched_output.mp4")); err != nil {
		t.Fatalf("最终文件不存在: %v", err)
	}
}


func TestHLSResolveSegments(t *testing.T) {
	srv := testmedia.NewServer()
	defer srv.Close()

	engine := NewHLSEngine()
	segments, err := engine.resolveSegments(context.Background(), srv.URL()+"/hls/master.m3u8")
	if err != nil {
		t.Fatalf("解析 HLS Master 失败: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("期望解析出 2 个分片，实际为 %d", len(segments))
	}
}
