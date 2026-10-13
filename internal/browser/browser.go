package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/google/uuid"
	"mediago/internal/config"
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
		Title:                "MediaGo 专用浏览器",
		URL:                  initialURL,
		Width:                1180,
		Height:               760,
		MinWidth:             840,
		MinHeight:            520,
		TitleBarStyle:        mygo.TitleBarHiddenInset,
		TitleBarHeight:       44,
		Vibrancy:             mygo.VibrancySidebar,
		BackgroundColor:      "light-dark(#f6f6f8, #1c1c20)",
		TrafficLightPosition: &mygo.Point{X: 16, Y: 14},
		StateKey:             "mediago_browser_window",
		Page: mygo.PageOptions{
			DevTools:  mygo.DevToolsAuto,
			UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		},
	})

	m.window = win

	// 关闭浏览器窗口即彻底销毁 WebKit 实例并回收进程资源 (不再拦截关闭转为隐藏驻留)
	// 登录态由 WKWebView 默认的持久化数据存储负责落盘，重新打开浏览器仍然保留
	win.OnClosed(func() {
		m.mu.Lock()
		m.window = nil
		m.lastCand = nil
		m.mu.Unlock()
	})

	page := win.Page()
	if page == nil {
		return
	}

	// 拦截 target="_blank" 与 window.open()：一律在内置浏览器窗口内原地打开
	// (mygo 默认会把 http(s) 新窗口请求交给系统默认浏览器，必须显式覆盖)
	page.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		u, err := url.Parse(req.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil // 非网页协议 (如 bilibili://) 直接忽略，既不弹窗也不外跳
		}
		go page.LoadURL(req.URL)
		return nil // 返回 nil 拒绝弹出原生新窗口
	})

	// 监听页面导航与加载完成，注入 Gorex 风格顶栏与触发独立媒体嗅探
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if strings.HasPrefix(e.URL, "mediago-sniff://extract") {
			e.PreventDefault()
			decURL := ""
			if u, err := url.Parse(e.URL); err == nil {
				decURL = u.Query().Get("url")
			}
			if decURL == "" {
				raw := strings.TrimPrefix(e.URL, "mediago-sniff://extract")
				raw = strings.TrimPrefix(raw, "/")
				raw = strings.TrimPrefix(raw, "?")
				if q, err := url.ParseQuery(raw); err == nil {
					decURL = q.Get("url")
				}
			}
			if decURL == "" {
				decURL = m.GetCurrentURL()
			}
			if decURL == "" {
				return
			}

			// 在后台异步深度探测，主窗口完全不弹窗
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()

				cand, err := m.extractor.Extract(ctx, decURL)
				if err != nil || cand == nil {
					errStr := "页面视频解析受限"
					if err != nil {
						errStr = err.Error()
					}
					if len([]rune(errStr)) > 35 {
						errStr = string([]rune(errStr)[:32]) + "..."
					}
					script := fmt.Sprintf(`if (window.__mg_render_extract_error) window.__mg_render_extract_error(%s);`, jsString(errStr))
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

				// 分集列表与当前分 P 一并下发，浏览器内卡片可直接选择分集下载
				eps := make([]epItem, 0, len(cand.Episodes))
				for _, ep := range cand.Episodes {
					eps = append(eps, epItem{I: ep.Index, T: ep.Title})
				}
				eb, _ := json.Marshal(eps)

				script := fmt.Sprintf(`if (window.__mg_render_parsed_ui) window.__mg_render_parsed_ui(%s, %s, %s, %d);`,
					jsString(cand.Title), string(fb), string(eb), cand.CurrentPage)
				_, _ = page.Eval(script)
			}()
			return
		}

		if strings.HasPrefix(e.URL, "mediago-sniff://confirm-download") {
			e.PreventDefault()
			fmtID, mode, epsParam := parseConfirmParams(e.URL)

			m.mu.Lock()
			cand := m.lastCand
			outDir := m.downloadDir
			m.mu.Unlock()

			if cand != nil && m.onSubmitTask != nil {
				for _, task := range buildConfirmTasks(cand, fmtID, mode, epsParam, outDir) {
					m.onSubmitTask(task)
				}
			}
			_, _ = page.Eval(`if (window.__mg_render_success) window.__mg_render_success();`)
			return
		}

		if strings.HasPrefix(e.URL, "mediago-sniff://direct-download") {
			e.PreventDefault()
			directURL := ""
			tTitle := ""
			if u, err := url.Parse(e.URL); err == nil {
				directURL = u.Query().Get("directUrl")
				tTitle = u.Query().Get("title")
			}
			if directURL == "" {
				raw := strings.TrimPrefix(e.URL, "mediago-sniff://direct-download")
				raw = strings.TrimPrefix(raw, "/")
				raw = strings.TrimPrefix(raw, "?")
				if q, err := url.ParseQuery(raw); err == nil {
					directURL = q.Get("directUrl")
					tTitle = q.Get("title")
				}
			}
			tTitle = sanitizeFilename(tTitle)
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

// isDarkMode 检测系统或配置是否处于深色模式
func (m *Manager) isDarkMode() bool {
	cfg := config.Global().Get()
	if cfg.Theme == "dark" {
		return true
	}
	if cfg.Theme == "light" {
		return false
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
		if err == nil && strings.TrimSpace(string(out)) == "Dark" {
			return true
		}
		return false
	}
	return false
}

// injectGorexTopBar 注入 Gorex 风格顶栏: 交通灯避让、输入栏聚焦让出40%、渐进模糊遮罩、多标签栏+、圆角容器主体
func (m *Manager) injectGorexTopBar(page *mygo.Page) {
	themeMode := "light"
	if m.isDarkMode() {
		themeMode = "dark"
	}

	script := `(() => {
		try {
			if (window !== window.top) return; // 仅在顶层窗口运行

			const mgAppTheme = "` + themeMode + `";
			if (mgAppTheme === 'light') {
				document.documentElement.classList.add('mg-theme-light');
				document.documentElement.classList.remove('mg-theme-dark');
			} else {
				document.documentElement.classList.add('mg-theme-dark');
				document.documentElement.classList.remove('mg-theme-light');
			}

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
				document.documentElement.appendChild(styleEl);
			}
			styleEl.textContent = ` + "`" + `
					/* 页面主体视口统一下移 44px，从顶栏下边界开始 */
					html {
						padding-top: 44px !important;
						box-sizing: border-box !important;
					}
					body {
						box-sizing: border-box !important;
					}

					/* 统一下移各大网站的顶部固定栏 (包括 YouTube, Bilibili, 抖音, 微博等) */
					#masthead-container,
					ytd-masthead,
					#bili-header-container,
					.bili-header,
					.bili-header__bar,
					.mini-header,
					.m-top-bar,
					header.bili-header,
					header.bili-header--sticky,
					#header-bar,
					.bili-feed-header,
					.bili-header--mini,
					#internationalHeader,
					.video-toolbar-container,
					.douyin-header,
					.wbpro-topbar,
					header[class*="header"],
					div[class*="header-bar"],
					nav[class*="nav"] {
						top: 44px !important;
					}

					/* YouTube 专用深度补偿: 确保 masthead 和 page-manager 均不遮挡 */
					ytd-app {
						--ytd-masthead-height: 56px !important;
					}
					#masthead-container {
						top: 44px !important;
					}
					ytd-masthead {
						top: 44px !important;
					}
					#page-manager {
						margin-top: 100px !important;
					}
					#guide-wrapper, #guide-content {
						top: 100px !important;
					}

					/* Bilibili 专用深度补偿: 确保顶部固定栏避让 */
					.bili-header, #bili-header-container {
						top: 44px !important;
					}
					.bili-feed-header {
						top: 44px !important;
					}

					/* 核心色彩与视觉变量 (默认深色模式，浅色模式通过 prefers-color-scheme 优雅覆盖) */
					:root {
						--mg-topbar-bg: rgba(28, 28, 32, 0.90);
						--mg-topbar-border: rgba(255, 255, 255, 0.08);
						--mg-topbar-shadow: none;
						--mg-btn-color: rgba(255, 255, 255, 0.72);
						--mg-btn-hover-bg: rgba(255, 255, 255, 0.12);
						--mg-btn-hover-color: #ffffff;
						--mg-omni-bg: rgba(255, 255, 255, 0.07);
						--mg-omni-border: rgba(255, 255, 255, 0.12);
						--mg-omni-text: #ffffff;
						--mg-omni-icon: rgba(255, 255, 255, 0.55);
						--mg-omni-focus-bg: rgba(255, 255, 255, 0.12);
						--mg-omni-focus-border: rgba(255, 255, 255, 0.35);
						--mg-omni-focus-ring: rgba(255, 255, 255, 0.06);
						--mg-strip-bg: rgba(255, 255, 255, 0.04);
						--mg-strip-border: rgba(255, 255, 255, 0.06);
						--mg-tab-color: rgba(255, 255, 255, 0.65);
						--mg-tab-hover-bg: rgba(255, 255, 255, 0.08);
						--mg-tab-hover-color: rgba(255, 255, 255, 0.92);
						--mg-tab-active-bg: rgba(255, 255, 255, 0.16);
						--mg-tab-active-color: #ffffff;
						--mg-tab-active-border: rgba(255, 255, 255, 0.22);
						--mg-tab-active-shadow: 0 1px 4px rgba(0, 0, 0, 0.25);
						--mg-tab-close-color: rgba(255, 255, 255, 0.5);
						--mg-tab-close-hover-bg: rgba(255, 255, 255, 0.2);
						--mg-tab-close-hover-color: #ffffff;
						--mg-toast-bg: rgba(28, 28, 32, 0.92);
						--mg-toast-border: rgba(255, 255, 255, 0.16);
						--mg-toast-shadow: 0 10px 32px rgba(0, 0, 0, 0.5);
						--mg-toast-title: #ffffff;
						--mg-toast-body: rgba(255, 255, 255, 0.85);
						--mg-toast-muted: rgba(255, 255, 255, 0.5);
						--mg-toast-btn-sec-bg: rgba(255, 255, 255, 0.1);
						--mg-toast-btn-sec-color: rgba(255, 255, 255, 0.7);
						--mg-close-btn-color: rgba(255, 255, 255, 0.45);
						--mg-close-btn-hover-bg: rgba(255, 255, 255, 0.15);
						--mg-close-btn-hover-color: #ffffff;
						--mg-spinner-track: rgba(255, 255, 255, 0.25);
						--mg-select-bg: rgba(255, 255, 255, 0.1);
						--mg-select-border: rgba(255, 255, 255, 0.16);
						--mg-select-color: #ffffff;
					}

					:root.mg-theme-light,
					.mg-theme-light {
						--mg-topbar-bg: rgba(246, 246, 248, 0.94);
						--mg-topbar-border: rgba(0, 0, 0, 0.09);
						--mg-topbar-shadow: 0 1px 3px rgba(0, 0, 0, 0.04);
						--mg-btn-color: rgba(0, 0, 0, 0.65);
						--mg-btn-hover-bg: rgba(0, 0, 0, 0.08);
						--mg-btn-hover-color: #000000;
						--mg-omni-bg: rgba(0, 0, 0, 0.05);
						--mg-omni-border: rgba(0, 0, 0, 0.1);
						--mg-omni-text: #1d1d1f;
						--mg-omni-icon: rgba(0, 0, 0, 0.45);
						--mg-omni-focus-bg: #ffffff;
						--mg-omni-focus-border: rgba(0, 113, 227, 0.6);
						--mg-omni-focus-ring: rgba(0, 113, 227, 0.12);
						--mg-strip-bg: rgba(0, 0, 0, 0.05);
						--mg-strip-border: rgba(0, 0, 0, 0.08);
						--mg-tab-color: rgba(0, 0, 0, 0.65);
						--mg-tab-hover-bg: rgba(0, 0, 0, 0.06);
						--mg-tab-hover-color: #000000;
						--mg-tab-active-bg: #ffffff;
						--mg-tab-active-color: #1d1d1f;
						--mg-tab-active-border: rgba(0, 0, 0, 0.12);
						--mg-tab-active-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
						--mg-tab-close-color: rgba(0, 0, 0, 0.45);
						--mg-tab-close-hover-bg: rgba(0, 0, 0, 0.1);
						--mg-tab-close-hover-color: #000000;
						--mg-toast-bg: rgba(255, 255, 255, 0.96);
						--mg-toast-border: rgba(0, 0, 0, 0.12);
						--mg-toast-shadow: 0 10px 32px rgba(0, 0, 0, 0.16);
						--mg-toast-title: #1d1d1f;
						--mg-toast-body: #3a3a3c;
						--mg-toast-muted: rgba(0, 0, 0, 0.5);
						--mg-toast-btn-sec-bg: rgba(0, 0, 0, 0.06);
						--mg-toast-btn-sec-color: rgba(0, 0, 0, 0.75);
						--mg-close-btn-color: rgba(0, 0, 0, 0.45);
						--mg-close-btn-hover-bg: rgba(0, 0, 0, 0.08);
						--mg-close-btn-hover-color: #000000;
						--mg-spinner-track: rgba(0, 0, 0, 0.15);
						--mg-select-bg: rgba(0, 0, 0, 0.05);
						--mg-select-border: rgba(0, 0, 0, 0.12);
						--mg-select-color: #1d1d1f;
					}

					@media (prefers-color-scheme: light) {
						:root {
							--mg-topbar-bg: rgba(246, 246, 248, 0.94);
							--mg-topbar-border: rgba(0, 0, 0, 0.09);
							--mg-topbar-shadow: 0 1px 3px rgba(0, 0, 0, 0.04);
							--mg-btn-color: rgba(0, 0, 0, 0.65);
							--mg-btn-hover-bg: rgba(0, 0, 0, 0.08);
							--mg-btn-hover-color: #000000;
							--mg-omni-bg: rgba(0, 0, 0, 0.05);
							--mg-omni-border: rgba(0, 0, 0, 0.1);
							--mg-omni-text: #1d1d1f;
							--mg-omni-icon: rgba(0, 0, 0, 0.45);
							--mg-omni-focus-bg: #ffffff;
							--mg-omni-focus-border: rgba(0, 113, 227, 0.6);
							--mg-omni-focus-ring: rgba(0, 113, 227, 0.12);
							--mg-strip-bg: rgba(0, 0, 0, 0.05);
							--mg-strip-border: rgba(0, 0, 0, 0.08);
							--mg-tab-color: rgba(0, 0, 0, 0.65);
							--mg-tab-hover-bg: rgba(0, 0, 0, 0.06);
							--mg-tab-hover-color: #000000;
							--mg-tab-active-bg: #ffffff;
							--mg-tab-active-color: #1d1d1f;
							--mg-tab-active-border: rgba(0, 0, 0, 0.12);
							--mg-tab-active-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
							--mg-tab-close-color: rgba(0, 0, 0, 0.45);
							--mg-tab-close-hover-bg: rgba(0, 0, 0, 0.1);
							--mg-tab-close-hover-color: #000000;
							--mg-toast-bg: rgba(255, 255, 255, 0.96);
							--mg-toast-border: rgba(0, 0, 0, 0.12);
							--mg-toast-shadow: 0 10px 32px rgba(0, 0, 0, 0.16);
							--mg-toast-title: #1d1d1f;
							--mg-toast-body: #3a3a3c;
							--mg-toast-muted: rgba(0, 0, 0, 0.5);
							--mg-toast-btn-sec-bg: rgba(0, 0, 0, 0.06);
							--mg-toast-btn-sec-color: rgba(0, 0, 0, 0.75);
							--mg-close-btn-color: rgba(0, 0, 0, 0.45);
							--mg-close-btn-hover-bg: rgba(0, 0, 0, 0.08);
							--mg-close-btn-hover-color: #000000;
							--mg-spinner-track: rgba(0, 0, 0, 0.15);
							--mg-select-bg: rgba(0, 0, 0, 0.05);
							--mg-select-border: rgba(0, 0, 0, 0.12);
							--mg-select-color: #1d1d1f;
						}
					}

					/* 顶栏: 类似 gorex titleBar (高度 44px, 交通灯避让 88px, 毛玻璃底板) */
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
						background: var(--mg-topbar-bg) !important;
						backdrop-filter: blur(28px) saturate(190%) !important;
						-webkit-backdrop-filter: blur(28px) saturate(190%) !important;
						border-bottom: 0.5px solid var(--mg-topbar-border) !important;
						box-shadow: var(--mg-topbar-shadow) !important;
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

					/* 导航控制区 (后退/前进，左侧留出舒适呼吸间距) */
					.mg-nav-actions {
						display: flex;
						align-items: center;
						gap: 4px;
						margin-left: 10px !important;
						flex-shrink: 0;
					}
					.mg-icon-btn {
						width: 28px;
						height: 28px;
						border-radius: 7px;
						border: none;
						background: transparent;
						color: var(--mg-btn-color) !important;
						display: flex;
						align-items: center;
						justify-content: center;
						cursor: pointer;
						transition: all 0.15s ease;
						font-size: 17px;
						font-weight: 500;
					}
					.mg-icon-btn:hover {
						background: var(--mg-btn-hover-bg) !important;
						color: var(--mg-btn-hover-color) !important;
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
						background: var(--mg-omni-bg) !important;
						border: 1px solid var(--mg-omni-border) !important;
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
						background: var(--mg-omni-focus-bg) !important;
						border-color: var(--mg-omni-focus-border) !important;
						box-shadow: 0 0 0 3px var(--mg-omni-focus-ring) !important;
					}

					/* 输入框文字渐进模糊遮罩 */
					.mg-omnibox-input {
						flex: 1;
						background: transparent;
						border: none;
						outline: none;
						color: var(--mg-omni-text) !important;
						font-size: 12px;
						font-family: inherit;
						width: 100%;
						-webkit-mask-image: linear-gradient(to right, black calc(100% - 22px), transparent 100%);
						mask-image: linear-gradient(to right, black calc(100% - 22px), transparent 100%);
					}

					/* Gorex 风格多标签栏轨道 */
					.mg-tab-strip {
						flex: 1;
						display: flex;
						align-items: center;
						gap: 4px;
						height: 32px;
						padding: 2px !important;
						border-radius: 16px;
						background: var(--mg-strip-bg) !important;
						border: 0.5px solid var(--mg-strip-border) !important;
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
						color: var(--mg-tab-color) !important;
						cursor: pointer;
						transition: all 0.18s ease;
						flex-shrink: 1;
						position: relative;
						border: 0.5px solid transparent;
					}
					.mg-tab-item:hover {
						background: var(--mg-tab-hover-bg) !important;
						color: var(--mg-tab-hover-color) !important;
					}
					.mg-tab-item.active {
						background: var(--mg-tab-active-bg) !important;
						color: var(--mg-tab-active-color) !important;
						border-color: var(--mg-tab-active-border) !important;
						box-shadow: var(--mg-tab-active-shadow) !important;
						font-weight: 500;
					}

					/* 标签标题超出渐变淡出 (显式指定文字色彩，杜绝宿主页面全局 CSS 污染) */
					.mg-tab-title {
						flex: 1;
						white-space: nowrap;
						overflow: hidden;
						color: var(--mg-tab-color) !important;
						font-size: 12px !important;
						font-weight: 500 !important;
						line-height: 1.2 !important;
						font-family: inherit !important;
						-webkit-mask-image: linear-gradient(to right, black calc(100% - 16px), transparent 100%);
						mask-image: linear-gradient(to right, black calc(100% - 16px), transparent 100%);
					}
					.mg-tab-item.active .mg-tab-title {
						color: var(--mg-tab-active-color) !important;
						font-weight: 600 !important;
					}
					.mg-tab-item:hover .mg-tab-title {
						color: var(--mg-tab-hover-color) !important;
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
						color: var(--mg-tab-close-color) !important;
						opacity: 0;
						transition: opacity 0.15s, background 0.15s;
					}
					.mg-tab-item:hover .mg-tab-close,
					.mg-tab-item.active .mg-tab-close {
						opacity: 1;
					}
					.mg-tab-close:hover {
						background: var(--mg-tab-close-hover-bg) !important;
						color: var(--mg-tab-close-hover-color) !important;
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
						color: var(--mg-btn-color) !important;
						display: flex;
						align-items: center;
						justify-content: center;
						cursor: pointer;
						transition: all 0.15s ease;
						flex-shrink: 0;
					}
					.mg-new-tab-btn:hover, .mg-reload-btn:hover {
						background: var(--mg-btn-hover-bg) !important;
						color: var(--mg-btn-hover-color) !important;
					}

					/* 统一小关闭按钮 (适配深浅模式) */
					.mg-close-btn {
						width: 20px;
						height: 20px;
						border-radius: 10px;
						border: none;
						background: transparent;
						color: var(--mg-close-btn-color) !important;
						font-size: 13px;
						display: flex;
						align-items: center;
						justify-content: center;
						cursor: pointer;
						line-height: 1;
						transition: all 0.15s ease;
						padding: 0;
					}
					.mg-close-btn:hover {
						background: var(--mg-close-btn-hover-bg) !important;
						color: var(--mg-close-btn-hover-color) !important;
					}

					/* 浏览器右下角浮动媒体嗅探卡片 (Toast Modal) */
					#mg-sniff-toast-card {
						position: fixed !important;
						bottom: 24px !important;
						right: 24px !important;
						width: 330px !important;
						padding: 14px 16px !important;
						border-radius: 12px !important;
						background: var(--mg-toast-bg) !important;
						backdrop-filter: blur(28px) saturate(190%) !important;
						-webkit-backdrop-filter: blur(28px) saturate(190%) !important;
						border: 0.5px solid var(--mg-toast-border) !important;
						box-shadow: var(--mg-toast-shadow) !important;
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
					#mg-sniff-toast-card .mg-toast-title {
						color: var(--mg-toast-title) !important;
					}
					#mg-sniff-toast-card .mg-toast-body {
						color: var(--mg-toast-body) !important;
					}
					#mg-sniff-toast-card .mg-toast-muted {
						color: var(--mg-toast-muted) !important;
					}
					#mg-sniff-toast-card .mg-btn-secondary {
						background: var(--mg-toast-btn-sec-bg) !important;
						color: var(--mg-toast-btn-sec-color) !important;
					}
					#mg-sniff-toast-card .mg-btn-secondary:hover {
						opacity: 0.85;
					}

					/* 动态旋转菊花 Loading 动画 */
					.mg-spinner {
						width: 14px;
						height: 14px;
						border: 2px solid var(--mg-spinner-track) !important;
						border-top-color: #0071e3 !important;
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
						background: var(--mg-select-bg) !important;
						border: 1px solid var(--mg-select-border) !important;
						color: var(--mg-select-color) !important;
						font-size: 11px;
						padding: 0 8px;
						outline: none;
						width: 100%;
					}
				` + "`" + `;

			// 3. 构建/更新顶栏 DOM
			const safeSetHTML = (el, htmlStr) => {
				try {
					if (window.trustedTypes) {
						if (!window.__mg_tt_policy) {
							try {
								window.__mg_tt_policy = window.trustedTypes.createPolicy('mediago', { createHTML: s => s });
							} catch (e) {
								window.__mg_tt_policy = window.trustedTypes.defaultPolicy;
							}
						}
						if (window.__mg_tt_policy) {
							el.innerHTML = window.__mg_tt_policy.createHTML(htmlStr);
							return;
						}
					}
				} catch (e) {}
				try {
					el.innerHTML = htmlStr;
				} catch (e) {
					try {
						const parser = new DOMParser();
						const doc = parser.parseFromString(htmlStr, 'text/html');
						while (el.firstChild) el.removeChild(el.firstChild);
						while (doc.body.firstChild) el.appendChild(doc.body.firstChild);
					} catch (e2) {}
				}
			};

			let bar = document.getElementById('mg-chrome-topbar');
			if (!bar) {
				bar = document.createElement('div');
				bar.id = 'mg-chrome-topbar';
				document.documentElement.appendChild(bar);
			}
			bar.className = (mgAppTheme === 'light' ? 'mg-theme-light' : 'mg-theme-dark');

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

			safeSetHTML(bar, ` + "`" + `
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
						<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
							<line x1="12" y1="5" x2="12" y2="19"></line>
							<line x1="5" y1="12" x2="19" y2="12"></line>
						</svg>
					</button>
				</div>
			` + "`" + `);

			// 4. 事件绑定
			const backBtn = bar.querySelector('#mg-nav-back');
			const forwardBtn = bar.querySelector('#mg-nav-forward');
			const reloadBtn = bar.querySelector('#mg-nav-reload');
			const input = bar.querySelector('#mg-nav-input');
			const newTabBtn = bar.querySelector('#mg-new-tab');

			backBtn.onclick = () => window.history.back();
			forwardBtn.onclick = () => window.history.forward();
			reloadBtn.onclick = () => window.location.reload();

			const triggerNav = () => {
				let val = input.value.trim();
				if (!val) return;
				if (!val.includes('.') && !val.startsWith('http')) {
					val = 'https://search.bilibili.com/all?keyword=' + encodeURIComponent(val);
				} else if (!val.startsWith('http://') && !val.startsWith('https://')) {
					val = 'https://' + val;
				}
				window.location.href = val;
			};

			input.addEventListener('keydown', (e) => {
				if (e.key === 'Enter' || e.keyCode === 13 || e.which === 13) {
					e.preventDefault();
					triggerNav();
				}
			});

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

			// 5. 顶栏守护机制 (保证在 YouTube 等单页应用重排时顶栏不丢失)
			if (!window.__mg_topbar_watcher_installed) {
				window.__mg_topbar_watcher_installed = true;
				window.addEventListener('yt-navigate-finish', () => {
					const curBar = document.getElementById('mg-chrome-topbar');
					if (!curBar && document.documentElement) {
						document.documentElement.appendChild(bar);
					}
				});
				window.addEventListener('popstate', () => {
					const curBar = document.getElementById('mg-chrome-topbar');
					if (!curBar && document.documentElement) {
						document.documentElement.appendChild(bar);
					}
				});
			}

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

// epItem 分集条目 (下发给浏览器内浮动卡片，用于分 P 选择)
type epItem struct {
	I int    `json:"i"`
	T string `json:"t"`
}

// cardData 浏览器内浮动卡片所需的页面数据 (整体 JSON 注入，避免手工转义引发脚本错误)
type cardData struct {
	Direct     string `json:"direct"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	MediaCount int    `json:"mediaCount"`
}

// parseConfirmParams 解析浏览器卡片"确认下载"自定义协议的查询参数
// 兼容标准 url.Parse 与原始查询串两种形式；mode 缺省为 current
func parseConfirmParams(rawURL string) (fmtID, mode, eps string) {
	mode = "current"
	read := func(q url.Values) {
		if v := q.Get("fmtId"); v != "" {
			fmtID = v
		}
		if v := q.Get("mode"); v != "" {
			mode = v
		}
		if v := q.Get("eps"); v != "" {
			eps = v
		}
	}
	if u, err := url.Parse(rawURL); err == nil {
		read(u.Query())
	}
	if fmtID == "" {
		raw := strings.TrimPrefix(rawURL, "mediago-sniff://confirm-download")
		raw = strings.TrimPrefix(raw, "/")
		raw = strings.TrimPrefix(raw, "?")
		if q, err := url.ParseQuery(raw); err == nil {
			read(q)
		}
	}
	return fmtID, mode, eps
}

// buildConfirmTasks 根据浏览器卡片的确认参数构建下载任务
// mode=batch 时按 eps 勾选的分 P 逐集建任务 (对齐主窗口批量下载)，否则仅建当前分 P 的单个任务
func buildConfirmTasks(cand *model.Candidate, fmtID, mode, epsParam, outDir string) []*model.Task {
	if cand == nil {
		return nil
	}

	var selectedFmt *model.MediaFormat
	for i := range cand.Formats {
		if cand.Formats[i].FormatID == fmtID {
			selectedFmt = &cand.Formats[i]
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
		if selectedFmt.Protocol == "extractor" && selectedFmt.FormatID != "" {
			// extractor 协议下 FormatID 即 yt-dlp 选择器，交由引擎按该规格下载
			dlURL = selectedFmt.FormatID
		} else if selectedFmt.URL != "" {
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

	cleanTitle := sanitizeFilename(strings.TrimSpace(cand.Title))
	if cleanTitle == "" {
		cleanTitle = "视频资源"
	}

	newTask := func(sourceURL, taskTitle string) *model.Task {
		if sourceURL == "" {
			sourceURL = cand.SourceURL
		}
		now := time.Now()
		return &model.Task{
			ID:             uuid.New().String(),
			Title:          taskTitle,
			SourceURL:      sourceURL,
			DownloadURL:    dlURL,
			Engine:         engineType,
			Status:         model.StatusQueued,
			TotalBytes:     totalBytes,
			OutputDir:      outDir,
			OutputFilename: fmt.Sprintf("%s.%s", sanitizeFilename(taskTitle), ext),
			Priority:       1,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
	}

	// 批量模式: 按用户勾选的分 P 逐集创建任务
	if mode == "batch" && len(cand.Episodes) > 0 {
		selected := make(map[int]bool)
		for _, part := range strings.Split(epsParam, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && n > 0 {
				selected[n] = true
			}
		}
		var tasks []*model.Task
		for _, ep := range cand.Episodes {
			if !selected[ep.Index] {
				continue
			}
			tasks = append(tasks, newTask(ep.URL, fmt.Sprintf("%s [P%02d %s]", cleanTitle, ep.Index, sanitizeFilename(ep.Title))))
		}
		if len(tasks) > 0 {
			return tasks
		}
	}

	// 单集模式: 多分集视频在标题中附加当前分 P 序号与分集名
	taskTitle := cleanTitle
	if len(cand.Episodes) > 1 && cand.CurrentPage > 0 && cand.CurrentPage <= len(cand.Episodes) {
		curEp := cand.Episodes[cand.CurrentPage-1]
		taskTitle = fmt.Sprintf("%s [P%02d %s]", cleanTitle, curEp.Index, sanitizeFilename(curEp.Title))
	}
	return []*model.Task{newTask(cand.SourceURL, taskTitle)}
}

// sniffPageMedia 探测当前页面的视频与媒体元素
func (m *Manager) sniffPageMedia(page *mygo.Page) {
	// 等待 1 秒待动态播放器或异步脚本初始化完成
	time.Sleep(1 * time.Second)

	jsScript := `(() => {
		const urls = new Set();
		// 1. 查找 video 标签与 source (包括 currentSrc)
		document.querySelectorAll('video').forEach(v => {
			if (v.src && !v.src.startsWith('blob:')) urls.add(v.src);
			if (v.currentSrc && !v.currentSrc.startsWith('blob:')) urls.add(v.currentSrc);
			v.querySelectorAll('source').forEach(s => {
				if (s.src && !s.src.startsWith('blob:')) urls.add(s.src);
			});
		});
		// 2. 查找 PerformanceResourceTiming 中的媒体
		if (window.performance && window.performance.getEntriesByType) {
			const resources = window.performance.getEntriesByType('resource');
			for (const r of resources) {
				const u = r.name;
				if (u.includes('.m3u8') || u.includes('.mp4') || r.initiatorType === 'video' || r.initiatorType === 'media') {
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
		strings.Contains(res.URL, "weibo.com/") ||
		strings.Contains(res.URL, "x.com/") ||
		strings.Contains(res.URL, "twitter.com/") ||
		strings.Contains(res.URL, "threads.com/") ||
		strings.Contains(res.URL, "threads.net/") ||
		strings.Contains(res.URL, "facebook.com/")

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
		dataJSON, _ := json.Marshal(cardData{
			Direct:     firstDirectURL,
			Title:      cleanTitle,
			URL:        res.URL,
			MediaCount: mediaFoundCount,
		})

		// 页面数据整体以 JSON 注入，避免手工转义导致的脚本语法错误或注入风险
		toastScript := `(() => {
	try {
		const MG = ` + string(dataJSON) + `;
		window.__mg_first_direct_url = MG.direct || '';
		window.__mg_clean_title = MG.title || '';

		const safeSetHTML = (el, htmlStr) => {
			try {
				if (window.trustedTypes) {
					if (!window.__mg_tt_policy) {
						try {
							window.__mg_tt_policy = window.trustedTypes.createPolicy('mediago', { createHTML: s => s });
						} catch (e) {
							window.__mg_tt_policy = window.trustedTypes.defaultPolicy;
						}
					}
					if (window.__mg_tt_policy) {
						el.innerHTML = window.__mg_tt_policy.createHTML(htmlStr);
						return;
					}
				}
			} catch (e) {}
			try {
				el.innerHTML = htmlStr;
			} catch (e) {
				try {
					const parser = new DOMParser();
					const doc = parser.parseFromString(htmlStr, 'text/html');
					while (el.firstChild) el.removeChild(el.firstChild);
					while (doc.body.firstChild) el.appendChild(doc.body.firstChild);
				} catch (e2) {}
			}
		};

		const esc = (s) => String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

		const ensureToast = () => {
			let toast = document.getElementById('mg-sniff-toast-card');
			if (!toast) {
				toast = document.createElement('div');
				toast.id = 'mg-sniff-toast-card';
				document.documentElement.appendChild(toast);
			}
			return toast;
		};

		// ---- 分集选择状态 (由 Go 端深度解析回调写入) ----
		let EPS = [];
		let CUR = 0;
		let mgMode = 'current';
		let mgSel = {};
		let lastFormats = [];
		let lastFmtValue = '';

		const selCount = () => EPS.filter(e => mgSel[e.i]).length;

		const curEpLabel = () => {
			if (CUR <= 0) return '仅下载当前集';
			const ep = EPS.find(e => e.i === CUR);
			let t = ep && ep.t ? String(ep.t) : '';
			if (t.length > 8) t = t.slice(0, 8) + '…';
			return '仅下载当前集 (P' + CUR + (t ? ' ' + t : '') + ')';
		};

		const batchLabel = () => {
			const n = selCount();
			if (EPS.length > 0 && n === EPS.length) return '批量下载全部 ' + EPS.length + ' 集';
			return '批量下载 (已选 ' + n + ' 集)';
		};

		const confirmLabel = () => {
			if (EPS.length <= 1) return '开始下载';
			if (mgMode !== 'batch') return CUR > 0 ? '立即下载当前集 (P' + CUR + ')' : '立即下载当前集';
			const n = selCount();
			if (n === 0) return '请先选择分集';
			if (n === EPS.length) return '立即批量下载全部 ' + EPS.length + ' 集';
			return '立即批量下载已选 ' + n + ' 集';
		};

		const pillStyle = (active) => active
			? 'background:rgba(0,113,227,0.16);border:1px solid #0071e3;color:#0071e3;font-weight:600;'
			: 'background:var(--mg-toast-btn-sec-bg);border:1px solid var(--mg-toast-border);color:var(--mg-toast-btn-sec-color);';

		const fmtOptionsHtml = () => {
			if (lastFormats.length > 0) {
				return lastFormats.map(f => '<option value="' + esc(f.id) + '">' + esc(f.res) + ' (' + esc(f.ext) + ')</option>').join('');
			}
			return '<option value="">默认最优规格</option>';
		};

		// 渲染解析结果卡片 (切换模式 / 勾选分集后原地重绘)
		const renderParsedCard = () => {
			const toast = ensureToast();
			const epBadge = EPS.length > 1
				? '<span style="font-size:10px;background:rgba(0,113,227,0.15);color:#0071e3;padding:2px 6px;border-radius:4px;font-weight:600;">共 ' + EPS.length + ' 集</span>'
				: '';

			let epHtml = '';
			if (EPS.length > 1) {
				epHtml = '<div style="display:flex;gap:6px;">' +
					'<button id="mg-mode-cur" style="flex:1;height:26px;border-radius:6px;font-size:11px;cursor:pointer;' + pillStyle(mgMode !== 'batch') + '">' + esc(curEpLabel()) + '</button>' +
					'<button id="mg-mode-batch" style="flex:1;height:26px;border-radius:6px;font-size:11px;cursor:pointer;' + pillStyle(mgMode === 'batch') + '">' + esc(batchLabel()) + '</button>' +
				'</div>';
				if (mgMode === 'batch') {
					const rows = EPS.map(e => {
						const on = !!mgSel[e.i];
						return '<div class="mg-ep-row" data-ep="' + e.i + '" style="display:flex;align-items:center;gap:6px;padding:3px 6px;border-radius:5px;cursor:pointer;' + (on ? 'background:rgba(0,113,227,0.14);' : '') + '">' +
							'<span style="width:14px;height:14px;border-radius:4px;flex-shrink:0;display:flex;align-items:center;justify-content:center;font-size:10px;color:#ffffff;' + (on ? 'background:#0071e3;' : 'background:var(--mg-toast-btn-sec-bg);border:1px solid var(--mg-toast-border);') + '">' + (on ? '✓' : '') + '</span>' +
							'<span style="font-size:10px;font-weight:600;color:var(--mg-toast-muted);flex-shrink:0;">P' + String(e.i).padStart(2, '0') + '</span>' +
							'<span style="font-size:11px;color:' + (on ? '#0071e3' : 'var(--mg-toast-body)') + ';white-space:nowrap;overflow:hidden;text-overflow:ellipsis;" title="' + esc(e.t) + '">' + esc(e.t) + '</span>' +
						'</div>';
					}).join('');
					epHtml += '<div style="display:flex;align-items:center;justify-content:space-between;padding:0 2px;">' +
							'<span style="font-size:10px;color:var(--mg-toast-muted);">已选 ' + selCount() + '/' + EPS.length + ' 集</span>' +
							'<span id="mg-ep-toggleall" style="font-size:10px;color:#0071e3;cursor:pointer;">' + (selCount() === EPS.length ? '取消全选' : '全选') + '</span>' +
						'</div>' +
						'<div class="mg-ep-list" style="max-height:150px;overflow-y:auto;display:flex;flex-direction:column;gap:2px;padding:3px;border-radius:6px;background:var(--mg-toast-btn-sec-bg);">' + rows + '</div>';
				}
			}

			const confirmDisabled = EPS.length > 1 && mgMode === 'batch' && selCount() === 0;
			safeSetHTML(toast, '' +
				'<div style="display:flex;align-items:center;justify-content:space-between;">' +
					'<div style="display:flex;align-items:center;gap:6px;">' +
						'<span style="font-size:13px;">🎬</span>' +
						'<span class="mg-toast-title" style="font-size:12px;font-weight:600;color:#ffffff;">资源解析就绪</span>' +
						epBadge +
					'</div>' +
					'<button class="mg-close-btn" id="mg-sniff-close" title="关闭">✕</button>' +
				'</div>' +
				'<div class="mg-toast-body" style="font-size:12px;line-height:1.4;color:rgba(255,255,255,0.85);font-weight:500;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;" title="' + esc(MG.title) + '">' + esc(MG.title) + '</div>' +
				'<div style="display:flex;flex-direction:column;gap:5px;">' +
					'<label class="mg-toast-muted" style="font-size:11px;color:rgba(255,255,255,0.6);">选择清晰度规格:</label>' +
					'<select class="mg-format-select" id="mg-card-fmt">' + fmtOptionsHtml() + '</select>' +
				'</div>' +
				epHtml +
				'<div style="display:flex;align-items:center;justify-content:flex-end;gap:8px;margin-top:2px;">' +
					'<button class="mg-btn-secondary" id="mg-sniff-cancel" style="height:26px;padding:0 12px;border-radius:6px;border:none;background:rgba(255,255,255,0.1);color:rgba(255,255,255,0.7);font-size:11px;cursor:pointer;">取消</button>' +
					'<button id="mg-sniff-confirm-btn" style="height:26px;padding:0 14px;border-radius:6px;border:none;font-size:11px;font-weight:600;cursor:pointer;' + (confirmDisabled ? 'background:rgba(0,113,227,0.35);color:rgba(255,255,255,0.55);' : 'background:#0071e3;color:#ffffff;') + '">' + esc(confirmLabel()) + '</button>' +
				'</div>'
			);

			const sel = toast.querySelector('#mg-card-fmt');
			if (sel) {
				if (lastFmtValue) {
					for (const opt of sel.options) {
						if (opt.value === lastFmtValue) { sel.value = lastFmtValue; break; }
					}
				}
				lastFmtValue = sel.value;
				sel.onchange = () => { lastFmtValue = sel.value; };
			}
			toast.querySelector('#mg-sniff-close').onclick = () => toast.remove();
			toast.querySelector('#mg-sniff-cancel').onclick = () => toast.remove();

			const modeCur = toast.querySelector('#mg-mode-cur');
			if (modeCur) modeCur.onclick = () => { mgMode = 'current'; renderParsedCard(); };
			const modeBatch = toast.querySelector('#mg-mode-batch');
			if (modeBatch) modeBatch.onclick = () => { mgMode = 'batch'; renderParsedCard(); };
			const toggleAll = toast.querySelector('#mg-ep-toggleall');
			if (toggleAll) toggleAll.onclick = () => {
				const makeAll = selCount() !== EPS.length;
				EPS.forEach(e => { mgSel[e.i] = makeAll; });
				renderParsedCard();
			};
			toast.querySelectorAll('.mg-ep-row').forEach(row => {
				row.onclick = () => {
					const i = parseInt(row.getAttribute('data-ep'), 10);
					mgSel[i] = !mgSel[i];
					renderParsedCard();
				};
			});
			toast.querySelector('#mg-sniff-confirm-btn').onclick = () => {
				if (confirmDisabled) return;
				let target = 'mediago-sniff://confirm-download?fmtId=' + encodeURIComponent(lastFmtValue || '');
				if (EPS.length > 1 && mgMode === 'batch') {
					const chosen = EPS.filter(e => mgSel[e.i]).map(e => e.i).join(',');
					target += '&mode=batch&eps=' + encodeURIComponent(chosen);
				}
				window.__mg_last_confirm_url = target; // 供调试与自动化校验读取最终提交参数
				window.location.href = target;
			};
		};

		// 挂载解析结果回调 (Go 端深度解析完成后下发标题、规格与分集列表)
		window.__mg_render_parsed_ui = (title, fmts, eps, curPage) => {
			if (!document.getElementById('mg-sniff-toast-card')) return;
			lastFormats = fmts || [];
			lastFmtValue = '';
			if (title) MG.title = title;
			EPS = eps || [];
			CUR = curPage || 0;
			mgSel = {};
			EPS.forEach(e => { mgSel[e.i] = true; }); // 默认全选，对齐主窗口
			mgMode = 'current';                       // 默认仅下载当前集
			renderParsedCard();
		};

		// 挂载解析失败回调 (尺寸与正在解析文案容器一致，紧凑小巧)
		window.__mg_render_extract_error = (errMsg) => {
			let toast = document.getElementById('mg-sniff-toast-card');
			if (!toast) return;
			const directURL = window.__mg_first_direct_url || '';
			let fallbackHtml = '';
			if (directURL) {
				fallbackHtml = '<button id="mg-sniff-direct-btn" style="height:22px;padding:0 8px;border-radius:5px;border:none;background:#34c759;color:#ffffff;font-size:11px;font-weight:600;cursor:pointer;">直链下载</button>';
			}
			safeSetHTML(toast, '<div style="display:flex;align-items:center;justify-content:space-between;padding:2px 0;">' +
					'<div style="display:flex;align-items:center;gap:6px;min-width:0;flex:1;">' +
						'<span style="font-size:13px;flex-shrink:0;">⚠️</span>' +
						'<span class="mg-toast-limit-text" style="font-size:12px;color:#ff453a;font-weight:500;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;" title="' + esc(errMsg) + '">页面视频解析受限</span>' +
					'</div>' +
					'<div style="display:flex;align-items:center;gap:6px;flex-shrink:0;margin-left:8px;">' +
						fallbackHtml +
						'<button class="mg-close-btn" id="mg-sniff-close" title="关闭">✕</button>' +
					'</div>' +
				'</div>');
			toast.querySelector('#mg-sniff-close').onclick = () => toast.remove();
			const directBtn = toast.querySelector('#mg-sniff-direct-btn');
			if (directBtn) {
				directBtn.onclick = () => {
					window.location.href = 'mediago-sniff://direct-download?directUrl=' + encodeURIComponent(directURL) + '&title=' + encodeURIComponent(window.__mg_clean_title || '嗅探直链视频');
				};
			}
		};

		// 挂载下载提交成功反馈
		window.__mg_render_success = () => {
			let toast = document.getElementById('mg-sniff-toast-card');
			if (!toast) return;
			safeSetHTML(toast, '<div style="font-size:12px;color:#34c759;font-weight:600;padding:6px 0;text-align:center;">✓ 已加入下载队列，在主窗口查看进度</div>');
			setTimeout(() => { if (toast) toast.remove(); }, 2200);
		};

		// 渲染初始嗅探卡片
		const toast = ensureToast();
		safeSetHTML(toast, '<div style="display:flex;align-items:center;justify-content:space-between;">' +
				'<div style="display:flex;align-items:center;gap:6px;">' +
					'<div class="mg-pulse-dot"></div>' +
					'<span class="mg-toast-title" style="font-size:12px;font-weight:600;color:#ffffff;">发现可下载媒体</span>' +
				'</div>' +
				'<button class="mg-close-btn" id="mg-sniff-close" title="关闭">✕</button>' +
			'</div>' +
			'<div class="mg-toast-body" style="font-size:12px;line-height:1.4;color:rgba(255,255,255,0.85);font-weight:500;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;">' + esc(MG.title) + '</div>' +
			'<div style="display:flex;align-items:center;justify-content:space-between;margin-top:2px;">' +
				'<span class="mg-toast-muted" style="font-size:10px;color:rgba(255,255,255,0.5);">检测到 ' + MG.mediaCount + ' 个媒体资源</span>' +
				'<div style="display:flex;align-items:center;gap:8px;">' +
					'<button class="mg-btn-secondary" id="mg-sniff-ignore" style="height:26px;padding:0 10px;border-radius:6px;border:none;background:rgba(255,255,255,0.1);color:rgba(255,255,255,0.7);font-size:11px;cursor:pointer;">忽略</button>' +
					'<button id="mg-sniff-download-btn" style="height:26px;padding:0 12px;border-radius:6px;border:none;background:#0071e3;color:#ffffff;font-size:11px;font-weight:600;cursor:pointer;">立即下载</button>' +
				'</div>' +
			'</div>');
		toast.querySelector('#mg-sniff-close').onclick = () => toast.remove();
		toast.querySelector('#mg-sniff-ignore').onclick = () => toast.remove();
		toast.querySelector('#mg-sniff-download-btn').onclick = () => {
			// 原地切换为解析中 UI (带动态旋转菊花，精简文案)
			safeSetHTML(toast, '<div style="display:flex;align-items:center;justify-content:space-between;padding:2px 0;">' +
					'<div style="display:flex;align-items:center;gap:8px;">' +
						'<div class="mg-spinner"></div>' +
						'<span style="font-size:12px;color:#0071e3;font-weight:500;">正在解析视频...</span>' +
					'</div>' +
					'<button class="mg-close-btn" id="mg-sniff-close" title="关闭">✕</button>' +
				'</div>');
			toast.querySelector('#mg-sniff-close').onclick = () => toast.remove();
			// 触发后台深度解析，优先传当前真实页面 href
			const targetURL = window.location.href || MG.url || '';
			window.location.href = 'mediago-sniff://extract?url=' + encodeURIComponent(targetURL);
		};
	} catch (e) {}
})()`
		_, _ = page.Eval(toastScript)
	}
}
