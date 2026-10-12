package library

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMoveToTrashMovesFileIntoHomeTrash 验证文件被移入 $HOME/.Trash
func TestMoveToTrashMovesFileIntoHomeTrash(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "video.mp4")
	if err := os.WriteFile(srcPath, []byte("fake video"), 0644); err != nil {
		t.Fatal(err)
	}

	l := &Library{}
	if err := l.MoveToTrash(srcPath); err != nil {
		t.Fatalf("MoveToTrash 失败: %v", err)
	}

	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("源文件应已被移除, stat err=%v", err)
	}
	dest := filepath.Join(tmpHome, ".Trash", "video.mp4")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("废纸篓中应存在文件: %v", err)
	}
	if string(data) != "fake video" {
		t.Fatalf("内容不一致: %q", data)
	}
}

// TestMoveToTrashAvoidsOverwritingExistingTrashFile 废纸篓已有同名文件时应追加时间戳而非覆盖
func TestMoveToTrashAvoidsOverwritingExistingTrashFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	trashDir := filepath.Join(tmpHome, ".Trash")
	if err := os.MkdirAll(trashDir, 0700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(trashDir, "video.mp4")
	if err := os.WriteFile(existing, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "video.mp4")
	if err := os.WriteFile(srcPath, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	l := &Library{}
	if err := l.MoveToTrash(srcPath); err != nil {
		t.Fatalf("MoveToTrash 失败: %v", err)
	}

	if data, _ := os.ReadFile(existing); string(data) != "old" {
		t.Fatalf("废纸篓中旧文件被覆盖: %q", data)
	}
	matches, _ := filepath.Glob(filepath.Join(trashDir, "video *.mp4"))
	if len(matches) != 1 {
		t.Fatalf("应存在 1 个带时间戳后缀的文件, got %v", matches)
	}
	if data, _ := os.ReadFile(matches[0]); string(data) != "new" {
		t.Fatalf("新文件内容不一致: %q", data)
	}
}

// TestMoveToTrashMissingFileIsNoop 文件不存在时应视为成功 (幂等)
func TestMoveToTrashMissingFileIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	l := &Library{}
	if err := l.MoveToTrash(filepath.Join(t.TempDir(), "nope.mp4")); err != nil {
		t.Fatalf("不存在的文件应静默成功: %v", err)
	}
}
