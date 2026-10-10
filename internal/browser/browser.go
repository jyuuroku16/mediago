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

	// 监听页面导航与加载完成，触发媒体探测嗅探与 Chrome 顶栏注入
	page.OnDidNavigate(func(u string) {
		go m.injectChromeTopBar(page)
		go m.sniffPageMedia(page)
	})

	page.OnDidFinishLoad(func() {
		go m.injectChromeTopBar(page)
		go m.sniffPageMedia(page)
	})

	page.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		win.SetTitle("MediaGo 浏览器 - " + e.Title)
		go m.injectChromeTopBar(page)
		go m.sniffPageMedia(page)
	})
}

// injectChromeTopBar 注入类似 Chrome 现代顶栏与 Omnibox 地址输入栏
func (m *Manager) injectChromeTopBar(page *mygo.Page) {
	script := `(() => {
		try {
			let bar = document.getElementById('mediago-omnibox-bar');
			if (!bar) {
				bar = document.createElement('div');
				bar.id = 'mediago-omnibox-bar';
				bar.style.cssText = 'position:fixed!important;top:0!important;left:0!important;right:0!important;height:38px!important;background:rgba(28,28,30,0.96)!important;backdrop-filter:blur(24px)!important;-webkit-backdrop-filter:blur(24px)!important;border-bottom:1px solid rgba(255,255,255,0.12)!important;z-index:2147483647!important;display:flex!important;align-items:center!important;padding:0 10px!important;gap:8px!important;font-family:-apple-system,BlinkMacSystemFont,sans-serif!important;box-shadow:0 2px 12px rgba(0,0,0,0.3)!important;color:#fff!important;user-select:none!important;box-sizing:border-box!important;';
				bar.innerHTML = '<button id="mg-nav-back" title="后退" style="background:transparent;border:none;color:#aaa;font-size:16px;cursor:pointer;width:26px;height:26px;border-radius:6px;display:flex;align-items:center;justify-content:center;">‹</button>' +
					'<button id="mg-nav-forward" title="前进" style="background:transparent;border:none;color:#aaa;font-size:16px;cursor:pointer;width:26px;height:26px;border-radius:6px;display:flex;align-items:center;justify-content:center;">›</button>' +
					'<button id="mg-nav-reload" title="刷新" style="background:transparent;border:none;color:#aaa;font-size:13px;cursor:pointer;width:26px;height:26px;border-radius:6px;display:flex;align-items:center;justify-content:center;">↻</button>' +
					'<div style="flex:1;position:relative;display:flex;align-items:center;height:26px;background:rgba(255,255,255,0.08);border:1px solid rgba(255,255,255,0.14);border-radius:6px;padding:0 8px;gap:6px;">' +
						'<span style="font-size:11px;opacity:0.6;">🔒</span>' +
						'<input id="mg-nav-input" type="text" spellcheck="false" autocomplete="off" style="flex:1;background:transparent;border:none;color:#fff;font-size:12px;outline:none;font-family:inherit;width:100%;" placeholder="输入网址或回车前往..." />' +
					'</div>' +
					'<div id="mg-sniff-badge" style="display:flex;align-items:center;gap:4px;padding:2px 8px;background:rgba(52,199,89,0.15);border:1px solid rgba(52,199,89,0.3);border-radius:6px;font-size:11px;color:#34c759;font-weight:500;">' +
						'<span style="width:6px;height:6px;border-radius:50%;background:#34c759;display:inline-block;"></span>' +
						'<span id="mg-sniff-text">嗅探就绪</span>' +
					'</div>';
				document.documentElement.appendChild(bar);

				if (!document.getElementById('mediago-style-override')) {
					const st = document.createElement('style');
					st.id = 'mediago-style-override';
					st.innerHTML = 'html, body { margin-top: 38px !important; }';
					document.head.appendChild(st);
				}

				const backBtn = bar.querySelector('#mg-nav-back');
				const forwardBtn = bar.querySelector('#mg-nav-forward');
				const reloadBtn = bar.querySelector('#mg-nav-reload');
				const input = bar.querySelector('#mg-nav-input');

				[backBtn, forwardBtn, reloadBtn].forEach(b => {
					b.onmouseenter = () => { b.style.background = 'rgba(255,255,255,0.12)'; b.style.color = '#fff'; };
					b.onmouseleave = () => { b.style.background = 'transparent'; b.style.color = '#aaa'; };
				});

				backBtn.onclick = () => window.history.back();
				forwardBtn.onclick = () => window.history.forward();
				reloadBtn.onclick = () => window.location.reload();

				input.onkeydown = (e) => {
					if (e.key === 'Enter') {
						let val = input.value.trim();
						if (!val) return;
						if (!val.includes('.') && !val.startsWith('http')) {
							window.location.href = 'https://search.bilibili.com/all?keyword=' + encodeURIComponent(val);
						} else {
							if (!val.startsWith('http://') && !val.startsWith('https://')) {
								val = 'https://' + val;
							}
							window.location.href = val;
						}
					}
				};
			}

			const input = document.getElementById('mg-nav-input');
			if (input && document.activeElement !== input) {
				input.value = window.location.href;
			}
		} catch (e) {}
	})()`
	_, _ = page.Eval(script)
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
