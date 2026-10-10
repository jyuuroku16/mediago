package model

import (
	"time"
)

// TaskStatus 定义下载任务状态机状态
type TaskStatus string

const (
	StatusCreated         TaskStatus = "created"
	StatusResolving       TaskStatus = "resolving"
	StatusReady           TaskStatus = "ready"
	StatusQueued          TaskStatus = "queued"
	StatusDownloading     TaskStatus = "downloading"
	StatusVerifying       TaskStatus = "verifying"
	StatusProcessing      TaskStatus = "processing"
	StatusCompleted       TaskStatus = "completed"
	StatusPaused          TaskStatus = "paused"
	StatusFailed          TaskStatus = "failed"
	StatusCancelled       TaskStatus = "cancelled"
	StatusRetryWait       TaskStatus = "retry_wait"
	StatusWaitingForLogin TaskStatus = "waiting_for_login"
)

// EngineType 定义所采用的下载引擎
type EngineType string

const (
	EngineHTTPRange EngineType = "http_range"
	EngineHLS       EngineType = "hls"
	EngineDirect    EngineType = "direct"
	EngineExtractor EngineType = "extractor_worker"
)

// MediaFormat 表示媒体规格（清晰度、编码、格式等）
type MediaFormat struct {
	FormatID   string `json:"format_id"`
	Resolution string `json:"resolution"` // 例如 1080p, 720p
	Ext        string `json:"ext"`        // mp4, ts, mkv
	VCodec     string `json:"vcodec"`
	ACodec     string `json:"acodec"`
	Filesize   int64  `json:"filesize"`
	URL        string `json:"url"`
	Protocol   string `json:"protocol"` // http, m3u8, etc.
	Note       string `json:"note"`     // 备注说明
}

// Episode 表示视频的一个分集（如 B 站分 P 或播放列表单集）
type Episode struct {
	Index    int    `json:"index"`    // 1, 2, ..., N
	CID      int64  `json:"cid"`      // 平台内部 CID（若有）
	Title    string `json:"title"`    // 分集标题，例如 "第五課"
	Duration int64  `json:"duration"` // 时长（秒）
	URL      string `json:"url"`      // 该分集独立 URL
}

// Candidate 表示媒体解析候选
type Candidate struct {
	ID          string        `json:"id"`
	SourceURL   string        `json:"source_url"`
	Title       string        `json:"title"`
	Author      string        `json:"author"`
	CoverURL    string        `json:"cover_url"`
	Duration    float64       `json:"duration"` // 秒
	Formats     []MediaFormat `json:"formats"`
	Selected    int           `json:"selected_format_index"`
	Episodes    []Episode     `json:"episodes,omitempty"`     // 多分集列表
	CurrentPage int           `json:"current_page,omitempty"` // 当前分集序号 (1-indexed)
	CreatedAt   time.Time     `json:"created_at"`
}

// Chunk 表示 HTTP Range 分段信息
type Chunk struct {
	Index      int   `json:"index"`
	StartByte  int64 `json:"start_byte"`
	EndByte    int64 `json:"end_byte"`
	Downloaded int64 `json:"downloaded"`
	Done       bool  `json:"done"`
}

// Task 表示一个下载任务
type Task struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	SourceURL       string     `json:"source_url"`
	DownloadURL     string     `json:"download_url"`
	CoverURL        string     `json:"cover_url"`
	Engine          EngineType `json:"engine"`
	Status          TaskStatus `json:"status"`
	TotalBytes      int64      `json:"total_bytes"`
	DownloadedBytes int64      `json:"downloaded_bytes"`
	Speed           int64      `json:"speed"` // bytes/s (EWMA)
	Progress        float64    `json:"progress"` // 0.0 ~ 1.0
	OutputDir       string     `json:"output_dir"`
	OutputFilename  string     `json:"output_filename"`
	FinalPath       string     `json:"final_path"`
	ErrorMsg        string     `json:"error_msg"`
	RetryCount      int        `json:"retry_count"`
	Priority        int        `json:"priority"` // 越大优先级越高
	Chunks          []Chunk    `json:"chunks"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Asset 表示已下载完成并归档到媒体库的资源记录
type Asset struct {
	ID             string    `json:"id"`
	TaskID         string    `json:"task_id"`
	Title          string    `json:"title"`
	SourceURL      string    `json:"source_url"`
	FilePath       string    `json:"file_path"`
	FileSize       int64     `json:"file_size"`
	Duration       float64   `json:"duration"`
	Resolution     string    `json:"resolution"`
	FileHashSHA256 string    `json:"file_hash_sha256"`
	Tags           []string  `json:"tags"`
	CreatedAt      time.Time `json:"created_at"`
}
