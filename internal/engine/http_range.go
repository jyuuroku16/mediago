package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mediago/internal/model"
)

// ProgressCallback 进度更新回调
type ProgressCallback func(downloaded int64, total int64, speed int64)

// HTTPRangeEngine 纯 Go HTTP Range 分段下载引擎
type HTTPRangeEngine struct {
	client *http.Client
}

// newStreamingHTTPClient 构造适用于大文件/流式分片下载的 HTTP 客户端
// 注意: 刻意不设置 Client.Timeout 总超时 (30s 总超时会误杀一切大文件下载)，
// 仅约束连接握手与响应头等待时间；传输阶段的停滞由分段下载看门狗负责
func newStreamingHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: transport}
}

// NewHTTPRangeEngine 创建 HTTP 下载引擎
func NewHTTPRangeEngine() *HTTPRangeEngine {
	return &HTTPRangeEngine{
		client: newStreamingHTTPClient(),
	}
}

// ProbeResult 探测结果
type ProbeResult struct {
	ContentLength int64
	AcceptRanges  bool
	ETag          string
	ContentType   string
}

// Probe 探测服务器支持的能力
func (e *HTTPRangeEngine) Probe(ctx context.Context, rawURL string) (*ProbeResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)")

	resp, err := e.client.Do(req)
	// 若服务端禁止 HEAD 或报错，降级为 GET Range 探测
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return e.probeWithGetRange(ctx, rawURL)
	}
	defer resp.Body.Close()

	res := &ProbeResult{
		ContentLength: resp.ContentLength,
		AcceptRanges:  strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes"),
		ETag:          resp.Header.Get("ETag"),
		ContentType:   resp.Header.Get("Content-Type"),
	}

	return res, nil
}

func (e *HTTPRangeEngine) probeWithGetRange(ctx context.Context, rawURL string) (*ProbeResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	res := &ProbeResult{
		ETag:        resp.Header.Get("ETag"),
		ContentType: resp.Header.Get("Content-Type"),
	}

	if resp.StatusCode == http.StatusPartialContent {
		res.AcceptRanges = true
		contentRange := resp.Header.Get("Content-Range")
		// 格式形如: bytes 0-0/102400
		if slashIdx := strings.LastIndex(contentRange, "/"); slashIdx != -1 {
			totalStr := contentRange[slashIdx+1:]
			if total, err := strconv.ParseInt(totalStr, 10, 64); err == nil {
				res.ContentLength = total
			}
		}
	} else if resp.StatusCode == http.StatusOK {
		res.AcceptRanges = false
		res.ContentLength = resp.ContentLength
	}

	return res, nil
}

// DownloadOptions 下载选项
type DownloadOptions struct {
	NumChunks   int
	OnProgress  ProgressCallback
	CustomSpeed int64 // 限速 bytes/s，0 为不限
}

// Download 执行文件下载
func (e *HTTPRangeEngine) Download(ctx context.Context, task *model.Task, opts DownloadOptions) error {
	if err := os.MkdirAll(task.OutputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	partialPath := filepath.Join(task.OutputDir, task.OutputFilename+".partial")
	finalPath := filepath.Join(task.OutputDir, task.OutputFilename)
	task.FinalPath = finalPath

	// 1. 探测
	probe, err := e.Probe(ctx, task.DownloadURL)
	if err != nil {
		return fmt.Errorf("探测资源失败: %w", err)
	}

	task.TotalBytes = probe.ContentLength

	// 2. 判断是否支持 Range 分段
	canRange := probe.AcceptRanges && probe.ContentLength > 512*1024
	numChunks := opts.NumChunks
	if numChunks <= 0 {
		numChunks = 4
	}
	// 大文件自动提升并发分段数，充分利用高带宽链路
	if canRange && probe.ContentLength > 256*1024*1024 && numChunks < 8 {
		numChunks = 8
	}
	if !canRange {
		numChunks = 1
	}

	// 3. 打开或创建 .partial 临时文件
	partialInfo, partialStatErr := os.Stat(partialPath)
	partialExists := partialStatErr == nil

	file, err := os.OpenFile(partialPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("打开临时文件失败: %w", err)
	}
	defer file.Close()

	if task.TotalBytes > 0 {
		_ = file.Truncate(task.TotalBytes)
	}

	// 3.1 断点续传安全校验，避免稀疏写入在产物中留下空洞:
	//   - 服务端不支持 Range: 无法续传，必须从头开始，否则数据错位
	//   - .partial 临时文件不存在: 上次数据已丢失，历史分段进度作废
	//   - 分段进度超出实际文件长度: 回退该分段，防止写入空洞
	if !canRange {
		for i := range task.Chunks {
			task.Chunks[i].Downloaded = 0
			task.Chunks[i].Done = false
		}
	} else if !partialExists {
		for i := range task.Chunks {
			task.Chunks[i].Downloaded = 0
			task.Chunks[i].Done = false
		}
	} else {
		for i := range task.Chunks {
			c := &task.Chunks[i]
			if c.StartByte+c.Downloaded > partialInfo.Size() {
				c.Downloaded = 0
				c.Done = false
			}
		}
	}

	// 4. 初始化或复用 Chunks
	if len(task.Chunks) == 0 {
		task.Chunks = make([]model.Chunk, numChunks)
		chunkSize := task.TotalBytes / int64(numChunks)
		for i := 0; i < numChunks; i++ {
			start := int64(i) * chunkSize
			end := start + chunkSize - 1
			if i == numChunks-1 {
				end = task.TotalBytes - 1
			}
			task.Chunks[i] = model.Chunk{
				Index:      i,
				StartByte:  start,
				EndByte:    end,
				Downloaded: 0,
				Done:       false,
			}
		}
	}

	// 5. 速度计算器（EWMA）
	var totalDownloaded int64
	for _, c := range task.Chunks {
		totalDownloaded += c.Downloaded
	}
	atomic.StoreInt64(&task.DownloadedBytes, totalDownloaded)

	speedTrackerDone := make(chan struct{})
	defer close(speedTrackerDone)

	var lastBytes int64 = totalDownloaded
	var currentSpeed int64
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-speedTrackerDone:
				return
			case <-ticker.C:
				currentTotal := atomic.LoadInt64(&task.DownloadedBytes)
				delta := currentTotal - lastBytes
				lastBytes = currentTotal
				instSpeed := delta * 2 // 0.5s 转为 1s 速度
				if currentSpeed == 0 {
					currentSpeed = instSpeed
				} else {
					// EWMA 平滑
					currentSpeed = int64(0.7*float64(currentSpeed) + 0.3*float64(instSpeed))
				}
				atomic.StoreInt64(&task.Speed, currentSpeed)
				if opts.OnProgress != nil {
					opts.OnProgress(currentTotal, task.TotalBytes, currentSpeed)
				}
			}
		}
	}()

	// 6. 并发或单连接执行分段下载
	var wg sync.WaitGroup
	errCh := make(chan error, numChunks)

	for i := range task.Chunks {
		chunk := &task.Chunks[i]
		if chunk.Done {
			continue
		}

		wg.Add(1)
		go func(c *model.Chunk) {
			defer wg.Done()
			if err := e.downloadChunk(ctx, task.DownloadURL, file, c, canRange, &task.DownloadedBytes); err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}(chunk)
	}

	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}

	// 7. 检查 context 是否已取消
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 8. 校验所有 Chunk 是否完成
	for _, c := range task.Chunks {
		if !c.Done && canRange {
			return fmt.Errorf("分段 %d 未完成", c.Index)
		}
	}

	// 9. 关闭文件句柄后原子重命名
	_ = file.Close()
	if err := os.Rename(partialPath, finalPath); err != nil {
		return fmt.Errorf("原子完成重命名文件失败: %w", err)
	}

	return nil
}

// downloadChunk 下载单个分段，内置断点重试 (最多 3 次) 与网络停滞看门狗
func (e *HTTPRangeEngine) downloadChunk(
	ctx context.Context,
	rawURL string,
	file *os.File,
	chunk *model.Chunk,
	useRange bool,
	downloadedTotal *int64,
) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = e.downloadChunkOnce(ctx, rawURL, file, chunk, useRange, downloadedTotal)
		if lastErr == nil {
			return nil
		}
		// 用户主动取消/暂停时不再重试
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 800 * time.Millisecond):
		}
	}
	return lastErr
}

func (e *HTTPRangeEngine) downloadChunkOnce(
	ctx context.Context,
	rawURL string,
	file *os.File,
	chunk *model.Chunk,
	useRange bool,
	downloadedTotal *int64,
) error {
	// 独立子 context: 网络停滞时看门狗只中断本次分段请求，不影响整体任务
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)")

	curStart := chunk.StartByte + chunk.Downloaded
	if useRange {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", curStart, chunk.EndByte))
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("服务端返回异常状态码: %d", resp.StatusCode)
	}
	if useRange && resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("服务端返回非预期状态码: %d", resp.StatusCode)
	}
	if useRange && resp.StatusCode == http.StatusOK && curStart > 0 {
		return fmt.Errorf("服务端忽略了 Range 请求，无法断点续传分段 %d", chunk.Index+1)
	}

	// 网络停滞看门狗: 连续 30 秒无任何数据到达则中断本次请求，交由外层重试
	var lastActivity int64 = time.Now().UnixNano()
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, atomic.LoadInt64(&lastActivity))) > 30*time.Second {
					cancel()
					return
				}
			}
		}
	}()

	buf := make([]byte, 32*1024)
	offset := curStart

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			atomic.StoreInt64(&lastActivity, time.Now().UnixNano())
			_, wErr := file.WriteAt(buf[:n], offset)
			if wErr != nil {
				return wErr
			}
			offset += int64(n)
			chunk.Downloaded += int64(n)
			atomic.AddInt64(downloadedTotal, int64(n))
		}

		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			// 看门狗触发的取消: 转换为可读的错误信息
			if reqCtx.Err() != nil && ctx.Err() == nil {
				return fmt.Errorf("网络停滞超过 30 秒，分段 %d 下载中断", chunk.Index+1)
			}
			return rErr
		}
	}

	if !useRange || offset > chunk.EndByte {
		chunk.Done = true
	}

	return nil
}

// ExtractHost 从 URL 中提取域名
func ExtractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
