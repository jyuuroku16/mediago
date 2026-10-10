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

// TabInfo 标签页状态
type TabInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Active     bool   `json:"active"`
	MediaCount int    `json:"mediaCount"`
}

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

// Open 打开或聚焦专用浏览器窗口 (Gorex 风格无标题栏、毛玻璃底衬、多标签页)
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

	// 参考 gorex: TitleBarHiddenInset 无原生标题栏, TitleBarHeight: 44, VibrancySidebar 模糊底衬
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:         "MediaGo 专用浏览器",
		URL:           initialURL,
		Width:         1180,
		Height:        760,
		MinWidth:      840,
		MinHeight:     520,
		TitleBarStyle: mygo.TitleBarHiddenInset,
		TitleBarHeight: 44,
		Vibrancy:      mygo.VibrancySidebar,
		TrafficLightPosition: &mygo.Point{X: 16, Y: 14},
		StateKey:      "mediago_browser_window",
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

	// 监听页面导航与加载完成，注入 Gorex 风格顶栏与触发独立媒体嗅探
	page.OnDidNavigate(func(u string) {
		go m.injectGorexTopBar(page)
		go m.sniffPageMedia(page)
	})

	page.OnDidFinishLoad(func() {
		go m.injectGorexTopBar(page)
		go m.sniffPageMedia(page)
	})

	page.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		win.SetTitle("MediaGo 浏览器 - " + e.Title)
		go m.injectGorexTopBar(page)
		go m.sniffPageMedia(page)
	})
}

// injectGorexTopBar 注入 Gorex 风格顶栏: 交通灯避让、输入栏聚焦让出40%、渐进模糊遮罩、多标签栏+、圆角容器主体
func (m *Manager) injectGorexTopBar(page *mygo.Page) {
	script := `(() => {
		try {
			if (window !== window.top) return; // 仅在顶层窗口运行

			// 1. 确保多标签页数据初始化与持久化
			const TABS_KEY = 'mediago_browser_tabs_v2';
			let tabs = [];
			try {
				tabs = JSON.parse(localStorage.getItem(TABS_KEY) || '[]');
			} catch (e) { tabs = []; }

			const currentHref = window.location.href;
			const currentTitle = document.title || '哔哩哔哩 (゜-゜)つロ 干杯~';

			let currentTab = tabs.find(t => t.active);
			if (!currentTab) {
				if (tabs.length === 0) {
					currentTab = { id: 'tab-' + Date.now(), title: currentTitle, url: currentHref, active: true, mediaCount: 0 };
					tabs = [currentTab];
				} else {
					currentTab = tabs[0];
					currentTab.active = true;
				}
			}
			// 更新当前活跃标签信息
			currentTab.url = currentHref;
			if (currentTitle && !currentTitle.startsWith('http')) {
				currentTab.title = currentTitle;
			}
			localStorage.setItem(TABS_KEY, JSON.stringify(tabs));

			// 2. 注入全局样式 (Gorex 风格圆角主体、渐进模糊、聚焦展开40%、毛玻璃顶栏)
			let styleEl = document.getElementById('mediago-gorex-styles');
			if (!styleEl) {
				styleEl = document.createElement('style');
				styleEl.id = 'mediago-gorex-styles';
				styleEl.innerHTML = ` + "`" + `
					/* 页面主体包裹为 Gorex 风格悬浮圆角模糊容器 */
					html {
						background: transparent !important;
						height: 100% !important;
						overflow: hidden !important;
					}
					body {
						margin: 44px 8px 8px 8px !important;
						height: calc(100% - 52px) !important;
						box-sizing: border-box !important;
						border-radius: 12px !important;
						border: 1px solid rgba(255, 255, 255, 0.08) !important;
						box-shadow: 0 4px 24px rgba(0, 0, 0, 0.28) !important;
						overflow: auto !important;
						position: relative !important;
					}

					/* 顶栏: 类似 gorex titleBar (高度 44px, 交通灯避让 78px, 磨砂背景) */
					#mg-chrome-topbar {
						position: fixed !important;
						top: 0 !important;
						left: 0 !important;
						right: 0 !important;
						height: 44px !important;
						padding: 0 12px 0 78px !important;
						display: flex !important;
						align-items: center !important;
						gap: 10px !important;
						z-index: 2147483647 !important;
						background: rgba(28, 28, 32, 0.85) !important;
						backdrop-filter: blur(28px) saturate(190%) !important;
						-webkit-backdrop-filter: blur(28px) saturate(190%) !important;
						border-bottom: 0.5px solid rgba(255, 255, 255, 0.08) !important;
						user-select: none !important;
						-webkit-app-region: drag !important;
						box-sizing: border-box !important;
						font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Helvetica Neue", sans-serif !important;
					}

					/* 顶栏内控件不触发窗口拖拽 */
					#mg-chrome-topbar button,
					#mg-chrome-topbar input,
					#mg-chrome-topbar .mg-interactive {
						-webkit-app-region: no-drag !important;
					}

					/* 导航控制区 (后退/前进/刷新) */
					.mg-nav-actions {
						display: flex;
						align-items: center;
						gap: 4px;
						flex-shrink: 0;
					}
					.mg-icon-btn {
						width: 26px;
						height: 26px;
						border-radius: 7px;
						border: none;
						background: transparent;
						color: rgba(255, 255, 255, 0.65);
						display: flex;
						align-items: center;
						justify-content: center;
						cursor: pointer;
						transition: all 0.15s ease;
						font-size: 13px;
					}
					.mg-icon-btn:hover {
						background: rgba(255, 255, 255, 0.12);
						color: #ffffff;
					}

					/* 输入栏容器: 默认紧凑 (220px), 聚焦时让出 40% 标签栏宽度并平滑展开 */
					.mg-omnibox-wrap {
						width: 220px;
						height: 28px;
						background: rgba(255, 255, 255, 0.07);
						border: 1px solid rgba(255, 255, 255, 0.12);
						border-radius: 14px;
						display: flex;
						align-items: center;
						padding: 0 10px;
						gap: 6px;
						flex-shrink: 0;
						transition: width 0.28s cubic-bezier(0.16, 1, 0.3, 1), background 0.2s ease, border-color 0.2s ease;
						position: relative;
						overflow: hidden;
					}
					.mg-omnibox-wrap:focus-within {
						width: min(560px, calc(220px + 36vw)) !important;
						background: rgba(255, 255, 255, 0.12) !important;
						border-color: rgba(255, 255, 255, 0.3) !important;
						box-shadow: 0 0 0 3px rgba(255, 255, 255, 0.06) !important;
					}

					/* 输入框文字渐进模糊遮罩: 两侧渐变淡出，无生硬截断 */
					.mg-omnibox-input {
						flex: 1;
						background: transparent;
						border: none;
						outline: none;
						color: #ffffff;
						font-size: 12px;
						font-family: inherit;
						width: 100%;
						-webkit-mask-image: linear-gradient(to right, transparent 0px, black 14px, black calc(100% - 14px), transparent 100%);
						mask-image: linear-gradient(to right, transparent 0px, black 14px, black calc(100% - 14px), transparent 100%);
					}

					/* Gorex 风格多标签栏轨道 (Tab Track) */
					.mg-tab-strip {
						flex: 1;
						display: flex;
						align-items: center;
						gap: 4px;
						height: 32px;
						padding: 2px 4px;
						border-radius: 16px;
						background: rgba(255, 255, 255, 0.04);
						border: 0.5px solid rgba(255, 255, 255, 0.06);
						overflow-x: auto;
						overflow-y: hidden;
						min-width: 0;
					}
					.mg-tab-strip::-webkit-scrollbar {
						display: none;
					}

					/* 单个标签页胶囊 (Tab Item) */
					.mg-tab-item {
						height: 26px;
						min-width: 110px;
						max-width: 190px;
						padding: 0 8px 0 10px;
						border-radius: 13px;
						display: flex;
						align-items: center;
						gap: 6px;
						font-size: 12px;
						color: rgba(255, 255, 255, 0.65);
						cursor: pointer;
						transition: all 0.18s ease;
						flex-shrink: 1;
						position: relative;
						border: 0.5px solid transparent;
					}
					.mg-tab-item:hover {
						background: rgba(255, 255, 255, 0.08);
						color: rgba(255, 255, 255, 0.9);
					}
					.mg-tab-item.active {
						background: rgba(255, 255, 255, 0.16) !important;
						color: #ffffff !important;
						border-color: rgba(255, 255, 255, 0.22) !important;
						box-shadow: 0 1px 4px rgba(0, 0, 0, 0.25) !important;
						font-weight: 500;
					}

					/* 标签标题超出渐变淡出 */
					.mg-tab-title {
						flex: 1;
						white-space: nowrap;
						overflow: hidden;
						-webkit-mask-image: linear-gradient(to right, black calc(100% - 16px), transparent 100%);
						mask-image: linear-gradient(to right, black calc(100% - 16px), transparent 100%);
					}

					/* 标签关闭按钮 */
					.mg-tab-close {
						width: 16px;
						height: 16px;
						border-radius: 8px;
						display: flex;
						align-items: center;
						justify-content: center;
						font-size: 11px;
						color: rgba(255, 255, 255, 0.5);
						opacity: 0;
						transition: opacity 0.15s, background 0.15s;
					}
					.mg-tab-item:hover .mg-tab-close,
					.mg-tab-item.active .mg-tab-close {
						opacity: 1;
					}
					.mg-tab-close:hover {
						background: rgba(255, 255, 255, 0.2);
						color: #ffffff;
					}

					/* 嗅探绿点 (呼吸灯脉冲效果) */
					.mg-pulse-dot {
						width: 6.5px;
						height: 6.5px;
						border-radius: 50%;
						background: #34c759;
						box-shadow: 0 0 6px rgba(52, 199, 89, 0.8);
						flex-shrink: 0;
						animation: mg-pulse-anim 1.8s infinite ease-in-out;
					}
					@keyframes mg-pulse-anim {
						0% { transform: scale(0.9); opacity: 0.7; }
						50% { transform: scale(1.25); opacity: 1; box-shadow: 0 0 9px rgba(52, 199, 89, 1); }
						100% { transform: scale(0.9); opacity: 0.7; }
					}

					/* 新建标签按钮 (+) */
					.mg-new-tab-btn {
						width: 28px;
						height: 28px;
						border-radius: 8px;
						border: none;
						background: transparent;
						color: rgba(255, 255, 255, 0.7);
						display: flex;
						align-items: center;
						justify-content: center;
						cursor: pointer;
						font-size: 16px;
						font-weight: 300;
						transition: all 0.15s ease;
						flex-shrink: 0;
					}
					.mg-new-tab-btn:hover {
						background: rgba(255, 255, 255, 0.12);
						color: #ffffff;
					}
				` + "`" + `;
				document.documentElement.appendChild(styleEl);
			}

			// 3. 构建/更新顶栏 DOM
			let bar = document.getElementById('mg-chrome-topbar');
			if (!bar) {
				bar = document.createElement('div');
				bar.id = 'mg-chrome-topbar';
				document.documentElement.appendChild(bar);
			}

			// 组装顶栏 HTML
			let tabsHTML = tabs.map((t, idx) => {
				const isActive = t.active;
				const hasMedia = (t.mediaCount || 0) > 0;
				return ` + "`" + `
					<div class="mg-tab-item mg-interactive ${isActive ? 'active' : ''}" data-tab-id="${t.id}" title="${t.title}">
						${hasMedia ? '<div class="mg-pulse-dot" title="已嗅探到 ' + t.mediaCount + ' 个媒体"></div>' : '<span style="font-size:10px;opacity:0.6;">🌐</span>'}
						<span class="mg-tab-title">${t.title || '新标签页'}</span>
						${tabs.length > 1 ? '<span class="mg-tab-close" data-close-id="' + t.id + '">×</span>' : ''}
					</div>
				` + "`" + `;
			}).join('');

			bar.innerHTML = ` + "`" + `
				<div class="mg-nav-actions">
					<button class="mg-icon-btn mg-interactive" id="mg-nav-back" title="后退">‹</button>
					<button class="mg-icon-btn mg-interactive" id="mg-nav-forward" title="前进">›</button>
					<button class="mg-icon-btn mg-interactive" id="mg-nav-reload" title="刷新">↻</button>
				</div>
				<div class="mg-omnibox-wrap mg-interactive">
					<span style="font-size:11px;opacity:0.55;">🔒</span>
					<input class="mg-omnibox-input" id="mg-nav-input" type="text" spellcheck="false" autocomplete="off" value="${currentHref}" placeholder="输入网址或回车搜索..." />
				</div>
				<div class="mg-tab-strip">
					${tabsHTML}
				</div>
				<button class="mg-new-tab-btn mg-interactive" id="mg-new-tab" title="新建标签页 (+)">+</button>
			` + "`" + `;

			// 4. 事件绑定
			const backBtn = bar.querySelector('#mg-nav-back');
			const forwardBtn = bar.querySelector('#mg-nav-forward');
			const reloadBtn = bar.querySelector('#mg-nav-reload');
			const input = bar.querySelector('#mg-nav-input');
			const newTabBtn = bar.querySelector('#mg-new-tab');

			backBtn.onclick = () => window.history.back();
			forwardBtn.onclick = () => window.history.forward();
			reloadBtn.onclick = () => window.location.reload();

			input.onkeydown = (e) => {
				if (e.key === 'Enter') {
					let val = input.value.trim();
					if (!val) return;
					if (!val.includes('.') && !val.startsWith('http')) {
						val = 'https://search.bilibili.com/all?keyword=' + encodeURIComponent(val);
					} else if (!val.startsWith('http://') && !val.startsWith('https://')) {
						val = 'https://' + val;
					}
					window.location.href = val;
				}
			};

			// 新建标签页
			newTabBtn.onclick = () => {
				const newId = 'tab-' + Date.now();
				tabs.forEach(t => t.active = false);
				const newTab = {
					id: newId,
					title: '哔哩哔哩 (゜-゜)つロ 干杯~',
					url: 'https://www.bilibili.com',
					active: true,
					mediaCount: 0
				};
				tabs.push(newTab);
				localStorage.setItem(TABS_KEY, JSON.stringify(tabs));
				window.location.href = newTab.url;
			};

			// 标签点击与关闭
			bar.querySelectorAll('.mg-tab-item').forEach(item => {
				const tabId = item.getAttribute('data-tab-id');
				item.onclick = (e) => {
					if (e.target.classList.contains('mg-tab-close')) {
						e.stopPropagation();
						const closeId = e.target.getAttribute('data-close-id');
						const closeIdx = tabs.findIndex(t => t.id === closeId);
						if (closeIdx >= 0) {
							const wasActive = tabs[closeIdx].active;
							tabs.splice(closeIdx, 1);
							if (wasActive && tabs.length > 0) {
								const nextTab = tabs[Math.max(0, closeIdx - 1)];
								nextTab.active = true;
								localStorage.setItem(TABS_KEY, JSON.stringify(tabs));
								window.location.href = nextTab.url;
								return;
							}
							localStorage.setItem(TABS_KEY, JSON.stringify(tabs));
							item.remove();
						}
						return;
					}

					// 切换标签
					if (item.classList.contains('active')) return;
					tabs.forEach(t => t.active = (t.id === tabId));
					localStorage.setItem(TABS_KEY, JSON.stringify(tabs));
					const target = tabs.find(t => t.id === tabId);
					if (target && target.url) {
						window.location.href = target.url;
					}
				};
			});

		} catch (e) {
			console.error('injectGorexTopBar error:', e);
		}
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

	mediaFoundCount := len(res.Media)

	// 如果检测到是热门视频网站播放页面，自动推送页面候选供主应用深度解析
	isKnownVideoPage := strings.Contains(res.URL, "bilibili.com/video/") ||
		strings.Contains(res.URL, "youtube.com/watch") ||
		strings.Contains(res.URL, "douyin.com/video") ||
		strings.Contains(res.URL, "kuaishou.com/short-video")

	if isKnownVideoPage && res.Title != "" && !strings.HasPrefix(res.Title, "哔哩哔哩 (゜-゜)つロ 干杯~") {
		mediaFoundCount++
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

	// 更新标签栏上的嗅探状态徽标与绿灯
	if mediaFoundCount > 0 {
		updateBadgeScript := fmt.Sprintf(`(() => {
			try {
				const TABS_KEY = 'mediago_browser_tabs_v2';
				let tabs = JSON.parse(localStorage.getItem(TABS_KEY) || '[]');
				const cur = tabs.find(t => t.active);
				if (cur) {
					cur.mediaCount = %d;
					localStorage.setItem(TABS_KEY, JSON.stringify(tabs));
					const dot = document.querySelector('.mg-tab-item.active .mg-pulse-dot');
					if (!dot) {
						const activeItem = document.querySelector('.mg-tab-item.active');
						if (activeItem) {
							const span = activeItem.querySelector('span');
							if (span) {
								const newDot = document.createElement('div');
								newDot.className = 'mg-pulse-dot';
								newDot.title = '已嗅探到 %d 个媒体';
								activeItem.insertBefore(newDot, span);
								span.remove();
							}
						}
					}
				}
			} catch (e) {}
		})()`, mediaFoundCount, mediaFoundCount)
		_, _ = page.Eval(updateBadgeScript)
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
