package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"mediago/internal/model"
)

// YtDlpExtractor 基于 yt-dlp 的视频元数据解析器
type YtDlpExtractor struct {
	binPath string
}

// NewYtDlpExtractor 初始化解析器并探测可用二进制路径
func NewYtDlpExtractor() *YtDlpExtractor {
	candidatePaths := []string{
		"/Users/wuzhiying/.pyenv/shims/yt-dlp",
		"/opt/homebrew/bin/yt-dlp",
		"/usr/local/bin/yt-dlp",
	}

	selected := ""
	for _, p := range candidatePaths {
		if _, err := os.Stat(p); err == nil {
			selected = p
			break
		}
	}
	if selected == "" {
		if p, err := exec.LookPath("yt-dlp"); err == nil {
			selected = p
		}
	}

	return &YtDlpExtractor{binPath: selected}
}

type ytdlpFormat struct {
	FormatID   string  `json:"format_id"`
	Ext        string  `json:"ext"`
	Resolution string  `json:"resolution"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	VCodec     string  `json:"vcodec"`
	ACodec     string  `json:"acodec"`
	Filesize   int64   `json:"filesize"`
	URL        string  `json:"url"`
	Protocol   string  `json:"protocol"`
	FormatNote string  `json:"format_note"`
}

type ytdlpOutput struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Uploader  string        `json:"uploader"`
	Thumbnail string        `json:"thumbnail"`
	Duration  float64       `json:"duration"`
	Formats   []ytdlpFormat `json:"formats"`
	URL       string        `json:"url"`
	Ext       string        `json:"ext"`
}

// Extract 解析目标 URL 产出候选媒体信息
func (e *YtDlpExtractor) Extract(ctx context.Context, rawURL string) (*model.Candidate, error) {
	normURL := NormalizeURL(rawURL)

	// 1. 直链快捷处理
	if isDirect, ext := IsDirectMediaURL(normURL); isDirect {
		proto := "http"
		if ext == "m3u8" {
			proto = "m3u8"
		}
		filename := filepath.Base(normURL)
		return &model.Candidate{
			ID:        uuid.New().String(),
			SourceURL: normURL,
			Title:     filename,
			Author:    "Direct Media",
			CoverURL:  "",
			Duration:  0,
			Formats: []model.MediaFormat{
				{
					FormatID:   "direct",
					Resolution: "Original",
					Ext:        ext,
					URL:        normURL,
					Protocol:   proto,
					Note:       "Direct Link",
				},
			},
			Selected:  0,
			CreatedAt: time.Now(),
		}, nil
	}

	if e.binPath == "" {
		return nil, fmt.Errorf("系统未找到可用的 yt-dlp 可执行文件")
	}

	// 2. 调用 yt-dlp
	cmd := exec.CommandContext(ctx, e.binPath, "--dump-single-json", "--no-warnings", "--no-playlist", normURL)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp 解析失败: %w", err)
	}

	var data ytdlpOutput
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("解析 yt-dlp JSON 响应失败: %w", err)
	}

	candidate := &model.Candidate{
		ID:        uuid.New().String(),
		SourceURL: normURL,
		Title:     data.Title,
		Author:    data.Uploader,
		CoverURL:  data.Thumbnail,
		Duration:  data.Duration,
		CreatedAt: time.Now(),
	}

	for _, f := range data.Formats {
		// 过滤掉纯音频或无效格式
		if f.URL == "" {
			continue
		}
		res := f.Resolution
		if res == "" && f.Height > 0 {
			res = fmt.Sprintf("%dp", f.Height)
		}
		if res == "" {
			res = "Default"
		}

		candidate.Formats = append(candidate.Formats, model.MediaFormat{
			FormatID:   f.FormatID,
			Resolution: res,
			Ext:        f.Ext,
			VCodec:     f.VCodec,
			ACodec:     f.ACodec,
			Filesize:   f.Filesize,
			URL:        f.URL,
			Protocol:   f.Protocol,
			Note:       f.FormatNote,
		})
	}

	// 如果 formats 为空，但有单顶层 URL
	if len(candidate.Formats) == 0 && data.URL != "" {
		candidate.Formats = append(candidate.Formats, model.MediaFormat{
			FormatID:   "default",
			Resolution: "Default",
			Ext:        data.Ext,
			URL:        data.URL,
			Protocol:   "http",
		})
	}

	return candidate, nil
}
