package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mediago/internal/discovery"
)

// Library 本地媒体资源库管理器
type Library struct {
	baseDir    string
	thumbDir   string
	ffmpegPath string
	thumbCache sync.Map // map[string]*ui.Bitmap
	inProgress sync.Map // map[string]bool
}

// NewLibrary 初始化媒体库
func NewLibrary(dir ...string) (*Library, error) {
	base := ""
	if len(dir) > 0 && dir[0] != "" {
		base = dir[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, "Movies", "MediaGo")
	}

	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, fmt.Errorf("创建媒体库目录失败: %w", err)
	}

	home, _ := os.UserHomeDir()
	thumbDir := filepath.Join(home, ".mediago", "thumbnails")
	_ = os.MkdirAll(thumbDir, 0755)

	return &Library{
		baseDir:    base,
		thumbDir:   thumbDir,
		ffmpegPath: discovery.FindFFmpeg(),
	}, nil
}

// GetThumbnail 获取指定视频文件的首帧预览图 (16:9 比例)
// 若内存或本地磁盘已有缓存则直接返回 *ui.Bitmap，若未就绪则在后台异步提取并触发 onReady 回调
func (l *Library) GetThumbnail(filePath string, onReady func()) *ui.Bitmap {
	if filePath == "" {
		return nil
	}

	// 1. 检查内存缓存
	if val, ok := l.thumbCache.Load(filePath); ok {
		if bm, ok := val.(*ui.Bitmap); ok {
			return bm
		}
	}

	// 2. 检查源文件是否存在
	if _, err := os.Stat(filePath); err != nil {
		return nil
	}

	// 3. 计算磁盘缩略图缓存路径
	h := sha256.Sum256([]byte(filePath))
	cacheKey := hex.EncodeToString(h[:16])
	cacheFile := filepath.Join(l.thumbDir, fmt.Sprintf("%s_16x9.png", cacheKey))

	// 4. 若磁盘已存在缩略图，读取并解码
	if _, err := os.Stat(cacheFile); err == nil {
		if data, err := os.ReadFile(cacheFile); err == nil {
			if bm, err := ui.DecodeBitmap(data); err == nil {
				l.thumbCache.Store(filePath, bm)
				return bm
			}
		}
	}

	// 5. 若已在异步提取队列中，避免重复触发
	if _, loaded := l.inProgress.LoadOrStore(filePath, true); loaded {
		return nil
	}

	// 6. 后台异步调用 ffmpeg 抽取首帧并填充为严格 16:9 (320x180)
	go func() {
		defer l.inProgress.Delete(filePath)

		ff := l.ffmpegPath
		if ff == "" {
			ff = discovery.FindFFmpeg()
		}
		if ff == "" {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, ff,
			"-ss", "00:00:00",
			"-i", filePath,
			"-vframes", "1",
			"-update", "1",
			"-vf", "scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2:black",
			"-y", cacheFile,
		)
		if err := cmd.Run(); err != nil {
			return
		}

		if data, err := os.ReadFile(cacheFile); err == nil {
			if bm, err := ui.DecodeBitmap(data); err == nil {
				l.thumbCache.Store(filePath, bm)
				if onReady != nil {
					onReady()
				}
			}
		}
	}()

	return nil
}

// BaseDir 获取存储根路径
func (l *Library) BaseDir() string {
	return l.baseDir
}

// SetBaseDir 动态更新媒体库根存储路径
func (l *Library) SetBaseDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("目录路径不能为空")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	l.baseDir = dir
	return nil
}

// OpenInFinder 在 macOS Finder 中定位并高亮选定文件
func (l *Library) OpenInFinder(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	return exec.Command("open", "-R", filePath).Run()
}

// QuickLook 使用 macOS 系统原生播放器打开媒体 (不调用 qlmanage 调试工具)
func (l *Library) QuickLook(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	return exec.Command("open", filePath).Start()
}

// MoveToTrash 将文件安全移动到 macOS 废纸篓
func (l *Library) MoveToTrash(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return nil
	}
	// 利用 AppleScript 安全移入废纸篓
	script := fmt.Sprintf(`tell application "Finder" to delete POSIX file "%s"`, filePath)
	return exec.Command("osascript", "-e", script).Run()
}
