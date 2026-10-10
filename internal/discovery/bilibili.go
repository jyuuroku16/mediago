package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"mediago/internal/model"
)

var (
	bvPattern = regexp.MustCompile(`(?i)(BV[a-zA-Z0-9]{10})`)
)

// BilibiliViewResponse B 站 web-interface/view API 响应
type BilibiliViewResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		BVID  string `json:"bvid"`
		Title string `json:"title"`
		Pic   string `json:"pic"`
		Pages []struct {
			CID      int64  `json:"cid"`
			Page     int    `json:"page"`
			Part     string `json:"part"`
			Duration int64  `json:"duration"`
		} `json:"pages"`
	} `json:"data"`
}

// BilibiliPagelistResponse B 站 pagelist API 响应
type BilibiliPagelistResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []struct {
		CID      int64  `json:"cid"`
		Page     int    `json:"page"`
		Part     string `json:"part"`
		Duration int64  `json:"duration"`
	} `json:"data"`
}

// ExtractBVID 从 URL 中稳定提取 BVID (例如 BV1xA411b7ri)
func ExtractBVID(rawURL string) string {
	matches := bvPattern.FindStringSubmatch(rawURL)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// ExtractPageNumber 从 URL 中提取当前分 P 序号 (默认为 1)
func ExtractPageNumber(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 1
	}
	pStr := u.Query().Get("p")
	if pStr == "" {
		return 1
	}
	p, err := strconv.Atoi(pStr)
	if err != nil || p <= 0 {
		return 1
	}
	return p
}

// FetchBilibiliPages 获取 B 站视频的全部分 P 列表与基本元数据
func FetchBilibiliPages(ctx context.Context, bvid string) ([]model.Episode, string, string, error) {
	if bvid == "" {
		return nil, "", "", fmt.Errorf("空的 BVID")
	}

	client := &http.Client{
		Timeout: 6 * time.Second,
	}

	// 1. 优先请求详细 view API
	viewURL := fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?bvid=%s", bvid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, viewURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://www.bilibili.com")

	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		var viewResp BilibiliViewResponse
		if err := json.Unmarshal(body, &viewResp); err == nil && viewResp.Code == 0 && len(viewResp.Data.Pages) > 0 {
			episodes := make([]model.Episode, len(viewResp.Data.Pages))
			for i, p := range viewResp.Data.Pages {
				title := p.Part
				if title == "" {
					title = fmt.Sprintf("第 %d 集", p.Page)
				}
				episodes[i] = model.Episode{
					Index:    p.Page,
					CID:      p.CID,
					Title:    title,
					Duration: p.Duration,
					URL:      fmt.Sprintf("https://www.bilibili.com/video/%s?p=%d", bvid, p.Page),
				}
			}
			return episodes, viewResp.Data.Title, viewResp.Data.Pic, nil
		}
	}

	// 2. 兜底请求轻量 pagelist API
	plistURL := fmt.Sprintf("https://api.bilibili.com/x/player/pagelist?bvid=%s", bvid)
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, plistURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	req2.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req2.Header.Set("Referer", "https://www.bilibili.com")

	resp2, err := client.Do(req2)
	if err != nil {
		return nil, "", "", fmt.Errorf("请求 pagelist 失败: %w", err)
	}
	defer resp2.Body.Close()

	var plistResp BilibiliPagelistResponse
	if err := json.NewDecoder(resp2.Body).Decode(&plistResp); err != nil {
		return nil, "", "", fmt.Errorf("解析 pagelist 响应失败: %w", err)
	}

	if plistResp.Code != 0 || len(plistResp.Data) == 0 {
		return nil, "", "", fmt.Errorf("未获取到分 P 列表 (code=%d)", plistResp.Code)
	}

	episodes := make([]model.Episode, len(plistResp.Data))
	for i, p := range plistResp.Data {
		title := p.Part
		if title == "" {
			title = fmt.Sprintf("第 %d 集", p.Page)
		}
		episodes[i] = model.Episode{
			Index:    p.Page,
			CID:      p.CID,
			Title:    title,
			Duration: p.Duration,
			URL:      fmt.Sprintf("https://www.bilibili.com/video/%s?p=%d", bvid, p.Page),
		}
	}

	return episodes, "", "", nil
}
