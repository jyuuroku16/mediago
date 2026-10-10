package browser

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"mediago/internal/discovery"
	"mediago/internal/model"
)

// SniffCallback 发现媒体时的回调函数
type SniffCallback func(candidate *model.Candidate)

// Manager 管理独立 WKWebView 浏览器窗口
type Manager struct {
	window       *mygo.Window
	onMediaFound SniffCallback
	mu           sync.Mutex
	isSniffing   bool
}

// NewManager 初始化浏览器管理器
func NewManager(cb SniffCallback) *Manager {
	return &Manager{
		onMediaFound: cb,
	}
}

// Open 打开或聚焦专用浏览器窗口
func (m *Manager) Open(initialURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if initialURL == "" {
		initialURL = "https://www.bilibili.com"
	}

	if m.window != nil {
		m.window.Show()
		m.window.Focus()
		if p := m.window.Page(); p != nil {
			p.LoadURL(initialURL)
		}
		return
	}

	win := mygo.NewWindow(mygo.WindowOptions{
		Title:     "MediaGo 专用浏览器 (登录态与媒体嗅探)",
		URL:       initialURL,
		Width:     1180,
		Height:    760,
		MinWidth:  800,
		MinHeight: 500,
		StateKey:  "mediago_browser_window",
		Page: mygo.PageOptions{
			DevTools:  mygo.DevToolsAuto,
			UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		},
	})

	m.window = win

	win.OnClosed(func() {
		m.mu.Lock()
		m.window = nil
		m.mu.Unlock()
	})

	page := win.Page()
	if page == nil {
		return
	}

	// 监听页面导航与加载完成，触发媒体探测嗅探
	page.OnDidNavigate(func(u string) {
		go m.sniffPageMedia(page)
	})

	page.OnDidFinishLoad(func() {
		go m.sniffPageMedia(page)
	})

	page.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		win.SetTitle("MediaGo 浏览器 - " + e.Title)
		go m.sniffPageMedia(page)
	})
}

// Navigate 导航到指定 URL
func (m *Manager) Navigate(targetURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.window != nil {
		if p := m.window.Page(); p != nil {
			p.LoadURL(targetURL)
		}
	}
}

// GetCurrentURL 获取当前页面 URL
func (m *Manager) GetCurrentURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.window != nil {
		if p := m.window.Page(); p != nil {
			return p.URL()
		}
	}
	return ""
}

type sniffResult struct {
	Title string   `json:"title"`
	URL   string   `json:"url"`
	Media []string `json:"media"`
}

// sniffPageMedia 探测当前页面的视频与媒体元素
func (m *Manager) sniffPageMedia(page *mygo.Page) {
	// 等待 1 秒待动态播放器或异步脚本初始化完成
	time.Sleep(1 * time.Second)

	jsScript := `(() => {
		const urls = new Set();
		// 1. 查找 video 标签与 source
		document.querySelectorAll('video').forEach(v => {
			if (v.src && !v.src.startsWith('blob:')) urls.add(v.src);
			v.querySelectorAll('source').forEach(s => {
				if (s.src && !s.src.startsWith('blob:')) urls.add(s.src);
			});
		});
		// 2. 查找 PerformanceResourceTiming 中的媒体
		if (window.performance && window.performance.getEntriesByType) {
			const resources = window.performance.getEntriesByType('resource');
			for (const r of resources) {
				const u = r.name;
				if (u.includes('.m3u8') || u.includes('.mp4')) {
					urls.add(u);
				}
			}
		}
		return JSON.stringify({
			title: document.title || '',
			url: location.href || '',
			media: Array.from(urls)
		});
	})()`

	resVal, err := page.Eval(jsScript)
	if err != nil {
		return
	}

	var jsonStr string
	if s, ok := resVal.(string); ok {
		jsonStr = s
	} else {
		jsonBytes, _ := json.Marshal(resVal)
		jsonStr = string(jsonBytes)
	}

	var res sniffResult
	if err := json.Unmarshal([]byte(jsonStr), &res); err != nil {
		return
	}

	// 如果检测到是热门视频网站播放页面，自动推送页面候选供主应用深度解析
	isKnownVideoPage := strings.Contains(res.URL, "bilibili.com/video/") ||
		strings.Contains(res.URL, "youtube.com/watch") ||
		strings.Contains(res.URL, "douyin.com/video") ||
		strings.Contains(res.URL, "kuaishou.com/short-video")

	if isKnownVideoPage && res.Title != "" && !strings.HasPrefix(res.Title, "哔哩哔哩 (゜-゜)つロ 干杯~") {
		cand := &model.Candidate{
			ID:        fmt.Sprintf("page-%d", time.Now().UnixNano()),
			SourceURL: res.URL,
			Title:     res.Title,
			Author:    "页面视频发现",
			CreatedAt: time.Now(),
		}
		if m.onMediaFound != nil {
			log.Printf("[Sniffer] 自动识别视频播放页: %s -> %s\n", res.Title, res.URL)
			m.onMediaFound(cand)
		}
	}

	if len(res.Media) == 0 {
		return
	}

	for _, mediaURL := range res.Media {
		mediaURL = strings.TrimSpace(mediaURL)
		if mediaURL == "" {
			continue
		}

		isDirect, ext := discovery.IsDirectMediaURL(mediaURL)
		if isDirect {
			proto := "http"
			if ext == "m3u8" {
				proto = "m3u8"
			}
			cand := &model.Candidate{
				ID:        fmt.Sprintf("cand-%d", time.Now().UnixNano()),
				SourceURL: res.URL,
				Title:     res.Title,
				Author:    "网页嗅探",
				Formats: []model.MediaFormat{
					{
						FormatID:   "sniffed",
						Resolution: "Sniffed",
						Ext:        ext,
						URL:        mediaURL,
						Protocol:   proto,
						Note:       "浏览器嗅探直链",
					},
				},
				CreatedAt: time.Now(),
			}

			if m.onMediaFound != nil {
				log.Printf("[Sniffer] 成功嗅探到媒体: %s -> %s\n", res.Title, mediaURL)
				m.onMediaFound(cand)
			}
		}
	}
}
