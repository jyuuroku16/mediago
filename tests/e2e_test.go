package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediago/internal/engine"
	"mediago/internal/model"
	"mediago/internal/storage"
	"mediago/internal/testmedia"
)

func TestFullDownloadLifecycleE2E(t *testing.T) {
	srv := testmedia.NewServer()
	defer srv.Close()

	tempDir, err := os.MkdirTemp("", "mediago-e2e-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "e2e.db")
	store, err := storage.NewStorage(dbPath)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	defer store.Close()

	scheduler := engine.NewScheduler(store, 2)
	scheduler.Start()
	defer scheduler.Stop()

	doneCh := make(chan struct{}, 1)
	scheduler.OnUpdate(func(task *model.Task) {
		if task.Status == model.StatusCompleted {
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})

	outputFilename := "e2e_test_video.mp4"
	task := &model.Task{
		ID:             "e2e-task-1",
		Title:          "E2E 自动化测试视频",
		SourceURL:      srv.URL() + "/mp4/range",
		DownloadURL:    srv.URL() + "/mp4/range",
		Engine:         model.EngineHTTPRange,
		Status:         model.StatusQueued,
		OutputDir:      tempDir,
		OutputFilename: outputFilename,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := scheduler.Submit(task); err != nil {
		t.Fatalf("提交下载任务失败: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("E2E 下载超时")
	}

	// 1. 验证落盘文件与哈希
	finalPath := filepath.Join(tempDir, outputFilename)
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}

	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != srv.DataSHA256 {
		t.Fatalf("文件校验失败：哈希不匹配")
	}

	// 2. 验证 Asset 资源库记录
	assets, err := store.ListAssets()
	if err != nil || len(assets) == 0 {
		t.Fatalf("未成功写入资产库记录: %v", err)
	}
	if assets[0].Title != task.Title {
		t.Fatalf("资产标题不匹配: %s", assets[0].Title)
	}

	// 3. 验证任务状态
	tRecord, err := store.GetTask(task.ID)
	if err != nil || tRecord.Status != model.StatusCompleted {
		t.Fatalf("数据库中的任务状态未标记为 completed")
	}
}
