package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/google/uuid"
	"mediago/internal/discovery"
	"mediago/internal/model"
)

// TaskSubmitCallback 浏览器确认下载时提交任务给调度器的回调
type TaskSubmitCallback func(task *model.Task)

// SniffCallback 兼容旧回调
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
	onSubmitTask TaskSubmitCallback
	onMediaFound SniffCallback
	extractor    *discovery.YtDlpExtractor
	downloadDir  string
	lastCand     *model.Candidate
	mu           sync.Mutex
	isSniffing   bool
}

// NewManager 初始化浏览器管理器
func NewManager(onSubmit TaskSubmitCallback, downloadDir ...string) *Manager {
	dir := ""
	if len(downloadDir) > 0 {
		dir = downloadDir[0]
	}
	return &Manager{
		onSubmitTask: onSubmit,
		extractor:    discovery.NewYtDlpExtractor(),
		downloadDir:  dir,
	}
}

// SetDownloadDir 设置下载保存目录
func (m *Manager) SetDownloadDir(d string) {
	m.mu.Lock()
	m.downloadDir = d
	m.mu.Unlock()
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
		TitleBarStyle:        mygo.TitleBarHiddenInset,
		TitleBarHeight:       44,
		Vibrancy:             mygo.VibrancySidebar,
		BackgroundColor:      "#1c1c20",
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
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if strings.HasPrefix(e.URL, "mediago-sniff://extract") {
			e.PreventDefault()
			raw := strings.TrimPrefix(e.URL, "mediago-sniff://extract?")
			q, _ := url.ParseQuery(raw)
			decURL := q.Get("url")
			if decURL == "" {
				return
			}

			// 在后台异步深度探测，主窗口完全不弹窗
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()

				cand, err := m.extractor.Extract(ctx, decURL)
				if err != nil || cand == nil {
					errStr := "页面视频深度解析受限"
					if err != nil {
						errStr = err.Error()
					}
					if len([]rune(errStr)) > 35 {
						errStr = string([]rune(errStr)[:32]) + "..."
					}
					escapedErr := strings.ReplaceAll(errStr, `"`, `\"`)
					escapedErr = strings.ReplaceAll(escapedErr, "\n", " ")
					script := fmt.Sprintf(`if (window.__mg_render_extract_error) window.__mg_render_extract_error("%s");`, escapedErr)
					_, _ = page.Eval(script)
					return
				}

				m.mu.Lock()
				m.lastCand = cand
				m.mu.Unlock()

				type fmtItem struct {
					ID  string `json:"id"`
					Res string `json:"res"`
					Ext string `json:"ext"`
				}
				var fmts []fmtItem
				for _, f := range cand.Formats {
					fmts = append(fmts, fmtItem{
						ID:  f.FormatID,
						Res: f.Resolution,
						Ext: strings.ToUpper(f.Ext),
					})
				}
				fb, _ := json.Marshal(fmts)
				escapedTitle := strings.ReplaceAll(cand.Title, `"`, `\"`)
				escapedTitle = strings.ReplaceAll(escapedTitle, "\n", " ")

				script := fmt.Sprintf(`if (window.__mg_render_parsed_ui) window.__mg_render_parsed_ui("%s", %s, %d);`,
					escapedTitle, string(fb), len(cand.Episodes))
				_, _ = page.Eval(script)
			}()
			return
		}

		if strings.HasPrefix(e.URL, "mediago-sniff://confirm-download") {
			e.PreventDefault()
			raw := strings.TrimPrefix(e.URL, "mediago-sniff://confirm-download?")
			q, _ := url.ParseQuery(raw)
			fmtID := q.Get("fmtId")

			m.mu.Lock()
			cand := m.lastCand
			outDir := m.downloadDir
			m.mu.Unlock()

			if cand != nil && m.onSubmitTask != nil {
				var selectedFmt *model.MediaFormat
				for _, f := range cand.Formats {
					if f.FormatID == fmtID {
						selectedFmt = &f
						break
					}
				}
				if selectedFmt == nil && len(cand.Formats) > 0 {
					selectedFmt = &cand.Formats[0]
				}

				dlURL := cand.SourceURL
				engineType := model.EngineExtractor
				ext := "mp4"
				var totalBytes int64
				if selectedFmt != nil {
					if selectedFmt.URL != "" {
						dlURL = selectedFmt.URL
					}
					if selectedFmt.Protocol == "m3u8" {
						engineType = model.EngineHLS
					}
					if selectedFmt.Ext != "" {
						ext = selectedFmt.Ext
					}
					totalBytes = selectedFmt.Filesize
				}

				cleanTitle := strings.TrimSpace(cand.Title)
				if cleanTitle == "" {
					cleanTitle = "视频资源"
				}
				fn := fmt.Sprintf("%s.%s", cleanTitle, ext)

				task := &model.Task{
					ID:             uuid.New().String(),
					Title:          cleanTitle,
					SourceURL:      cand.SourceURL,
					DownloadURL:    dlURL,
					Engine:         engineType,
					Status:         model.StatusQueued,
					TotalBytes:     totalBytes,
					OutputDir:      outDir,
					OutputFilename: fn,
					Priority:       1,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
				}
				m.onSubmitTask(task)
			}
			_, _ = page.Eval(`if (window.__mg_render_success) window.__mg_render_success();`)
			return
		}

		if strings.HasPrefix(e.URL, "mediago-sniff://direct-download") {
			e.PreventDefault()
			raw := strings.TrimPrefix(e.URL, "mediago-sniff://direct-download?")
			q, _ := url.ParseQuery(raw)
			directURL := q.Get("directUrl")
			tTitle := q.Get("title")
			if tTitle == "" {
				tTitle = "嗅探直链视频"
			}

			if directURL != "" && m.onSubmitTask != nil {
				m.mu.Lock()
				outDir := m.downloadDir
				m.mu.Unlock()

				proto := "http"
				ext := "mp4"
				if strings.Contains(directURL, ".m3u8") {
					proto = "m3u8"
					ext = "ts"
				}
				engineType := model.EngineDirect
				if proto == "m3u8" {
					engineType = model.EngineHLS
				}

				task := &model.Task{
					ID:             uuid.New().String(),
					Title:          tTitle,
					SourceURL:      directURL,
					DownloadURL:    directURL,
					Engine:         engineType,
					Status:         model.StatusQueued,
					OutputDir:      outDir,
					OutputFilename: fmt.Sprintf("%s.%s", tTitle, ext),
					Priority:       1,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
				}
				m.onSubmitTask(task)
			}
			_, _ = page.Eval(`if (window.__mg_render_success) window.__mg_render_success();`)
			return
		}
	})

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
					/* 页面主体: 移除白色边框与内缩，恢复全屏通透无切边 */
					html {
						height: 100% !important;
					}
					body {
						margin: 0 !important;
						padding-top: 44px !important;
						box-sizing: border-box !important;
						border: none !important;
						border-radius: 0 !important;
						box-shadow: none !important;
					}

					/* 顶栏: 类似 gorex titleBar (高度 44px, 交通灯避让 78px, 均匀毛玻璃磨砂底板) */
					#mg-chrome-topbar {
						position: fixed !important;
						top: 0 !important;
						left: 0 !important;
						right: 0 !important;
						height: 44px !important;
						padding: 0 14px 0 88px !important;
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

					/* 适配系统浅色模式 */
					@media (prefers-color-scheme: light) {
						#mg-chrome-topbar {
							background: rgba(246, 246, 248, 0.88) !important;
							border-bottom: 0.5px solid rgba(0, 0, 0, 0.08) !important;
						}
						.mg-icon-btn, .mg-new-tab-btn {
							color: rgba(0, 0, 0, 0.65) !important;
						}
						.mg-icon-btn:hover, .mg-new-tab-btn:hover {
							background: rgba(0, 0, 0, 0.08) !important;
							color: #000000 !important;
						}
						.mg-omnibox-wrap {
							background: rgba(0, 0, 0, 0.05) !important;
							border-color: rgba(0, 0, 0, 0.1) !important;
						}
						.mg-omnibox-wrap:focus-within {
							background: rgba(0, 0, 0, 0.08) !important;
							border-color: rgba(0, 0, 0, 0.25) !important;
							box-shadow: 0 0 0 3px rgba(0, 0, 0, 0.04) !important;
						}
						.mg-omnibox-input {
							color: #1d1d1f !important;
						}
						.mg-tab-strip {
							background: rgba(0, 0, 0, 0.04) !important;
							border-color: rgba(0, 0, 0, 0.06) !important;
						}
						.mg-tab-item {
							color: rgba(0, 0, 0, 0.65) !important;
						}
						.mg-tab-item:hover {
							background: rgba(0, 0, 0, 0.06) !important;
							color: #000000 !important;
						}
						.mg-tab-item.active {
							background: rgba(0, 0, 0, 0.12) !important;
							color: #000000 !important;
							border-color: rgba(0, 0, 0, 0.15) !important;
						}
					}

					/* 顶栏内控件不触发窗口拖拽 */
					#mg-chrome-topbar button,
					#mg-chrome-topbar input,
					#mg-chrome-topbar .mg-interactive {
						-webkit-app-region: no-drag !important;
					}

					/* 导航控制区 (后退/前进，左侧留出舒适呼吸间距) */
					.mg-nav-actions {
						display: flex;
						align-items: center;
						gap: 4px;
						margin-left: 4px !important;
						flex-shrink: 0;
					}
					.mg-icon-btn {
						width: 28px;
						height: 28px;
						border-radius: 7px;
						border: none;
						background: transparent;
						color: rgba(255, 255, 255, 0.7);
						display: flex;
						align-items: center;
						justify-content: center;
						cursor: pointer;
						transition: all 0.15s ease;
						font-size: 17px;
						font-weight: 500;
					}
					.mg-icon-btn:hover {
						background: rgba(255, 255, 255, 0.12);
						color: #ffffff;
					}

					/* 右上角操作区 (刷新与新建标签) */
					.mg-right-actions {
						display: flex;
						align-items: center;
						gap: 4px;
						flex-shrink: 0;
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

					/* 输入框文字渐进模糊遮罩: 左侧清晰锐利，仅右侧显示不全时渐变淡出 */
					.mg-omnibox-input {
						flex: 1;
						background: transparent;
						border: none;
						outline: none;
						color: #ffffff;
						font-size: 12px;
						font-family: inherit;
						width: 100%;
						-webkit-mask-image: linear-gradient(to right, black calc(100% - 22px), transparent 100%);
						mask-image: linear-gradient(to right, black calc(100% - 22px), transparent 100%);
					}

					/* Gorex 风格多标签栏轨道 (内外圆角同心切合: 16px = 14px + 2px) */
					.mg-tab-strip {
						flex: 1;
						display: flex;
						align-items: center;
						gap: 4px;
						height: 32px;
						padding: 2px !important;
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
						height: 28px;
						min-width: 110px;
						max-width: 190px;
						padding: 0 8px 0 10px;
						border-radius: 14px;
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

					/* 新建标签按钮 (+) 与刷新按钮通用样式 */
					.mg-new-tab-btn, .mg-reload-btn {
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
						transition: all 0.15s ease;
						flex-shrink: 0;
					}
					.mg-new-tab-btn:hover, .mg-reload-btn:hover {
						background: rgba(255, 255, 255, 0.12);
						color: #ffffff;
					}

					/* 浏览器右下角浮动媒体嗅探卡片 (Toast Modal) */
					#mg-sniff-toast-card {
						position: fixed !important;
						bottom: 24px !important;
						right: 24px !important;
						width: 330px !important;
						padding: 14px 16px !important;
						border-radius: 12px !important;
						background: rgba(28, 28, 32, 0.88) !important;
						backdrop-filter: blur(28px) saturate(190%) !important;
						-webkit-backdrop-filter: blur(28px) saturate(190%) !important;
						border: 0.5px solid rgba(255, 255, 255, 0.16) !important;
						box-shadow: 0 10px 32px rgba(0, 0, 0, 0.5) !important;
						z-index: 2147483647 !important;
						box-sizing: border-box !important;
						font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Helvetica Neue", sans-serif !important;
						display: flex !important;
						flex-direction: column !important;
						gap: 10px !important;
						animation: mgToastIn 0.28s cubic-bezier(0.16, 1, 0.3, 1) forwards !important;
						user-select: none !important;
					}
					@keyframes mgToastIn {
						from { opacity: 0; transform: translateY(16px) scale(0.96); }
						to { opacity: 1; transform: translateY(0) scale(1); }
					}
					/* 动态旋转菊花 Loading 动画 */
					.mg-spinner {
						width: 14px;
						height: 14px;
						border: 2px solid rgba(255, 255, 255, 0.25);
						border-top-color: #0071e3;
						border-radius: 50%;
						animation: mgSpinAnim 0.8s linear infinite;
						display: inline-block;
						flex-shrink: 0;
					}
					@keyframes mgSpinAnim {
						to { transform: rotate(360deg); }
					}

					/* 原地解析规格下拉框 */
					.mg-format-select {
						height: 28px;
						border-radius: 6px;
						background: rgba(255, 255, 255, 0.1);
						border: 1px solid rgba(255, 255, 255, 0.16);
						color: #ffffff;
						font-size: 11px;
						padding: 0 8px;
						outline: none;
						width: 100%;
					}
					@media (prefers-color-scheme: light) {
						.mg-spinner {
							border-color: rgba(0, 0, 0, 0.15);
							border-top-color: #0071e3;
						}
						.mg-format-select {
							background: rgba(0, 0, 0, 0.05);
							border-color: rgba(0, 0, 0, 0.12);
							color: #1d1d1f;
						}
						#mg-sniff-toast-card {
							background: rgba(255, 255, 255, 0.94) !important;
							border: 0.5px solid rgba(0, 0, 0, 0.12) !important;
							box-shadow: 0 10px 32px rgba(0, 0, 0, 0.16) !important;
						}
						#mg-sniff-toast-card .mg-toast-title {
							color: #1d1d1f !important;
						}
						#mg-sniff-toast-card .mg-toast-body {
							color: #3a3a3c !important;
						}
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
				</div>
				<div class="mg-omnibox-wrap mg-interactive">
					<span style="font-size:11px;opacity:0.55;">🔒</span>
					<input class="mg-omnibox-input" id="mg-nav-input" type="text" spellcheck="false" autocomplete="off" value="${currentHref}" placeholder="输入网址或回车搜索..." />
				</div>
				<div class="mg-tab-strip">
					${tabsHTML}
				</div>
				<div class="mg-right-actions">
					<button class="mg-reload-btn mg-interactive" id="mg-nav-reload" title="刷新页面">
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
							<path d="M21.5 2v6h-6M2.5 22v-6h6M2 11.5a10 10 0 0 1 18.8-4.3M22 12.5a10 10 0 0 1-18.8 4.2"/>
						</svg>
					</button>
					<button class="mg-new-tab-btn mg-interactive" id="mg-new-tab" title="新建标签页 (+)">
						<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
							<line x1="12" y1="5" x2="12" y2="19"></line>
							<line x1="5" y1="12" x2="19" y2="12"></line>
						</svg>
					</button>
				</div>
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
	firstDirectURL := ""

	// 找出是否存在已嗅探的直接媒体 URL (mp4, m3u8)
	for _, mediaURL := range res.Media {
		mediaURL = strings.TrimSpace(mediaURL)
		if mediaURL == "" {
			continue
		}
		isDirect, _ := discovery.IsDirectMediaURL(mediaURL)
		if isDirect && firstDirectURL == "" {
			firstDirectURL = mediaURL
		}
	}

	// 如果检测到是热门视频网站播放页面，计数加 1
	isKnownVideoPage := strings.Contains(res.URL, "bilibili.com/video/") ||
		strings.Contains(res.URL, "youtube.com/watch") ||
		strings.Contains(res.URL, "douyin.com/video") ||
		strings.Contains(res.URL, "kuaishou.com/short-video") ||
		strings.Contains(res.URL, "weibo.com/")

	if isKnownVideoPage && res.Title != "" && !strings.HasPrefix(res.Title, "哔哩哔哩 (゜-゜)つロ 干杯~") {
		mediaFoundCount++
	}

	// 绝不在浏览器内自动调用 m.onMediaFound(cand)，以防抢占主窗口焦点和弹窗！

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

		// 浏览器右下角浮动资源解析面板
		cleanTitle := res.Title
		if cleanTitle == "" {
			cleanTitle = "网页媒体资源"
		}
		escapedTitle := strings.ReplaceAll(cleanTitle, `\`, `\\`)
		escapedTitle = strings.ReplaceAll(escapedTitle, `"`, `\"`)
		escapedTitle = strings.ReplaceAll(escapedTitle, "'", `\'`)
		escapedTitle = strings.ReplaceAll(escapedTitle, "\n", " ")
		escapedURL := strings.ReplaceAll(res.URL, `\`, `\\`)
		escapedURL = strings.ReplaceAll(escapedURL, `"`, `\"`)
		escapedURL = strings.ReplaceAll(escapedURL, "'", `\'`)
		escapedDirect := strings.ReplaceAll(firstDirectURL, `\`, `\\`)
		escapedDirect = strings.ReplaceAll(escapedDirect, `"`, `\"`)
		escapedDirect = strings.ReplaceAll(escapedDirect, "'", `\'`)

		toastScript := fmt.Sprintf(`(() => {
			try {
				window.__mg_first_direct_url = '%s';
				window.__mg_clean_title = '%s';

				// 挂载资源解析结果回调
				window.__mg_render_parsed_ui = (title, fmts, episodesCount) => {
					let toast = document.getElementById('mg-sniff-toast-card');
					if (!toast) return;
					let optionsHtml = '';
					if (fmts && fmts.length > 0) {
						optionsHtml = fmts.map(f => `+"`"+`<option value="${f.id}">${f.res} (${f.ext})</option>`+"`"+`).join('');
					} else {
						optionsHtml = '<option value="">默认最优规格</option>';
					}
					const epBadge = episodesCount > 1 ? `+"`"+`<span style="font-size:10px;background:rgba(0,113,227,0.15);color:#0071e3;padding:2px 6px;border-radius:4px;font-weight:600;">共 ${episodesCount} 集</span>`+"`"+` : '';
					toast.innerHTML = `+"`"+`
						<div style="display:flex;align-items:center;justify-content:space-between;">
							<div style="display:flex;align-items:center;gap:6px;">
								<span style="font-size:13px;">🎬</span>
								<span class="mg-toast-title" style="font-size:12px;font-weight:600;color:#ffffff;">资源解析就绪</span>
								${epBadge}
							</div>
							<button id="mg-sniff-close" style="border:none;background:transparent;color:rgba(255,255,255,0.45);font-size:14px;cursor:pointer;padding:2px 4px;line-height:1;">✕</button>
						</div>
						<div class="mg-toast-body" style="font-size:12px;line-height:1.4;color:rgba(255,255,255,0.85);font-weight:500;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;" title="${title}">${title}</div>
						<div style="display:flex;flex-direction:column;gap:5px;margin-top:2px;">
							<label style="font-size:11px;color:rgba(255,255,255,0.6);">选择清晰度规格:</label>
							<select class="mg-format-select" id="mg-card-fmt">${optionsHtml}</select>
						</div>
						<div style="display:flex;align-items:center;justify-content:flex-end;gap:8px;margin-top:4px;">
							<button id="mg-sniff-cancel" style="height:26px;padding:0 12px;border-radius:6px;border:none;background:rgba(255,255,255,0.1);color:rgba(255,255,255,0.7);font-size:11px;cursor:pointer;">取消</button>
							<button id="mg-sniff-confirm-btn" style="height:26px;padding:0 14px;border-radius:6px;border:none;background:#0071e3;color:#ffffff;font-size:11px;font-weight:600;cursor:pointer;">开始下载</button>
						</div>
					`+"`"+`;
					toast.querySelector('#mg-sniff-close').onclick = () => toast.remove();
					toast.querySelector('#mg-sniff-cancel').onclick = () => toast.remove();
					toast.querySelector('#mg-sniff-confirm-btn').onclick = () => {
						const selFmt = toast.querySelector('#mg-card-fmt').value;
						window.location.href = 'mediago-sniff://confirm-download?fmtId=' + encodeURIComponent(selFmt);
					};
				};

				// 挂载解析失败回调 (支持直链兜底)
				window.__mg_render_extract_error = (errMsg) => {
					let toast = document.getElementById('mg-sniff-toast-card');
					if (!toast) return;
					const directURL = window.__mg_first_direct_url || '';
					let fallbackHtml = '';
					if (directURL) {
						fallbackHtml = '<button id="mg-sniff-direct-btn" style="height:26px;padding:0 10px;border-radius:6px;border:none;background:#34c759;color:#ffffff;font-size:11px;font-weight:600;cursor:pointer;">直接下载嗅探直链</button>';
					}
					toast.innerHTML = `+"`"+`
						<div style="display:flex;align-items:center;justify-content:space-between;">
							<div style="display:flex;align-items:center;gap:6px;">
								<span style="font-size:13px;">⚠️</span>
								<span class="mg-toast-title" style="font-size:12px;font-weight:600;color:#ff453a;">视频解析受限</span>
							</div>
							<button id="mg-sniff-close" style="border:none;background:transparent;color:rgba(255,255,255,0.45);font-size:14px;cursor:pointer;padding:2px 4px;line-height:1;">✕</button>
						</div>
						<div style="font-size:11px;line-height:1.4;color:rgba(255,255,255,0.7);">${errMsg}</div>
						<div style="display:flex;align-items:center;justify-content:flex-end;gap:8px;margin-top:4px;">
							<button id="mg-sniff-ignore" style="height:26px;padding:0 10px;border-radius:6px;border:none;background:rgba(255,255,255,0.1);color:rgba(255,255,255,0.7);font-size:11px;cursor:pointer;">忽略</button>
							${fallbackHtml}
						</div>
					`+"`"+`;
					toast.querySelector('#mg-sniff-close').onclick = () => toast.remove();
					toast.querySelector('#mg-sniff-ignore').onclick = () => toast.remove();
					if (toast.querySelector('#mg-sniff-direct-btn')) {
						toast.querySelector('#mg-sniff-direct-btn').onclick = () => {
							window.location.href = 'mediago-sniff://direct-download?directUrl=' + encodeURIComponent(directURL) + '&title=' + encodeURIComponent(window.__mg_clean_title || '嗅探直链视频');
						};
					}
				};

				// 挂载下载提交成功反馈
				window.__mg_render_success = () => {
					let toast = document.getElementById('mg-sniff-toast-card');
					if (!toast) return;
					toast.innerHTML = '<div style="font-size:12px;color:#34c759;font-weight:600;padding:10px 0;text-align:center;">✓ 已加入下载队列，在主窗口查看进度</div>';
					setTimeout(() => { if (toast) toast.remove(); }, 2200);
				};

				// 渲染初始嗅探卡片
				let toast = document.getElementById('mg-sniff-toast-card');
				if (!toast) {
					toast = document.createElement('div');
					toast.id = 'mg-sniff-toast-card';
					document.documentElement.appendChild(toast);
				}
				toast.innerHTML = `+"`"+`
					<div style="display:flex;align-items:center;justify-content:space-between;">
						<div style="display:flex;align-items:center;gap:6px;">
							<div class="mg-pulse-dot"></div>
							<span class="mg-toast-title" style="font-size:12px;font-weight:600;color:#ffffff;">发现可下载媒体</span>
						</div>
						<button id="mg-sniff-close" style="border:none;background:transparent;color:rgba(255,255,255,0.45);font-size:14px;cursor:pointer;padding:2px 4px;line-height:1;">✕</button>
					</div>
					<div class="mg-toast-body" style="font-size:12px;line-height:1.4;color:rgba(255,255,255,0.85);font-weight:500;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;">%s</div>
					<div style="display:flex;align-items:center;justify-content:space-between;margin-top:2px;">
						<span style="font-size:10px;color:rgba(255,255,255,0.5);">检测到 %d 个媒体资源</span>
						<div style="display:flex;align-items:center;gap:8px;">
							<button id="mg-sniff-ignore" style="height:26px;padding:0 10px;border-radius:6px;border:none;background:rgba(255,255,255,0.1);color:rgba(255,255,255,0.7);font-size:11px;cursor:pointer;">忽略</button>
							<button id="mg-sniff-download-btn" style="height:26px;padding:0 12px;border-radius:6px;border:none;background:#0071e3;color:#ffffff;font-size:11px;font-weight:600;cursor:pointer;">立即下载</button>
						</div>
					</div>
				`+"`"+`;
				toast.querySelector('#mg-sniff-close').onclick = () => toast.remove();
				toast.querySelector('#mg-sniff-ignore').onclick = () => toast.remove();
				toast.querySelector('#mg-sniff-download-btn').onclick = () => {
					// 原地切换为解析中 UI (带动态旋转菊花)
					toast.innerHTML = `+"`"+`
						<div style="display:flex;align-items:center;gap:8px;padding:8px 0;">
							<div class="mg-spinner"></div>
							<span style="font-size:12px;color:#0071e3;font-weight:500;">正在深入解析视频规格与清晰度...</span>
						</div>
					`+"`"+`;
					// 触发后台深度解析
					window.location.href = 'mediago-sniff://extract?url=' + encodeURIComponent('%s');
				};
			} catch (e) {}
		})()`, escapedDirect, escapedTitle, escapedTitle, mediaFoundCount, escapedURL)
		_, _ = page.Eval(toastScript)
	}
}
