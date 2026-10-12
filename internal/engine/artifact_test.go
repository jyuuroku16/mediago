package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediago/internal/model"
	"mediago/internal/storage"
)

// TestPickProducedArtifactPrefersMergedFile 验证产物挑选不会误选 .part 中间文件
// (历史缺陷: glob 首个结果中 ".part" 字典序排在 ".mp4" 之前，会把未完成的碎片当成最终产物)
func TestPickProducedArtifactPrefersMergedFile(t *testing.T) {
	dir := t.TempDir()
	id := "task-abc"

	partPath := filepath.Join(dir, id+"_tmp.f137.mp4.part")
	if err := os.WriteFile(partPath, []byte("partial-data"), 0644); err != nil {
		t.Fatal(err)
	}
	mergedPath := filepath.Join(dir, id+"_tmp.mp4")
	if err := os.WriteFile(mergedPath, []byte("merged-final"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := pickProducedArtifact(dir, id)
	if err != nil {
		t.Fatalf("挑选产物失败: %v", err)
	}
	if got != mergedPath {
		t.Fatalf("应优先选择合并产物 %s，实际选中 %s", mergedPath, got)
	}
}

// TestPickProducedArtifactFallback 验证没有精确匹配时回退到最新的非中间文件
func TestPickProducedArtifactFallback(t *testing.T) {
	dir := t.TempDir()
	id := "task-def"

	partPath := filepath.Join(dir, id+"_tmp.f137.mp4.part")
	if err := os.WriteFile(partPath, []byte("partial-data"), 0644); err != nil {
		t.Fatal(err)
	}
	webmPath := filepath.Join(dir, id+"_tmp.webm")
	if err := os.WriteFile(webmPath, []byte("webm-final"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := pickProducedArtifact(dir, id)
	if err != nil {
		t.Fatalf("挑选产物失败: %v", err)
	}
	if got != webmPath {
		t.Fatalf("应回退选择 %s，实际选中 %s", webmPath, got)
	}

	if _, err := pickProducedArtifact(dir, "no-such-task"); err == nil {
		t.Fatalf("不存在的任务应返回错误")
	}
}

// TestCleanupTempArtifacts 验证中间文件清理逻辑
func TestCleanupTempArtifacts(t *testing.T) {
	dir := t.TempDir()
	id := "task-ghi"

	partPath := filepath.Join(dir, id+"_tmp.f137.mp4.part")
	ytdlPath := filepath.Join(dir, id+"_tmp.f137.mp4.ytdl")
	otherPath := filepath.Join(dir, "other-file.mp4")
	for _, p := range []string{partPath, ytdlPath, otherPath} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cleanupTempArtifacts(dir, id, "")

	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Fatalf(".part 中间文件应被清理")
	}
	if _, err := os.Stat(ytdlPath); !os.IsNotExist(err) {
		t.Fatalf(".ytdl 中间文件应被清理")
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("无关文件不应被误删: %v", err)
	}
}

// TestReconcileOnStartup 验证启动时中断任务的状态修复 (不自动续传时转为暂停)
func TestReconcileOnStartup(t *testing.T) {
	dir := t.TempDir()
	st, err := storage.NewStorage(filepath.Join(dir, "reconcile.db"))
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	defer st.Close()

	now := time.Now()
	stuck := &model.Task{
		ID: "stuck-1", Title: "中断任务", DownloadURL: "https://example.com/x.mp4",
		Engine: model.EngineHTTPRange, Status: model.StatusDownloading,
		OutputDir: dir, OutputFilename: "x.mp4", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.SaveTask(stuck); err != nil {
		t.Fatal(err)
	}
	done := &model.Task{
		ID: "done-1", Title: "已完成任务", DownloadURL: "https://example.com/y.mp4",
		Engine: model.EngineHTTPRange, Status: model.StatusCompleted,
		OutputDir: dir, OutputFilename: "y.mp4", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.SaveTask(done); err != nil {
		t.Fatal(err)
	}

	sc := NewScheduler(st, 2)
	sc.ReconcileOnStartup(false)

	got, err := st.GetTask("stuck-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusPaused {
		t.Fatalf("中断任务应被置为暂停，实际为 %s", got.Status)
	}
	if got.ErrorMsg == "" {
		t.Fatalf("中断任务应带有提示信息")
	}

	doneTask, err := st.GetTask("done-1")
	if err != nil {
		t.Fatal(err)
	}
	if doneTask.Status != model.StatusCompleted {
		t.Fatalf("已完成任务不应被改动，实际为 %s", doneTask.Status)
	}
}
