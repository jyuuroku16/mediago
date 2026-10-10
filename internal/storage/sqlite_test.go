package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediago/internal/model"
)

func TestStorageLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mediago-storage-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	s, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("初始化 Storage 失败: %v", err)
	}
	defer s.Close()

	// 1. 创建并保存任务
	task := &model.Task{
		ID:              "task-001",
		Title:           "测试视频",
		SourceURL:       "https://example.com/video",
		DownloadURL:     "https://example.com/video.mp4",
		Engine:          model.EngineHTTPRange,
		Status:          model.StatusDownloading,
		TotalBytes:      1024 * 1024,
		DownloadedBytes: 512 * 1024,
		Speed:           102400,
		Progress:        0.5,
		OutputDir:       tempDir,
		OutputFilename:  "video.mp4",
		Chunks: []model.Chunk{
			{Index: 0, StartByte: 0, EndByte: 512*1024 - 1, Downloaded: 512 * 1024, Done: true},
			{Index: 1, StartByte: 512 * 1024, EndByte: 1024*1024 - 1, Downloaded: 0, Done: false},
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := s.SaveTask(task); err != nil {
		t.Fatalf("保存任务失败: %v", err)
	}

	// 2. 查询并验证
	fetched, err := s.GetTask("task-001")
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if fetched.Title != task.Title || fetched.Progress != 0.5 || len(fetched.Chunks) != 2 {
		t.Fatalf("查询出的任务数据不符合预期: %+v", fetched)
	}

	// 3. 更新任务状态为 completed
	task.Status = model.StatusCompleted
	task.DownloadedBytes = 1024 * 1024
	task.Progress = 1.0
	task.FinalPath = filepath.Join(tempDir, "video.mp4")
	if err := s.SaveTask(task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	tasks, err := s.ListTasks(model.StatusCompleted)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("按状态查询任务失败: len=%d, err=%v", len(tasks), err)
	}

	// 4. 保存资产 Asset
	asset := &model.Asset{
		ID:             "asset-001",
		TaskID:         task.ID,
		Title:          task.Title,
		SourceURL:      task.SourceURL,
		FilePath:       task.FinalPath,
		FileSize:       task.TotalBytes,
		Duration:       60.5,
		Resolution:     "1080p",
		FileHashSHA256: "fake-sha256",
		Tags:           []string{"测试", "公开"},
		CreatedAt:      time.Now(),
	}
	if err := s.SaveAsset(asset); err != nil {
		t.Fatalf("保存资产失败: %v", err)
	}

	assets, err := s.ListAssets()
	if err != nil || len(assets) != 1 {
		t.Fatalf("查询资产列表失败: len=%d, err=%v", len(assets), err)
	}
	if len(assets[0].Tags) != 2 || assets[0].Tags[0] != "测试" {
		t.Fatalf("资产标签不符合预期: %+v", assets[0].Tags)
	}

	// 5. 删除测试
	if err := s.DeleteTask(task.ID); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if _, err := s.GetTask(task.ID); err == nil {
		t.Fatalf("删除后依然能查到任务")
	}
}
