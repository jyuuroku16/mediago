package tests

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"mediago/internal/discovery"
	"mediago/internal/engine"
	"mediago/internal/model"
	"mediago/internal/storage"
)

func TestBilibiliBatchEpisodesFlow(t *testing.T) {
	// 1. 初始化独立测试环境
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "bili_test.db")
	store, err := storage.NewStorage(dbPath)
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()

	scheduler := engine.NewScheduler(store, 3)
	scheduler.Start()
	defer scheduler.Stop()

	// 2. 解析 B 站多 P 视频
	extractor := discovery.NewYtDlpExtractor()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	targetURL := "https://www.bilibili.com/video/BV1xA411b7ri/?spm_id_from=333.788.player.switch&vd_source=fa2ce602313887c5a6607aee51e857a1&p=5"
	cand, err := extractor.Extract(ctx, targetURL)
	if err != nil {
		t.Fatalf("提取视频候选失败: %v", err)
	}

	if len(cand.Episodes) != 41 {
		t.Fatalf("预期分集数量为 41，实际为 %d", len(cand.Episodes))
	}
	if cand.CurrentPage != 5 {
		t.Fatalf("预期当前分集为 5，实际为 %d", cand.CurrentPage)
	}
	if cand.Episodes[4].Title != "第五課" {
		t.Fatalf("预期第 5 集标题为 '第五課'，实际为 '%s'", cand.Episodes[4].Title)
	}

	t.Logf("成功解析视频: %s", cand.Title)
	t.Logf("检测到总分集: %d 集, 当前分集: P%d (%s)", len(cand.Episodes), cand.CurrentPage, cand.Episodes[4].Title)

	// 3. 模拟批量提交 41 集任务
	fmtObj := cand.Formats[0]
	for _, ep := range cand.Episodes {
		taskTitle := fmt.Sprintf("%s [P%02d %s]", cand.Title, ep.Index, ep.Title)
		filename := fmt.Sprintf("%s.%s", taskTitle, fmtObj.Ext)
		task := &model.Task{
			ID:             fmt.Sprintf("task-p%02d", ep.Index),
			Title:          taskTitle,
			SourceURL:      ep.URL,
			DownloadURL:    fmtObj.FormatID,
			Engine:         model.EngineExtractor,
			Status:         model.StatusQueued,
			OutputDir:      tmpDir,
			OutputFilename: filename,
			Priority:       1,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}
		if err := store.SaveTask(task); err != nil {
			t.Fatalf("任务入库失败 (P%d): %v", ep.Index, err)
		}
	}

	// 4. 验证数据库任务持久化
	tasks, err := store.ListTasks("")
	if err != nil {
		t.Fatalf("查询任务列表失败: %v", err)
	}
	if len(tasks) != 41 {
		t.Fatalf("预期数据库包含 41 个任务，实际包含 %d", len(tasks))
	}

	t.Logf("数据库成功入库全部分集任务，共 %d 个", len(tasks))
	t.Logf("任务 1: %s (URL: %s)", tasks[0].Title, tasks[0].SourceURL)
	t.Logf("任务 5: %s (URL: %s)", tasks[4].Title, tasks[4].SourceURL)
	t.Logf("任务 41: %s (URL: %s)", tasks[40].Title, tasks[40].SourceURL)
}
