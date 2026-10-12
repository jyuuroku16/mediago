package ui

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/google/uuid"

	"mediago/internal/browser"
	"mediago/internal/config"
	"mediago/internal/discovery"
	"mediago/internal/engine"
	"mediago/internal/library"
	"mediago/internal/model"
	"mediago/internal/storage"
)

const titleBarHeight = 52

// AppUI 现代化桌面主界面控制器 (对标 godiff / gorex 与 Apple HIG)
type AppUI struct {
	window     *mygo.Window
	storage    *storage.Storage
	scheduler  *engine.Scheduler
	browserMgr *browser.Manager
	library    *library.Library
	extractor  *discovery.YtDlpExtractor

	mu sync.RWMutex

	// 侧栏当前导航 ID: "downloading", "completed", "paused", "failed", "all", "library"
	currentTab string

	// 过滤与搜索
	searchFilter string

	// 任务与媒体数据
	tasks  []*model.Task
	assets []*model.Asset

	// 新建下载弹窗状态
	showModal      bool
	modalURL       string
	isExtracting   bool
	extractError   string
	candidate      *model.Candidate
	selectedFormat int

	// 分集下载控制 (B站多P等)
	downloadAllEpisodes bool
	showEpisodesModal   bool
	selectedEpisodesMap map[int]bool

	// 浏览器 Raycast 风格 Launcher 启动器状态
	showBrowserLauncher bool
	launcherInput       string

	// 独立偏好设置原生窗口管理器
	settingsWin *SettingsWindow

	// 侧边栏显隐状态 (参考 godiff)
	sidebarShown bool

	// 键盘快捷键速查面板状态
	showShortcuts bool

	// 工具安装提示横幅已关闭标记
	dismissToolBanner bool

	// 删除/移除二次确认弹窗状态
	showDeleteConfirm bool
	deleteAssetTarget *model.Asset
	deleteTaskTarget  *model.Task
	deleteFileToo     bool
}

// RefreshUI 触发主窗口重绘
func (app *AppUI) RefreshUI() {
	if app.window != nil {
		app.window.Invalidate()
	}
}

// OpenSettings 打开独立的 macOS 原生偏好设置窗口
func (app *AppUI) OpenSettings(initialTab ...string) {
	if app.settingsWin != nil {
		app.settingsWin.Open(initialTab...)
	}
}

// OpenBrowserLauncher 打开 Raycast 风格网址输入启动器
func (app *AppUI) OpenBrowserLauncher() {
	app.showBrowserLauncher = true
	app.launcherInput = ""
	app.RefreshUI()
}

// OpenShortcutsHelp 打开快捷键速查面板
func (app *AppUI) OpenShortcutsHelp() {
	app.showShortcuts = true
	app.RefreshUI()
}

// OpenNewDownload 打开新建下载弹窗
func (app *AppUI) OpenNewDownload() {
	app.showModal = true
	app.modalURL = ""
	app.candidate = nil
	app.extractError = ""
	app.downloadAllEpisodes = false
	app.RefreshUI()
}

// ToggleSidebar 切换左侧侧边栏折叠/展开
func (app *AppUI) ToggleSidebar() {
	app.sidebarShown = !app.sidebarShown
	app.RefreshUI()
}

// iconButton 统一标准带悬浮与按下动效的纯图标按钮 (参考 godiff)
func (app *AppUI) iconButton(c *ui.Context, svg *ui.SVG, tip string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Size(28, 28).Radius(7).Center().Label(tip).Tooltip(tip).TextColor(t.TextMuted)
	if b.Pressed() {
		b.Background(ui.RGBA(127, 127, 127, 0.22))
	} else if b.Hovered() {
		b.Background(ui.RGBA(127, 127, 127, 0.13))
	}
	b.Children(func() { ui.Icon(c, svg).FontSize(15) })
	return b
}

// NewAppUI 创建主界面控制器
func NewAppUI(
	st *storage.Storage,
	sc *engine.Scheduler,
	bm *browser.Manager,
	lib *library.Library,
) *AppUI {
	app := &AppUI{
		storage:             st,
		scheduler:           sc,
		browserMgr:          bm,
		library:             lib,
		extractor:           discovery.NewYtDlpExtractor(),
		currentTab:          "all",
		showModal:           false,
		selectedFormat:      0,
		downloadAllEpisodes: false,
		selectedEpisodesMap: make(map[int]bool),
		sidebarShown:        true,
		settingsWin:         NewSettingsWindow(),
	}

	app.refreshData()

	// 调度器状态变动时触发 UI 重新绘制
	sc.OnUpdate(func(t *model.Task) {
		app.refreshData()
		if app.window != nil {
			app.window.Invalidate()
		}
	})

	// 监听核心流媒体工具安装进度，触发主视窗重绘
	discovery.DefaultInstaller().OnProgress(func(_ discovery.InstallProgress) {
		app.RefreshUI()
	})

	return app
}

// SetWindow 关联原生窗口实例
func (app *AppUI) SetWindow(w *mygo.Window) {
	app.window = w
}

func (app *AppUI) refreshData() {
	app.mu.Lock()
	defer app.mu.Unlock()

	if ts, err := app.storage.ListTasks(""); err == nil {
		app.tasks = ts
	}
	if as, err := app.storage.ListAssets(); err == nil {
		app.assets = as
	}
}

func (app *AppUI) filteredTasks() []*model.Task {
	app.mu.RLock()
	defer app.mu.RUnlock()

	var result []*model.Task
	q := strings.ToLower(strings.TrimSpace(app.searchFilter))

	for _, t := range app.tasks {
		if q != "" && !strings.Contains(strings.ToLower(t.Title), q) && !strings.Contains(strings.ToLower(t.SourceURL), q) {
			continue
		}

		switch app.currentTab {
		case "downloading":
			if t.Status == model.StatusDownloading || t.Status == model.StatusQueued {
				result = append(result, t)
			}
		case "completed":
			if t.Status == model.StatusCompleted {
				result = append(result, t)
			}
		case "paused":
			if t.Status == model.StatusPaused {
				result = append(result, t)
			}
		case "failed":
			if t.Status == model.StatusFailed {
				result = append(result, t)
			}
		case "all":
			result = append(result, t)
		}
	}
	return result
}

// filteredAssets 按搜索关键词过滤媒体资料库条目
func (app *AppUI) filteredAssets() []*model.Asset {
	app.mu.RLock()
	defer app.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(app.searchFilter))
	if q == "" {
		return app.assets
	}
	var result []*model.Asset
	for _, a := range app.assets {
		if strings.Contains(strings.ToLower(a.Title), q) || strings.Contains(strings.ToLower(a.FilePath), q) {
			result = append(result, a)
		}
	}
	return result
}

// View 主渲染循环
func (app *AppUI) View(c *ui.Context) {
	theme := c.Theme()
	pal := getPalette(theme)

	// 在 macOS 下根背景必须为 Transparent，以使窗口底层的 NSVisualEffectView 磨砂毛玻璃透出
	c.Root().Background(pal.SidebarBg)

	// 纵向彻底拉伸填满窗口，避免任何未渲染的纯黑底层暴露
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		// 1. 左侧 Sidebar (Apple 原生磨砂侧边栏)
		if app.sidebarShown {
			app.renderSidebar(c, pal)

			// 2. 细腻垂直分割线
			ui.Box(c).Width(1).Shrink(0).Background(pal.HeaderBorder)
		}

		// 3. 右侧主体工作区 (Toolbar + 主内容流)
		ui.Column(c).Grow(1).MinWidth(0).Background(pal.ContentBg).Children(func() {
			app.renderTopBar(c, pal)
			app.renderToolInstallBanner(c, pal)
			app.renderMainContent(c, pal)
		})
	})

	// 全局快捷键处理: ⌘N 新建下载, ⌘B 网址启动器, ⌘, 偏好设置, ⌘\ 切换侧边栏, ⌘/ 快捷键速查
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		app.OpenNewDownload()
	}
	if c.Shortcut(ui.Cmd, ui.KeyB) {
		app.OpenBrowserLauncher()
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		app.OpenSettings()
	}
	if c.Shortcut(ui.Cmd, ui.KeyBackslash) {
		app.sidebarShown = !app.sidebarShown
		app.RefreshUI()
	}
	if c.Shortcut(ui.Cmd, ui.KeySlash) {
		app.showShortcuts = !app.showShortcuts
	}

	// 模态弹窗渲染 (基于 mygo ui.Modal 原生规范)
	if app.showModal {
		app.renderModalDialog(c, pal)
	}
	if app.showEpisodesModal {
		app.renderEpisodesModal(c, pal)
	}
	if app.showBrowserLauncher {
		app.renderBrowserLauncherModal(c, pal)
	}
	if app.showShortcuts {
		app.renderShortcutsModal(c, pal)
	}
	if app.showDeleteConfirm {
		app.renderDeleteConfirmModal(c, pal)
	}
}

// renderSidebar 渲染左侧原生侧栏
func (app *AppUI) renderSidebar(c *ui.Context, pal Palette) {
	bar := c.TitleBar()
	// 左侧避让 macOS 红黄绿交通灯
	left := max(bar.Left+10, 14)

	ui.Column(c).Width(240).Shrink(0).Background(pal.SidebarBg).Children(func() {
		// 顶部标题栏高度 52pt，避让系统交通灯，右侧放置折叠侧边栏纯图标按钮 (参考 godiff)
		ui.Row(c).Height(titleBarHeight).Padding(0, 12, 0, left).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Spacer(c)
			if app.iconButton(c, iconSidebar, "隐藏侧边栏 (⌘\\)").Clicked() {
				app.sidebarShown = false
				app.RefreshUI()
			}
		})

		// 搜索过滤胶囊框
		ui.Column(c).Padding(2, 12, 10, 12).Children(func() {
			ui.Row(c).Height(28).Padding(0, 8).Gap(6).Radius(7).Background(pal.InputBg).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconSearch).FontSize(12).TextColor(pal.TextMuted)
				ui.TextInputBase(c, &app.searchFilter).Placeholder("搜索任务与视频...").FontSize(12).Grow(1).MinWidth(0)
				if app.searchFilter != "" {
					clearBtn := ui.ButtonBase(c).Size(14, 14).Radius(7).Center().Background(pal.TextMuted).FocusRing(false)
					clearBtn.Children(func() {
						ui.Icon(c, iconClose).FontSize(9).TextColor(pal.CardBg)
					})
					if clearBtn.Clicked() {
						app.searchFilter = ""
					}
				}
			})
		})

		// 导航菜单列表
		ui.Column(c).Padding(0, 10).Gap(2).Grow(1).Children(func() {
			app.renderSectionHeader(c, "任务队列", pal)

			activeCount := 0
			app.mu.RLock()
			for _, t := range app.tasks {
				if t.Status == model.StatusDownloading {
					activeCount++
				}
			}
			app.mu.RUnlock()

			app.renderSidebarItem(c, "all", "全部任务", iconList, 0, pal)
			app.renderSidebarItem(c, "downloading", "下载中", iconDownload, activeCount, pal)
			app.renderSidebarItem(c, "completed", "已完成", iconCheckCircle, 0, pal)
			app.renderSidebarItem(c, "paused", "已暂停", iconPause, 0, pal)
			app.renderSidebarItem(c, "failed", "下载失败", iconAlertCircle, 0, pal)

			ui.Box(c).Height(12)

			app.renderSectionHeader(c, "媒体资源", pal)
			app.renderSidebarItem(c, "library", "媒体资料库", iconFilm, 0, pal)
		})

		// 底部纯图标工具条: 左侧设置纯图标，右侧为浏览器(🌐)与新建(+)并排纯图标
		ui.Row(c).Height(38).Padding(0, 14).AlignItems(ui.Center).Children(func() {
			if app.iconButton(c, iconSettings, "偏好设置 (⌘,)").Clicked() {
				app.OpenSettings()
			}

			ui.Spacer(c)

			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if app.iconButton(c, iconGlobe, "专用浏览器启动器 (⌘B)").Clicked() {
					app.OpenBrowserLauncher()
				}
				if app.iconButton(c, iconPlus, "新建下载 (⌘N)").Clicked() {
					app.OpenNewDownload()
				}
			})
		})
	})
}

func (app *AppUI) renderSectionHeader(c *ui.Context, title string, pal Palette) {
	ui.Box(c).Padding(6, 8, 2, 8).Children(func() {
		ui.Text(c, strings.ToUpper(title)).FontSize(11).Bold().TextColor(pal.TextMuted)
	})
}

func (app *AppUI) renderSidebarItem(c *ui.Context, id, title string, icon *ui.SVG, badge int, pal Palette) {
	isSelected := app.currentTab == id
	bg := ui.Transparent
	textC := pal.TextPrimary
	if isSelected {
		bg = pal.AccentLight
		textC = pal.Accent
	}

	btn := ui.Row(c).Height(30).Padding(0, 10).Gap(8).Radius(7).Background(bg).AlignItems(ui.Center)
	btn.Children(func() {
		ui.Icon(c, icon).FontSize(14).TextColor(textC)
		ui.Text(c, title).FontSize(13).TextColor(textC)
		ui.Spacer(c)
		if badge > 0 {
			ui.Box(c).Padding(1, 6).Radius(8).Background(pal.Accent).Children(func() {
				ui.Text(c, fmt.Sprint(badge)).FontSize(10).Bold().TextColor(ui.Hex("#ffffff"))
			})
		}
	})

	if btn.Clicked() {
		app.currentTab = id
		if app.window != nil {
			app.window.Invalidate()
		}
	}
}

// renderTopBar 渲染右侧顶部无缝融合的 Toolbar (隐藏背景与底边框，参考 Pulse 轻盈设计)
func (app *AppUI) renderTopBar(c *ui.Context, pal Palette) {
	var totalSpeed int64
	app.mu.RLock()
	for _, t := range app.tasks {
		if t.Status == model.StatusDownloading {
			totalSpeed += t.Speed
		}
	}
	app.mu.RUnlock()

	leftPadding := float32(20)
	if !app.sidebarShown {
		bar := c.TitleBar()
		leftPadding = max(bar.Left+10, 14)
	}

	ui.Row(c).Height(titleBarHeight).Padding(0, 20, 0, leftPadding).Background(ui.Transparent).Material(glass.ScrollEdge{}).AlignItems(ui.Center).DragWindow().Children(func() {
		if !app.sidebarShown {
			if app.iconButton(c, iconSidebar, "显示侧边栏 (⌘\\)").Clicked() {
				app.sidebarShown = true
				app.RefreshUI()
			}
			ui.Box(c).Width(10)
		}

		viewTitle := "任务列表"
		switch app.currentTab {
		case "downloading":
			viewTitle = "下载中任务"
		case "completed":
			viewTitle = "已完成任务"
		case "paused":
			viewTitle = "已暂停任务"
		case "failed":
			viewTitle = "下载失败任务"
		case "all":
			viewTitle = "全部下载任务"
		case "library":
			viewTitle = "媒体资料库"
		}
		ui.Text(c, viewTitle).FontSize(15).Bold().TextColor(pal.TextPrimary)

		ui.Spacer(c)

		// 实时网络速率指示胶囊
		if totalSpeed > 0 {
			ui.Row(c).Height(26).Padding(0, 8).Gap(4).Radius(6).Background(pal.SuccessLight).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconLightning).FontSize(11).TextColor(pal.Success)
				ui.Text(c, formatSpeed(totalSpeed)).FontSize(11).Bold().TextColor(pal.Success)
			})
		}
	})
}

// renderToolInstallBanner 渲染顶部轻量流媒体工具缺失或安装进度横幅
func (app *AppUI) renderToolInstallBanner(c *ui.Context, pal Palette) {
	installer := discovery.DefaultInstaller()
	state := installer.State()
	isBusy := state.Status == discovery.StatusDownloading || state.Status == discovery.StatusExtracting

	// 1. 若处于后台安装与配置状态，渲染高质感进度横条 (固定高度 36px)
	if isBusy {
		ui.Row(c).Height(36).Padding(0, 20).Gap(10).Background(ui.RGBA(0, 122, 255, 0.08)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconDownload).FontSize(14).TextColor(pal.Accent)
			ui.Text(c, state.Message).FontSize(12).Bold().TextColor(pal.Accent).Grow(1)
			ui.Text(c, fmt.Sprintf("%.0f%%", state.Percent*100)).FontSize(11).Bold().TextColor(pal.Accent)
		})
		ui.Box(c).Height(1).Background(pal.HeaderBorder)
		return
	}

	// 2. 若用户手动关闭了本次提醒，则静默
	if app.dismissToolBanner {
		return
	}

	// 3. 检测组件缺失情况
	ytVer, ffVer := discovery.GetToolVersions()
	if ytVer != "未安装" && ffVer != "未安装" {
		return
	}

	missing := []string{}
	if ffVer == "未安装" {
		missing = append(missing, "FFmpeg")
	}
	if ytVer == "未安装" {
		missing = append(missing, "yt-dlp")
	}
	missingStr := strings.Join(missing, " 和 ")

	// 提示横幅 (浅暖色底衬，高度 36px，严格居中单行通知条)
	ui.Row(c).Height(36).Padding(0, 20).Gap(12).Background(ui.RGBA(255, 159, 10, 0.10)).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, iconAlertCircle).FontSize(14).TextColor(ui.Hex("#ff9f0a"))
		ui.Text(c, fmt.Sprintf("检测到未配置 %s 核心组件，视频解析与合并可能受限", missingStr)).
			FontSize(12).TextColor(pal.TextPrimary).Grow(1)

		// 一键自动配置按钮
		cfgBtn := ui.Row(c).Height(22).Padding(0, 10).Radius(5).Background(pal.Accent).AlignItems(ui.Center).Cursor(ui.CursorPointer)
		cfgBtn.Children(func() {
			ui.Text(c, "一键自动配置").FontSize(11).Bold().TextColor(ui.Hex("#ffffff"))
		})
		if cfgBtn.Clicked() {
			go func() {
				_ = installer.InstallAll(context.Background())
			}()
		}

		// 偏好设置入口
		setBtn := ui.Row(c).Height(22).Padding(0, 8).Radius(5).Background(pal.CardBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
		setBtn.Children(func() {
			ui.Text(c, "偏好设置").FontSize(11).TextColor(pal.TextSecondary)
		})
		if setBtn.Clicked() {
			app.OpenSettings("about")
		}

		// 关闭横幅按钮
		closeBtn := ui.Row(c).Height(20).Width(20).Radius(10).Center().Cursor(ui.CursorPointer)
		closeBtn.Children(func() {
			ui.Text(c, "✕").FontSize(11).TextColor(pal.TextMuted)
		})
		if closeBtn.Clicked() {
			app.dismissToolBanner = true
			app.RefreshUI()
		}
	})
	ui.Box(c).Height(1).Background(pal.HeaderBorder)
}

// renderMainContent 渲染主工作区卡片流
func (app *AppUI) renderMainContent(c *ui.Context, pal Palette) {
	if app.currentTab == "library" {
		app.renderLibraryCards(c, pal)
		return
	}

	tasks := app.filteredTasks()

	// 空状态展示 (Empty State)
	if len(tasks) == 0 {
		ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
			ui.Icon(c, iconFilm).FontSize(48).TextColor(pal.TextMuted)
			ui.Text(c, "暂无相关下载任务").FontSize(15).Bold().TextColor(pal.TextPrimary)
			ui.Text(c, "点击左下角「+」新建下载或通过专用浏览器嗅探网页视频").FontSize(13).TextColor(pal.TextSecondary)

			createBtn := ui.Row(c).Height(32).Padding(0, 16).Gap(6).Radius(7).Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
			createBtn.Children(func() {
				ui.Icon(c, iconPlus).FontSize(12).TextColor(pal.TextPrimary)
				ui.Text(c, "立即粘贴链接下载").FontSize(12).TextColor(pal.TextPrimary)
			})
			if createBtn.Clicked() {
				app.showModal = true
				app.modalURL = ""
				app.candidate = nil
			}
		})
		return
	}

	// 任务卡片列表 (可滚动)
	ui.Scroll(c).Vertical().Fill().Children(func() {
		ui.Column(c).Padding(16, 20).Gap(10).Children(func() {
			for _, t := range tasks {
				app.renderTaskCard(c, t, pal)
			}
		})
	})
}

// renderTaskCard 渲染单个任务卡片
func (app *AppUI) renderTaskCard(c *ui.Context, t *model.Task, pal Palette) {
	card := ui.Column(c).Padding(14, 16).Gap(10).Radius(10).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder)
	isFileMissing := false
	if t.Status == model.StatusCompleted {
		if t.FinalPath == "" {
			isFileMissing = true
		} else if _, err := os.Stat(t.FinalPath); err != nil {
			isFileMissing = true
		}
	}
	if isFileMissing {
		card.Opacity(0.55)
	}

	card.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.03))

	card.Children(func() {
		// 1. 顶部行: 标签徽章 + 标题 + 状态胶囊
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			engLabel := "MP4"
			engBg := pal.AccentLight
			engTc := pal.Accent
			if t.Engine == model.EngineExtractor {
				engLabel = "HD-MUX"
				engBg = pal.SuccessLight
				engTc = pal.Success
			} else if t.Engine == model.EngineHLS {
				engLabel = "HLS"
				engBg = ui.RGBA(175, 82, 222, 0.15)
				engTc = ui.Hex("#af52de")
			}
			ui.Box(c).Padding(2, 6).Radius(4).Background(engBg).Children(func() {
				ui.Text(c, engLabel).FontSize(10).Bold().TextColor(engTc)
			})

			// 视频标题 (加粗单行截断)
			ui.Text(c, t.Title).FontSize(13).Bold().TextColor(pal.TextPrimary).SingleLine().Grow(1).MinWidth(0)

			// 状态胶囊
			statusText := "等待中"
			statusBg := pal.InputBg
			statusTc := pal.TextSecondary
			switch t.Status {
			case model.StatusDownloading:
				statusText = "下载中"
				statusBg = pal.AccentLight
				statusTc = pal.Accent
			case model.StatusCompleted:
				if isFileMissing {
					statusText = "已失效"
					statusBg = pal.InputBg
					statusTc = pal.TextMuted
				} else {
					statusText = "已完成"
					statusBg = pal.SuccessLight
					statusTc = pal.Success
				}
			case model.StatusPaused:
				statusText = "已暂停"
				statusBg = pal.WarningLight
				statusTc = pal.Warning
			case model.StatusFailed:
				statusText = "失败"
				statusBg = pal.DangerLight
				statusTc = pal.Danger
			}
			ui.Box(c).Padding(2, 8).Radius(6).Background(statusBg).Children(func() {
				ui.Text(c, statusText).FontSize(11).Bold().TextColor(statusTc)
			})
		})

		// 2. 进度条行 (下载中或有进度时)
		if t.Status != model.StatusCompleted && t.TotalBytes > 0 {
			ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Row(c).Height(6).Radius(3).Background(pal.ProgressTrack).Grow(1).Children(func() {
					prog := float32(t.Progress)
					if prog < 0.01 && prog > 0 {
						prog = 0.01
					}
					if prog > 1.0 {
						prog = 1.0
					}
					rem := 1.0 - prog
					if prog > 0 {
						ui.Box(c).Height(6).Radius(3).Grow(prog).Background(pal.Accent)
					}
					if rem > 0 {
						ui.Spacer(c).Grow(rem)
					}
				})
				pctStr := fmt.Sprintf("%.1f%%", t.Progress*100)
				ui.Text(c, pctStr).FontSize(11).TextColor(pal.TextSecondary)
			})
		}

		// 3. 底部指标与操作控制行
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			sizeInfo := fmt.Sprintf("%s / %s", formatBytes(t.DownloadedBytes), formatBytes(t.TotalBytes))
			if isFileMissing {
				sizeInfo += " · 磁盘文件已被删除"
			}
			ui.Text(c, sizeInfo).FontSize(12).TextColor(pal.TextSecondary)

			if t.Status == model.StatusDownloading && t.Speed > 0 {
				ui.Box(c).Width(12)
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconLightning).FontSize(11).TextColor(pal.Success)
					ui.Text(c, formatSpeed(t.Speed)).FontSize(12).Bold().TextColor(pal.Success)
				})
			}

			if t.Status == model.StatusFailed && t.ErrorMsg != "" {
				ui.Box(c).Width(12)
				ui.Text(c, "错误: "+t.ErrorMsg).FontSize(11).TextColor(pal.Danger).SingleLine().Grow(1).MinWidth(0)
			}

			ui.Spacer(c)

			// 右侧操作按钮
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if t.Status == model.StatusDownloading {
					pauseBtn := ui.Button(c, "暂停").FontSize(11)
					if pauseBtn.Clicked() {
						_ = app.scheduler.Pause(t.ID)
					}
				} else if t.Status == model.StatusPaused || t.Status == model.StatusFailed {
					resumeBtn := ui.Button(c, "继续").FontSize(11)
					if resumeBtn.Clicked() {
						_ = app.scheduler.Resume(t.ID)
					}
				}

				if t.Status == model.StatusCompleted && t.FinalPath != "" && !isFileMissing {
					finderBtn := ui.Row(c).Height(26).Padding(0, 8).Gap(4).Radius(6).Background(pal.InputBg).AlignItems(ui.Center)
					finderBtn.Children(func() {
						ui.Icon(c, iconFolder).FontSize(12).TextColor(pal.TextPrimary)
						ui.Text(c, "Finder").FontSize(11).TextColor(pal.TextPrimary)
					})
					if finderBtn.Clicked() {
						_ = app.library.OpenInFinder(t.FinalPath)
					}

					prevBtn := ui.Row(c).Height(26).Padding(0, 8).Gap(4).Radius(6).Background(pal.InputBg).AlignItems(ui.Center)
					prevBtn.Children(func() {
						ui.Icon(c, iconPlay).FontSize(12).TextColor(pal.TextPrimary)
						ui.Text(c, "播放").FontSize(11).TextColor(pal.TextPrimary)
					})
					if prevBtn.Clicked() {
						_ = app.library.QuickLook(t.FinalPath)
					}
				}

				delBtn := ui.Row(c).Height(26).Padding(0, 8).Radius(6).Background(pal.InputBg).AlignItems(ui.Center)
				delBtn.Children(func() {
					ui.Icon(c, iconTrash).FontSize(12).TextColor(pal.Danger)
				})
				if delBtn.Clicked() {
					app.deleteTaskTarget = t
					app.deleteAssetTarget = nil
					app.showDeleteConfirm = true
					app.RefreshUI()
				}
			})
		})
	})
}

// renderLibraryCards 渲染本地媒体库卡片
func (app *AppUI) renderLibraryCards(c *ui.Context, pal Palette) {
	assets := app.filteredAssets()

	if len(assets) == 0 {
		ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
			ui.Icon(c, iconFilm).FontSize(48).TextColor(pal.TextMuted)
			ui.Text(c, "媒体资料库为空").FontSize(15).Bold().TextColor(pal.TextPrimary)
			ui.Text(c, "所有下载完成的视频将自动整理并保存在此处").FontSize(13).TextColor(pal.TextSecondary)
		})
		return
	}

	ui.Scroll(c).Vertical().Fill().Children(func() {
		ui.Column(c).Padding(16, 20).Gap(10).Children(func() {
			for _, a := range assets {
				fileMissing := false
				if _, err := os.Stat(a.FilePath); err != nil {
					fileMissing = true
				}

				card := ui.Row(c).Padding(12, 16).Gap(12).Radius(10).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
				if fileMissing {
					card.Opacity(0.55)
				}
				card.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.03))

				card.Children(func() {
					iconC := pal.Accent
					if fileMissing {
						iconC = pal.TextMuted
					}

					// 媒体资源库 item 左侧图标改为 16:9 视频首帧预览 (宽 80, 高 45)
					var thumbBm *ui.Bitmap
					if !fileMissing {
						thumbBm = app.library.GetThumbnail(a.FilePath, func() {
							c.Invalidate()
						})
					}

					if thumbBm != nil {
						ui.Image(c, thumbBm).Size(80, 45).Radius(6).Fit(ui.Cover)
					} else {
						placeholder := ui.Row(c).Size(80, 45).Radius(6).
							Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder).Center()
						placeholder.Children(func() {
							ui.Icon(c, iconFilm).FontSize(18).TextColor(iconC)
						})
					}

					ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
						titleRow := ui.Row(c).Gap(6).AlignItems(ui.Center)
						titleRow.Children(func() {
							ui.Text(c, a.Title).FontSize(13).Bold().TextColor(pal.TextPrimary).SingleLine()
							if fileMissing {
								ui.Box(c).Padding(1, 6).Radius(4).Background(pal.InputBg).Children(func() {
									ui.Text(c, "已失效").FontSize(10).TextColor(pal.TextMuted)
								})
							}
						})
						meta := fmt.Sprintf("%s · %s", formatBytes(a.FileSize), a.FilePath)
						if fileMissing {
							meta += " (文件已从磁盘删除)"
						}
						ui.Text(c, meta).FontSize(11).TextColor(pal.TextSecondary).SingleLine()
					})

					// 媒体资料库纯图标按钮栏 (需求 8)
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						if !fileMissing {
							if app.iconButton(c, iconPlay, "播放视频").Clicked() {
								_ = app.library.QuickLook(a.FilePath)
							}
							if app.iconButton(c, iconFolder, "在 Finder 中显示").Clicked() {
								_ = app.library.OpenInFinder(a.FilePath)
							}
						}
						if app.iconButton(c, iconTrash, "移除记录").Clicked() {
							app.deleteAssetTarget = a
							app.deleteTaskTarget = nil
							app.deleteFileToo = false
							app.showDeleteConfirm = true
							app.RefreshUI()
						}
					})
				})
			}
		})
	})
}

// renderModalDialog 渲染极简悬浮模态框 (基于 mygo ui.Modal 单层原生容器，无多余双层嵌套底座)
func (app *AppUI) renderModalDialog(c *ui.Context, pal Palette) {
	ui.Modal(c, &app.showModal, func() {
		ui.Column(c).Width(520).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "新建媒体下载").FontSize(15).Bold().TextColor(pal.TextPrimary)
				ui.Spacer(c)
				closeBtn := ui.ButtonBase(c).Size(20, 20).Radius(10).Center().Background(pal.InputBg)
				closeBtn.Children(func() {
					ui.Icon(c, iconClose).FontSize(11).TextColor(pal.TextSecondary)
				})
				if closeBtn.Clicked() {
					app.showModal = false
				}
			})

			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				// 输入框容器 (充满剩余宽度)
				ui.Row(c).Height(38).Padding(0, 12).Gap(8).Radius(8).
					Background(pal.InputBg).BorderWidth(1).BorderColor(pal.InputBorder).
					AlignItems(ui.Center).Grow(1).Children(func() {
					ui.Icon(c, iconGlobe).FontSize(14).TextColor(pal.TextMuted)
					in := ui.TextInputBase(c, &app.modalURL).AutoFocus().Placeholder("粘贴视频网址或直链地址 (MP4, HLS, B站, YouTube 等)").FontSize(13).Grow(1).MinWidth(0)
					if in.Submitted() || in.Shortcut(0, ui.KeyEnter) {
						if app.candidate != nil && len(app.candidate.Formats) > 0 {
							app.submitCandidate()
							app.showModal = false
						} else if strings.TrimSpace(app.modalURL) != "" {
							app.startExtract()
						}
					}
				})

				// 移出到输入框右侧的主按钮 (无内容或解析中置灰，有内容高亮 Accent 蓝)
				hasURL := strings.TrimSpace(app.modalURL) != ""
				canParse := hasURL && !app.isExtracting

				parseBtn := ui.Row(c).Height(38).Padding(0, 18).Radius(8).AlignItems(ui.Center)
				if canParse {
					parseBtn.Background(pal.Accent).Cursor(ui.CursorPointer)
					parseBtn.Children(func() {
						ui.Text(c, "解析").FontSize(13).Bold().TextColor(ui.Hex("#ffffff"))
					})
					if parseBtn.Clicked() {
						app.startExtract()
					}
				} else {
					parseBtn.Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder).Cursor(ui.CursorDefault)
					parseBtn.Children(func() {
						ui.Text(c, "解析").FontSize(13).TextColor(pal.TextMuted)
					})
				}
			})

			if app.isExtracting {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).FontSize(14).TextColor(pal.Accent)
					ui.Text(c, "正在解析视频...").FontSize(12).TextColor(pal.Accent)
				})
			}

			if app.extractError != "" {
				ui.Text(c, "解析失败: "+app.extractError).FontSize(12).TextColor(pal.Danger)
			}

			if app.candidate != nil {
				ui.Column(c).Padding(12).Radius(8).Background(pal.InputBg).Gap(10).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconFilm).FontSize(14).TextColor(pal.Accent)
						ui.Text(c, app.candidate.Title).FontSize(13).Bold().TextColor(pal.TextPrimary).SingleLine().Grow(1).MinWidth(0)
					})

					// 多集选集区块 (B站等多分集视频)
					if len(app.candidate.Episodes) > 1 {
						ui.Column(c).Padding(10, 12).Radius(8).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).Gap(8).Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
								ui.Icon(c, iconList).FontSize(13).TextColor(pal.Accent)
								ui.Text(c, fmt.Sprintf("检测到多集视频 (共 %d 集)", len(app.candidate.Episodes))).FontSize(12).Bold().TextColor(pal.TextPrimary).SingleLine()
								if app.candidate.CurrentPage > 0 && app.candidate.CurrentPage <= len(app.candidate.Episodes) {
									curEp := app.candidate.Episodes[app.candidate.CurrentPage-1]
									ui.Spacer(c)
									epTitle := curEp.Title
									if len([]rune(epTitle)) > 12 {
										epTitle = string([]rune(epTitle)[:10]) + "..."
									}
									ui.Text(c, fmt.Sprintf("当前位于: P%d %s", curEp.Index, epTitle)).FontSize(11).TextColor(pal.Accent).SingleLine()
								}
							})

							curPTitle := fmt.Sprintf("P%d", app.candidate.CurrentPage)
							if app.candidate.CurrentPage > 0 && app.candidate.CurrentPage <= len(app.candidate.Episodes) {
								epObj := app.candidate.Episodes[app.candidate.CurrentPage-1]
								epTitle := epObj.Title
								if len([]rune(epTitle)) > 10 {
									epTitle = string([]rune(epTitle)[:8]) + "..."
								}
								curPTitle = fmt.Sprintf("P%02d %s", epObj.Index, epTitle)
							}
							curLabel := "仅下载: " + curPTitle

							selectedCount := 0
							for _, ep := range app.candidate.Episodes {
								if app.selectedEpisodesMap[ep.Index] {
									selectedCount++
								}
							}
							batchLabel := fmt.Sprintf("批量下载 (已选 %d 集)", selectedCount)
							if selectedCount == len(app.candidate.Episodes) {
								batchLabel = fmt.Sprintf("批量下载全部 %d 集", len(app.candidate.Episodes))
							}

							ui.Row(c).Gap(8).Children(func() {
								btnCur := ui.Row(c).Height(28).Padding(0, 12).Radius(6).AlignItems(ui.Center)
								if !app.downloadAllEpisodes {
									btnCur.Background(pal.AccentLight).BorderWidth(1).BorderColor(pal.Accent)
									btnCur.Children(func() {
										ui.Text(c, curLabel).FontSize(11).Bold().TextColor(pal.Accent).SingleLine()
									})
								} else {
									btnCur.Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder)
									btnCur.Children(func() {
										ui.Text(c, curLabel).FontSize(11).TextColor(pal.TextSecondary).SingleLine()
									})
								}
								if btnCur.Clicked() {
									app.downloadAllEpisodes = false
								}

								btnAll := ui.Row(c).Height(28).Padding(0, 12).Radius(6).AlignItems(ui.Center).Gap(5)
								if app.downloadAllEpisodes {
									btnAll.Background(pal.AccentLight).BorderWidth(1).BorderColor(pal.Accent)
									btnAll.Children(func() {
										ui.Text(c, batchLabel).FontSize(11).Bold().TextColor(pal.Accent).SingleLine()
										ui.Icon(c, iconChevronDown).FontSize(11).TextColor(pal.Accent)
									})
								} else {
									btnAll.Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder)
									btnAll.Children(func() {
										ui.Text(c, batchLabel).FontSize(11).TextColor(pal.TextSecondary).SingleLine()
										ui.Icon(c, iconChevronDown).FontSize(11).TextColor(pal.TextMuted)
									})
								}
								if btnAll.Clicked() {
									app.showEpisodesModal = true
								}
							})
						})
					}

					if len(app.candidate.Formats) > 0 {
						ui.Text(c, "选择清晰度与保存格式:").FontSize(11).Bold().TextColor(pal.TextSecondary)
						ui.Column(c).Gap(6).Children(func() {
							for i := 0; i < len(app.candidate.Formats); i += 3 {
								end := i + 3
								if end > len(app.candidate.Formats) {
									end = len(app.candidate.Formats)
								}
								rowSlice := app.candidate.Formats[i:end]
								startIdx := i
								ui.Row(c).Gap(8).Children(func() {
									for j, f := range rowSlice {
                                        idx := startIdx + j
										isSel := app.selectedFormat == idx
										fBg := pal.CardBg
										fBorder := pal.CardBorder
										fTc := pal.TextPrimary
										if isSel {
											fBg = pal.AccentLight
											fBorder = pal.Accent
											fTc = pal.Accent
										}
										fmtBtn := ui.Row(c).Height(28).Padding(0, 12).Radius(7).Background(fBg).BorderWidth(1).BorderColor(fBorder).AlignItems(ui.Center)
										fmtLabel := fmt.Sprintf("%s (%s)", f.Resolution, strings.ToUpper(f.Ext))
										fmtBtn.Children(func() {
											ui.Text(c, fmtLabel).FontSize(11).Bold().TextColor(fTc)
										})
										if fmtBtn.Clicked() {
											app.selectedFormat = idx
										}
									}
								})
							}
						})
					}
				})
			}

			// 底部操作区 (彻底移除多余的取消按钮，右对齐立即开始下载)
			if app.candidate != nil {
				btnText := "立即开始下载"
				if len(app.candidate.Episodes) > 1 {
					if app.downloadAllEpisodes {
						btnText = fmt.Sprintf("立即批量下载全部 %d 集", len(app.candidate.Episodes))
					} else {
						btnText = fmt.Sprintf("立即下载当前集 (P%d)", app.candidate.CurrentPage)
					}
				}
				ui.Row(c).Justify(ui.End).Children(func() {
					confirmBtn := ui.Row(c).Height(32).Padding(0, 16).Radius(7).Background(pal.Accent).AlignItems(ui.Center)
					confirmBtn.Children(func() {
						ui.Text(c, btnText).FontSize(12).Bold().TextColor(ui.Hex("#ffffff")).SingleLine()
					})
					if confirmBtn.Clicked() {
						app.submitCandidate()
						app.showModal = false
					}
				})
			}
		})
	})
}

func (app *AppUI) startExtract() {
	u := strings.TrimSpace(app.modalURL)
	if u == "" {
		return
	}
	app.isExtracting = true
	app.extractError = ""
	app.candidate = nil
	if app.window != nil {
		app.window.Invalidate()
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		cand, err := app.extractor.Extract(ctx, u)
		app.mu.Lock()
		app.isExtracting = false
		if err != nil {
			app.extractError = err.Error()
		} else {
			app.candidate = cand
			app.selectedFormat = 0
			app.downloadAllEpisodes = false
			app.selectedEpisodesMap = make(map[int]bool)
			if cand != nil {
				for _, ep := range cand.Episodes {
					app.selectedEpisodesMap[ep.Index] = true
				}
			}
		}
		app.mu.Unlock()

		if app.window != nil {
			app.window.Invalidate()
		}
	}()
}

func (app *AppUI) submitCandidate() {
	if app.candidate == nil || len(app.candidate.Formats) == 0 {
		return
	}

	fmtObj := app.candidate.Formats[app.selectedFormat]
	cleanMainTitle := sanitizeFilename(app.candidate.Title)
	if cleanMainTitle == "" {
		cleanMainTitle = "video_" + time.Now().Format("20060102_150405")
	}

	engineType := model.EngineHTTPRange
	downloadURL := fmtObj.URL
	if fmtObj.Protocol == "extractor" {
		engineType = model.EngineExtractor
		downloadURL = fmtObj.FormatID
	} else if fmtObj.Protocol == "m3u8" || strings.HasSuffix(fmtObj.URL, ".m3u8") {
		engineType = model.EngineHLS
	}

	// 模式 A: 批量下载选中的分集
	if len(app.candidate.Episodes) > 1 && app.downloadAllEpisodes {
		for _, ep := range app.candidate.Episodes {
			if !app.selectedEpisodesMap[ep.Index] {
				continue
			}
			taskTitle := fmt.Sprintf("%s [P%02d %s]", cleanMainTitle, ep.Index, sanitizeFilename(ep.Title))
			filename := fmt.Sprintf("%s.%s", sanitizeFilename(taskTitle), fmtObj.Ext)
			task := &model.Task{
				ID:             uuid.New().String(),
				Title:          taskTitle,
				SourceURL:      ep.URL,
				DownloadURL:    downloadURL,
				Engine:         engineType,
				Status:         model.StatusQueued,
				TotalBytes:     fmtObj.Filesize,
				OutputDir:      app.library.BaseDir(),
				OutputFilename: filename,
				Priority:       1,
				CreatedAt:      time.Now(),
				UpdatedAt:      time.Now(),
			}
			app.submitTaskRespectingAutoStart(task)
		}
		app.refreshData()
		return
	}

	// 模式 B: 单集下载（若属于多集中的某一集，在标题中附加分 P 序号）
	taskTitle := app.candidate.Title
	if len(app.candidate.Episodes) > 1 && app.candidate.CurrentPage > 0 && app.candidate.CurrentPage <= len(app.candidate.Episodes) {
		curEp := app.candidate.Episodes[app.candidate.CurrentPage-1]
		taskTitle = fmt.Sprintf("%s [P%02d %s]", cleanMainTitle, curEp.Index, sanitizeFilename(curEp.Title))
	}

	filename := fmt.Sprintf("%s.%s", sanitizeFilename(taskTitle), fmtObj.Ext)
	task := &model.Task{
		ID:             uuid.New().String(),
		Title:          taskTitle,
		SourceURL:      app.candidate.SourceURL,
		DownloadURL:    downloadURL,
		Engine:         engineType,
		Status:         model.StatusQueued,
		TotalBytes:     fmtObj.Filesize,
		OutputDir:      app.library.BaseDir(),
		OutputFilename: filename,
		Priority:       1,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	app.submitTaskRespectingAutoStart(task)
	app.refreshData()
}

// submitTaskRespectingAutoStart 按"自动开始新加入的任务"设置提交任务
// 关闭该设置时任务以暂停状态加入列表，等待用户手动点击继续
func (app *AppUI) submitTaskRespectingAutoStart(task *model.Task) {
	if config.Global().Get().AutoStart {
		_ = app.scheduler.Submit(task)
		return
	}
	_ = app.scheduler.SubmitIdle(task)
}

func sanitizeFilename(name string) string {
	invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	for _, ch := range invalidChars {
		name = strings.ReplaceAll(name, ch, "_")
	}
	return strings.TrimSpace(name)
}

func formatBytes(bytes int64) string {
	if bytes <= 0 {
		return "未知"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatSpeed(bytesPerSec int64) string {
	if bytesPerSec <= 0 {
		return "0 B/s"
	}
	return formatBytes(bytesPerSec) + "/s"
}

// HandleSniffedCandidate 当外部浏览器嗅探到媒体时被触发
func (app *AppUI) HandleSniffedCandidate(cand *model.Candidate) {
	app.mu.Lock()
	app.candidate = cand
	app.modalURL = cand.SourceURL
	app.showModal = true
	app.selectedFormat = 0
	app.mu.Unlock()

	if len(cand.Formats) == 0 {
		app.startExtract()
	} else if app.window != nil {
		app.window.Invalidate()
	}
}


// renderBrowserLauncherModal 渲染极简 Raycast 风格的网址输入与直达启动器 (单层原生模态设计，无双层底座)
func (app *AppUI) renderBrowserLauncherModal(c *ui.Context, pal Palette) {
	ui.Modal(c, &app.showBrowserLauncher, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			// 1. 一体化纯净搜索栏
			ui.Row(c).Height(40).Padding(0, 12).Gap(10).Radius(8).
				Background(pal.InputBg).BorderWidth(1).BorderColor(pal.InputBorder).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconSearch).FontSize(16).TextColor(pal.TextSecondary)
				in := ui.TextInputBase(c, &app.launcherInput).Placeholder("输入网址或搜索关键词 (回车直达)...").
					FontSize(13).Grow(1).MinWidth(0)
				in.Focus()
				if in.Submitted() || in.Shortcut(0, ui.KeyEnter) {
					app.launchBrowserFromInput()
				}

				// 极简回车徽标
				enterBadge := ui.Row(c).Height(20).Padding(0, 6).Radius(4).
					Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
				enterBadge.Children(func() {
					ui.Text(c, "↵").FontSize(11).Bold().TextColor(pal.TextMuted)
				})
			})

			// 2. 常用平台快捷直达网格 (充满整行宽度，不留右侧空隙)
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "快捷直达").FontSize(11).Bold().TextColor(pal.TextMuted)

				hubs := []struct {
					name  string
					url   string
					color string
				}{
					{"X", "https://x.com", "#1da1f2"},
					{"Facebook", "https://www.facebook.com", "#1877f2"},
					{"Threads", "https://www.threads.com", "#e1306c"},
					{"哔哩哔哩", "https://www.bilibili.com", "#fb7299"},
					{"YouTube", "https://www.youtube.com", "#ff0000"},
					{"抖音", "https://www.douyin.com", "#00f2fe"},
					{"微博视频", "https://weibo.com", "#fa7d3c"},
					{"腾讯视频", "https://v.qq.com", "#0052d9"},
					{"爱奇艺", "https://www.iqiyi.com", "#00cc4c"},
				}

				for row := 0; row < 3; row++ {
					start := row * 3
					end := start + 3
					ui.Row(c).Gap(8).Children(func() {
						for _, hub := range hubs[start:end] {
							h := hub
							card := ui.Row(c).Height(34).Grow(1).Padding(0, 12).Gap(8).Radius(8).
								Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
							card.Children(func() {
								ui.Box(c).Size(7, 7).Radius(3.5).Background(ui.Hex(h.color))
								ui.Text(c, h.name).FontSize(12).TextColor(pal.TextPrimary).Grow(1)
								ui.Text(c, "›").FontSize(12).TextColor(pal.TextMuted)
							})
							if card.Clicked() {
								app.showBrowserLauncher = false
								app.browserMgr.Open(h.url)
							}
						}
					})
				}
			})
		})
	})
}

func (app *AppUI) launchBrowserFromInput() {
	raw := strings.TrimSpace(app.launcherInput)
	if raw == "" {
		raw = "https://www.bilibili.com"
	} else if !strings.Contains(raw, ".") && !strings.HasPrefix(raw, "http") {
		raw = "https://search.bilibili.com/all?keyword=" + url.QueryEscape(raw)
	} else if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	app.showBrowserLauncher = false
	app.launcherInput = ""
	app.browserMgr.Open(raw)
}







// renderShortcutsModal 渲染键盘快捷键速查面板 (对标 godiff/palette.go shortcutsHelp)
func (app *AppUI) renderShortcutsModal(c *ui.Context, pal Palette) {
	groups := []struct {
		title string
		keys  [][2]string
	}{
		{"任务与嗅探", [][2]string{
			{"新建下载任务", "⌘N"},
			{"打开专用浏览器", "⌘B"},
			{"确认解析 / 开始下载", "↩"},
		}},
		{"系统与偏好", [][2]string{
			{"偏好设置", "⌘,"},
			{"快捷键速查", "⌘/"},
			{"关闭浮层或弹窗", "Esc"},
			{"重新加载界面", "⌘R"},
		}},
	}

	ui.Modal(c, &app.showShortcuts, func() {
		ui.Column(c).Width(520).Gap(16).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "键盘快捷键速查 (Keyboard Shortcuts)").FontSize(15).Bold().Grow(1).TextColor(pal.TextPrimary)
				if ui.Button(c, "完成").Clicked() {
					app.showShortcuts = false
				}
			})

			ui.Grid(c).Columns(2).GapX(24).GapY(12).Children(func() {
				for _, g := range groups {
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, strings.ToUpper(g.title)).FontSize(11).Bold().TextColor(pal.TextMuted)
						for _, k := range g.keys {
							ui.Row(c).MinHeight(26).AlignItems(ui.Center).Children(func() {
								ui.Text(c, k[0]).FontSize(12).Grow(1).TextColor(pal.TextSecondary)
								ui.Text(c, k[1]).FontSize(11).Bold().Padding(2, 6).Radius(5).Background(pal.CardBorder).TextColor(pal.TextPrimary)
							})
						}
					})
				}
			})

			ui.Row(c).Justify(ui.End).Children(func() {
				if ui.PrimaryButton(c, "关闭").Clicked() {
					app.showShortcuts = false
				}
			})
		})
	})
}

// renderEpisodesModal 渲染分集多选二级模态弹窗
func (app *AppUI) renderEpisodesModal(c *ui.Context, pal Palette) {
	if app.candidate == nil || len(app.candidate.Episodes) == 0 {
		return
	}
	ui.Modal(c, &app.showEpisodesModal, func() {
		ui.Column(c).Width(480).Gap(12).Children(func() {
			// 1. 顶部标题与全选切换
			selectedCount := 0
			for _, ep := range app.candidate.Episodes {
				if app.selectedEpisodesMap[ep.Index] {
					selectedCount++
				}
			}
			isAllSelected := selectedCount == len(app.candidate.Episodes)

			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "选择批量下载分集").FontSize(14).Bold().TextColor(pal.TextPrimary)
				ui.Box(c).Width(8)
				ui.Text(c, fmt.Sprintf("(已选 %d/%d 集)", selectedCount, len(app.candidate.Episodes))).FontSize(12).TextColor(pal.Accent)

				ui.Spacer(c)

				toggleAllText := "全选"
				if isAllSelected {
					toggleAllText = "取消全选"
				}
				toggleBtn := ui.Row(c).Height(24).Padding(0, 10).Radius(5).Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				toggleBtn.Children(func() {
					ui.Text(c, toggleAllText).FontSize(11).TextColor(pal.TextSecondary)
				})
				if toggleBtn.Clicked() {
					newVal := !isAllSelected
					for _, ep := range app.candidate.Episodes {
						app.selectedEpisodesMap[ep.Index] = newVal
					}
				}
			})

			// 2. 分集滚动列表
			ui.Scroll(c).Vertical().Height(260).Background(pal.InputBg).Radius(8).Padding(6).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					for _, ep := range app.candidate.Episodes {
						e := ep
						isSel := app.selectedEpisodesMap[e.Index]
						rowBg := pal.CardBg
						if isSel {
							rowBg = pal.AccentLight
						}
						r := ui.Row(c).Height(32).Padding(0, 10).Gap(8).Radius(6).Background(rowBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
						r.Children(func() {
							chkBox := ui.Row(c).Size(16, 16).Radius(4).Center()
							if isSel {
								chkBox.Background(pal.Accent)
								chkBox.Children(func() {
									ui.Icon(c, iconCheck).FontSize(10).TextColor(ui.Hex("#ffffff"))
								})
							} else {
								chkBox.Background(pal.InputBg).BorderWidth(1.2).BorderColor(pal.InputBorder)
							}

							ui.Box(c).Padding(1, 5).Radius(3).Background(pal.InputBg).Children(func() {
								ui.Text(c, fmt.Sprintf("P%02d", e.Index)).FontSize(10).Bold().TextColor(pal.TextSecondary)
							})

							titleColor := pal.TextPrimary
							if isSel {
								titleColor = pal.Accent
							}
							ui.Text(c, e.Title).FontSize(12).TextColor(titleColor).SingleLine().Grow(1).MinWidth(0)
						})

						if r.Clicked() {
							app.selectedEpisodesMap[e.Index] = !isSel
						}
					}
				})
			})

			// 3. 底部确定与取消按钮 (统一高度 28px、内边距与圆角，视觉严格对齐)
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)

				cancelBtn := ui.Row(c).Height(28).Padding(0, 16).Radius(6).Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				cancelBtn.Children(func() {
					ui.Text(c, "取消").FontSize(12).TextColor(pal.TextPrimary)
				})
				if cancelBtn.Clicked() {
					app.showEpisodesModal = false
				}

				confirmBtn := ui.Row(c).Height(28).Padding(0, 16).Radius(6).Background(pal.Accent).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				confirmBtn.Children(func() {
					ui.Text(c, "确定").FontSize(12).Bold().TextColor(ui.Hex("#ffffff"))
				})
				if confirmBtn.Clicked() {
					app.downloadAllEpisodes = true
					app.showEpisodesModal = false
				}
			})
		})
	})
}

// renderDeleteConfirmModal 渲染删除/移除二次确认弹窗
func (app *AppUI) renderDeleteConfirmModal(c *ui.Context, pal Palette) {
	ui.Modal(c, &app.showDeleteConfirm, func() {
		ui.Column(c).Width(420).Gap(12).Children(func() {
			asset := app.deleteAssetTarget
			task := app.deleteTaskTarget

			// 1. 标题
			title := "移除媒体记录"
			if asset == nil && task != nil {
				title = "删除下载任务"
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconTrash).FontSize(14).TextColor(pal.Danger)
				ui.Text(c, title).FontSize(14).Bold().TextColor(pal.TextPrimary)
			})

			// 2. 说明与目标信息
			if asset != nil {
				ui.Text(c, "将从媒体资料库中移除这条记录，此操作不可撤销。").FontSize(12).TextColor(pal.TextSecondary)
				ui.Column(c).Fill().Padding(8, 10).Gap(3).Radius(6).Background(pal.InputBg).Children(func() {
					ui.Text(c, asset.Title).FontSize(12).TextColor(pal.TextPrimary).SingleLine()
					ui.Text(c, asset.FilePath).FontSize(11).TextColor(pal.TextMuted).SingleLine()
				})

				// 同时删除磁盘文件的勾选行
				fileRow := ui.Row(c).Height(30).Padding(0, 10).Gap(8).Radius(6).Background(pal.InputBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				fileRow.Children(func() {
					chkBox := ui.Row(c).Size(16, 16).Radius(4).Center()
					if app.deleteFileToo {
						chkBox.Background(pal.Danger)
						chkBox.Children(func() {
							ui.Icon(c, iconCheck).FontSize(10).TextColor(ui.Hex("#ffffff"))
						})
					} else {
						chkBox.Background(pal.CardBg).BorderWidth(1.2).BorderColor(pal.InputBorder)
					}
					ui.Text(c, "同时将视频文件移到废纸篓").FontSize(12).TextColor(pal.TextPrimary)
				})
				if fileRow.Clicked() {
					app.deleteFileToo = !app.deleteFileToo
				}
				if !app.deleteFileToo {
					ui.Text(c, "不勾选时，磁盘上的视频文件将保留，仅移除资料库记录。").FontSize(11).TextColor(pal.TextMuted)
				}
			} else if task != nil {
				switch task.Status {
				case model.StatusDownloading, model.StatusQueued:
					ui.Text(c, "该任务正在下载中，删除将立即中断当前下载进度。").FontSize(12).TextColor(pal.Warning)
				case model.StatusCompleted:
					ui.Text(c, "该任务记录将被删除；已归档到媒体资料库的视频文件不受影响。").FontSize(12).TextColor(pal.TextSecondary)
				default:
					ui.Text(c, "该任务记录将被删除，已下载的临时进度会保留，重新添加后可断点续传。").FontSize(12).TextColor(pal.TextSecondary)
				}
				ui.Column(c).Fill().Padding(8, 10).Gap(3).Radius(6).Background(pal.InputBg).Children(func() {
					ui.Text(c, task.Title).FontSize(12).TextColor(pal.TextPrimary).SingleLine()
					if task.FinalPath != "" {
						ui.Text(c, task.FinalPath).FontSize(11).TextColor(pal.TextMuted).SingleLine()
					}
				})
			}

			// 3. 底部按钮 (取消 / 确认删除)
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)

				cancelBtn := ui.Row(c).Height(28).Padding(0, 16).Radius(6).Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				cancelBtn.Children(func() {
					ui.Text(c, "取消").FontSize(12).TextColor(pal.TextPrimary)
				})
				if cancelBtn.Clicked() {
					app.closeDeleteConfirm()
				}

				confirmLabel := "移除记录"
				if asset == nil && task != nil {
					confirmLabel = "删除任务"
				}
				confirmBtn := ui.Row(c).Height(28).Padding(0, 16).Radius(6).Background(pal.Danger).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				confirmBtn.Children(func() {
					ui.Text(c, confirmLabel).FontSize(12).Bold().TextColor(ui.Hex("#ffffff"))
				})
				if confirmBtn.Clicked() {
					app.performConfirmedDelete()
				}
			})
		})
	})
}

// closeDeleteConfirm 关闭删除确认弹窗并清空待删除目标
func (app *AppUI) closeDeleteConfirm() {
	app.showDeleteConfirm = false
	app.deleteAssetTarget = nil
	app.deleteTaskTarget = nil
	app.deleteFileToo = false
}

// performConfirmedDelete 执行用户已二次确认的删除操作
func (app *AppUI) performConfirmedDelete() {
	asset := app.deleteAssetTarget
	task := app.deleteTaskTarget
	removeFile := app.deleteFileToo

	app.closeDeleteConfirm()

	if asset != nil {
		if removeFile {
			if err := app.library.MoveToTrash(asset.FilePath); err != nil {
				// 移入废纸篓失败时保留资料库记录，避免文件与记录双双消失造成数据丢失
				log.Printf("[ui] 移动文件到废纸篓失败 (%s): %v", asset.FilePath, err)
				app.refreshData()
				return
			}
		}
		_ = app.storage.DeleteAsset(asset.ID)
		app.refreshData()
		return
	}
	if task != nil {
		// CancelTask 会同时中断运行中的引擎并登记删除墓碑，避免进度回写"复活"任务
		_ = app.scheduler.CancelTask(task.ID)
		app.refreshData()
	}
}
