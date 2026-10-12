package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// MoveToTrash 将文件移入系统废纸篓
// 采用纯 Go 文件移动而非 Finder AppleScript: 既避免触发 macOS 自动化权限弹窗，
// 也彻底消除文件路径拼接进脚本的注入风险
func (l *Library) MoveToTrash(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("无法定位用户主目录: %w", err)
	}
	trashDir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trashDir, 0700); err != nil {
		return fmt.Errorf("无法访问废纸篓目录: %w", err)
	}

	dest := filepath.Join(trashDir, filepath.Base(filePath))
	if _, err := os.Stat(dest); err == nil {
		// 废纸篓中已有同名文件，附加时间戳避免覆盖旧文件
		ext := filepath.Ext(dest)
		stem := strings.TrimSuffix(filepath.Base(dest), ext)
		dest = filepath.Join(trashDir, fmt.Sprintf("%s %s%s", stem, time.Now().Format("2006-01-02 15.04.05"), ext))
	}

	if err := os.Rename(filePath, dest); err != nil {
		// 跨卷场景 (下载目录位于外置磁盘等) 无法直接重命名，退化为复制后删除
		if copyErr := copyFileToTrash(filePath, dest); copyErr != nil {
			return fmt.Errorf("移入废纸篓失败: %w", err)
		}
	}
	return nil
}

// copyFileToTrash 跨卷场景的废纸篓回退实现: 复制到目标位置后删除源文件
func copyFileToTrash(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return os.Remove(src)
}
