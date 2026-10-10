package discovery

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NormalizeURL 清理常见追踪参数，规范化 URL
func NormalizeURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return rawURL
	}

	q := u.Query()
	trackingParams := []string{
		"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
		"spm", "spm_id_from", "from_source", "share_source", "fbclid", "gclid",
	}

	for _, p := range trackingParams {
		q.Del(p)
	}

	u.RawQuery = q.Encode()
	return u.String()
}

// ResolveRedirects 针对短链接进行重定向解析，返回最终目标 URL
func ResolveRedirects(ctx context.Context, rawURL string) string {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return rawURL
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko)")

	resp, err := client.Do(req)
	if err != nil {
		return rawURL
	}
	defer resp.Body.Close()

	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return rawURL
}

// IsDirectMediaURL 检查是否为已知常见直链视频或媒体流
func IsDirectMediaURL(rawURL string) (bool, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, ""
	}

	lowerPath := strings.ToLower(u.Path)
	if strings.HasSuffix(lowerPath, ".mp4") {
		return true, "mp4"
	}
	if strings.HasSuffix(lowerPath, ".m3u8") {
		return true, "m3u8"
	}
	if strings.HasSuffix(lowerPath, ".webm") {
		return true, "webm"
	}
	if strings.HasSuffix(lowerPath, ".mkv") {
		return true, "mkv"
	}

	return false, ""
}
