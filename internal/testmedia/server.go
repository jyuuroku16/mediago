package testmedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Server 封装用于自动化测试的确定性本地媒体服务器
type Server struct {
	HTTPServer *httptest.Server
	DataBytes  []byte
	DataSHA256 string
	DataSize   int64

	// 模拟 429 计数器
	retry429Count int32
}

// NewServer 创建并启动测试服务器
func NewServer() *Server {
	// 构造 2MB 的确定性测试数据（具有重复固定模式）
	size := 2 * 1024 * 1024
	data := make([]byte, size)
	for i := 0; i < size; i++ {
		data[i] = byte((i * 31) % 256)
	}
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	s := &Server{
		DataBytes:  data,
		DataSHA256: hashStr,
		DataSize:   int64(size),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mp4/range", s.handleMP4Range)
	mux.HandleFunc("/mp4/no-range", s.handleMP4NoRange)
	mux.HandleFunc("/hls/master.m3u8", s.handleHLSMaster)
	mux.HandleFunc("/hls/index.m3u8", s.handleHLSIndex)
	mux.HandleFunc("/hls/segment", s.handleHLSSegment)
	mux.HandleFunc("/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/auth/video", s.handleAuthVideo)
	mux.HandleFunc("/retry/429", s.handleRetry429)

	s.HTTPServer = httptest.NewServer(mux)
	return s
}

// Close 关闭测试服务器
func (s *Server) Close() {
	if s.HTTPServer != nil {
		s.HTTPServer.Close()
	}
}

// URL 返回基础 URL
func (s *Server) URL() string {
	return s.HTTPServer.URL
}

func (s *Server) handleMP4Range(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("ETag", "\"test-etag-media-001\"")

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(s.DataSize, 10))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(s.DataBytes)
		return
	}

	// 解析 Range: bytes=start-end
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	rangeVal := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.Split(rangeVal, "-")
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start >= s.DataSize {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", s.DataSize))
		http.Error(w, "Range unsatisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	end := s.DataSize - 1
	if len(parts) > 1 && parts[1] != "" {
		parsedEnd, err := strconv.ParseInt(parts[1], 10, 64)
		if err == nil && parsedEnd < s.DataSize {
			end = parsedEnd
		}
	}

	contentLength := end - start + 1
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, s.DataSize))
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.WriteHeader(http.StatusPartialContent)

	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(s.DataBytes[start : end+1])
}

func (s *Server) handleMP4NoRange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", strconv.FormatInt(s.DataSize, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.DataBytes)
}

func (s *Server) handleHLSMaster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=1280000,RESOLUTION=1280x720
index.m3u8
`
	_, _ = io.WriteString(w, content)
}

func (s *Server) handleHLSIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.0,
segment?seq=0
#EXTINF:2.0,
segment?seq=1
#EXT-X-ENDLIST
`
	_, _ = io.WriteString(w, content)
}

func (s *Server) handleHLSSegment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "video/MP2T")
	seq := r.URL.Query().Get("seq")
	// 每个分片生成 64KB 的确定性数据
	segData := bytes.Repeat([]byte("MPEGTS_SEGMENT_"+seq+"_"), 4096)
	w.Header().Set("Content-Length", strconv.Itoa(len(segData)))
	_, _ = w.Write(segData)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	// 设置测试 Cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "secret_token_12345",
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Now().Add(24 * time.Hour),
	})
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "Logged In")
}

func (s *Server) handleAuthVideo(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("auth_token")
	if err != nil || cookie.Value != "secret_token_12345" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	s.handleMP4Range(w, r)
}

func (s *Server) handleRetry429(w http.ResponseWriter, r *http.Request) {
	count := atomic.AddInt32(&s.retry429Count, 1)
	// 前 2 次请求返回 429 Too Many Requests，第 3 次开始正常返回
	if count <= 2 {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}
	s.handleMP4Range(w, r)
}
