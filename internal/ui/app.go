package ui

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
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

	// 浏览器 Raycast 风格 Launcher 启动器状态
	showBrowserLauncher bool
	launcherInput       string

	// 分栏偏好设置状态 (对标 Raycast Settings)
	showSettings   bool
	settingsTab    string // "general", "downloader", "browser", "storage", "about"
	settingsSearch string

	// 键盘快捷键速查面板状态
	showShortcuts bool
}

// RefreshUI 触发主窗口重绘
func (app *AppUI) RefreshUI() {
	if app.window != nil {
		app.window.Invalidate()
	}
}

// OpenSettings 打开偏好设置面板
func (app *AppUI) OpenSettings() {
	app.showSettings = true
	if app.settingsTab == "" {
		app.settingsTab = "general"
	}
	app.RefreshUI()
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
		currentTab:          "downloading",
		showModal:           false,
		selectedFormat:      0,
		downloadAllEpisodes: false,
		settingsTab:         "general",
	}

	app.refreshData()

	// 调度器状态变动时触发 UI 重新绘制
	sc.OnUpdate(func(t *model.Task) {
		app.refreshData()
		if app.window != nil {
			app.window.Invalidate()
		}
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

// View 主渲染循环
func (app *AppUI) View(c *ui.Context) {
	theme := c.Theme()
	pal := getPalette(theme)

	// 在 macOS 下根背景必须为 Transparent，以使窗口底层的 NSVisualEffectView 磨砂毛玻璃透出
	c.Root().Background(pal.SidebarBg)

	// 纵向彻底拉伸填满窗口，避免任何未渲染的纯黑底层暴露
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		// 1. 左侧 Sidebar (Apple 原生磨砂侧边栏)
		app.renderSidebar(c, pal)

		// 2. 细腻垂直分割线
		ui.Box(c).Width(1).Shrink(0).Background(pal.HeaderBorder)

		// 3. 右侧主体工作区 (Toolbar + 主内容流)
		ui.Column(c).Grow(1).MinWidth(0).Background(pal.ContentBg).Children(func() {
			app.renderTopBar(c, pal)
			app.renderMainContent(c, pal)
		})
	})

	// 全局快捷键处理: ⌘N 新建下载, ⌘B 网址启动器, ⌘, 偏好设置, ⌘/ 快捷键速查
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		app.OpenNewDownload()
	}
	if c.Shortcut(ui.Cmd, ui.KeyB) {
		app.OpenBrowserLauncher()
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		app.OpenSettings()
	}
	if c.Shortcut(ui.Cmd, ui.KeySlash) {
		app.showShortcuts = !app.showShortcuts
	}

	// 模态弹窗渲染 (基于 mygo ui.Modal 原生规范)
	if app.showModal {
		app.renderModalDialog(c, pal)
	}
	if app.showBrowserLauncher {
		app.renderBrowserLauncherModal(c, pal)
	}
	if app.showSettings {
		app.renderSettingsModal(c, pal)
	}
	if app.showShortcuts {
		app.renderShortcutsModal(c, pal)
	}
}

// renderSidebar 渲染左侧原生侧栏
func (app *AppUI) renderSidebar(c *ui.Context, pal Palette) {
	bar := c.TitleBar()
	// 左侧避让 macOS 红黄绿交通灯
	left := max(bar.Left+10, 14)

	ui.Column(c).Width(240).Shrink(0).Background(pal.SidebarBg).Children(func() {
		// 顶部标题栏高度 52pt，与系统交通灯对齐
		ui.Row(c).Height(titleBarHeight).Padding(0, 12, 0, left).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, "MediaGo").FontSize(13).Bold().TextColor(pal.TextPrimary)
			ui.Box(c).Margin(0, 0, 0, 6).Padding(1, 5).Radius(4).Background(pal.AccentLight).Children(func() {
				ui.Text(c, "v1.0").FontSize(10).Bold().TextColor(pal.Accent)
			})
			ui.Spacer(c)
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

			app.renderSidebarItem(c, "downloading", "下载中", iconDownload, activeCount, pal)
			app.renderSidebarItem(c, "completed", "已完成", iconCheckCircle, 0, pal)
			app.renderSidebarItem(c, "paused", "已暂停", iconPause, 0, pal)
			app.renderSidebarItem(c, "failed", "下载失败", iconAlertCircle, 0, pal)
			app.renderSidebarItem(c, "all", "全部任务", iconList, 0, pal)

			ui.Box(c).Height(12)

			app.renderSectionHeader(c, "媒体资源", pal)
			app.renderSidebarItem(c, "library", "媒体资料库", iconFilm, 0, pal)
		})

		// 底部工具条: [设置纯图标] [浏览器纯图标] (颜色完全一致、尺寸对称、纯图标设计)
		ui.Row(c).Padding(10, 12, 14, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			settingsBtn := ui.Row(c).Height(34).Grow(1).Radius(8).
				Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).
				Justify(ui.Center).AlignItems(ui.Center)
			settingsBtn.Children(func() {
				ui.Icon(c, iconSettings).FontSize(15).TextColor(pal.TextSecondary)
			})
			if settingsBtn.Clicked() {
				app.OpenSettings()
			}

			browserBtn := ui.Row(c).Height(34).Grow(1).Radius(8).
				Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).
				Justify(ui.Center).AlignItems(ui.Center)
			browserBtn.Children(func() {
				ui.Icon(c, iconGlobe).FontSize(15).TextColor(pal.TextSecondary)
			})
			if browserBtn.Clicked() {
				app.OpenBrowserLauncher()
			}
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
		label := ui.Text(c, title).FontSize(13).TextColor(textC)
		if isSelected {
			label.Bold()
		}
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

// renderTopBar 渲染右侧顶部无缝融合的 Toolbar
func (app *AppUI) renderTopBar(c *ui.Context, pal Palette) {
	var totalSpeed int64
	app.mu.RLock()
	for _, t := range app.tasks {
		if t.Status == model.StatusDownloading {
			totalSpeed += t.Speed
		}
	}
	app.mu.RUnlock()

	ui.Row(c).Height(titleBarHeight).Padding(0, 20).BorderWidth(0, 0, 1, 0).BorderColor(pal.HeaderBorder).Background(pal.HeaderBg).AlignItems(ui.Center).DragWindow().Children(func() {
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
			ui.Box(c).Width(10)
		}

		// 操作按钮组
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			browBtn := ui.Row(c).Height(28).Padding(0, 10).Gap(6).Radius(6).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
			browBtn.Children(func() {
				ui.Icon(c, iconGlobe).FontSize(12).TextColor(pal.TextPrimary)
				ui.Text(c, "专用浏览器").FontSize(12).TextColor(pal.TextPrimary)
			})
			if browBtn.Clicked() {
				app.OpenBrowserLauncher()
			}

			newBtn := ui.Row(c).Height(28).Padding(0, 12).Gap(6).Radius(6).Background(pal.Accent).AlignItems(ui.Center)
			newBtn.Children(func() {
				ui.Icon(c, iconPlus).FontSize(12).TextColor(ui.Hex("#ffffff"))
				ui.Text(c, "新建下载").FontSize(12).Bold().TextColor(ui.Hex("#ffffff"))
			})
			if newBtn.Clicked() {
				app.showModal = true
				app.modalURL = ""
				app.candidate = nil
				app.extractError = ""
				if app.window != nil {
					app.window.Invalidate()
				}
			}
		})
	})
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
			ui.Text(c, "点击右上角「新建下载」或通过专用浏览器嗅探网页视频").FontSize(13).TextColor(pal.TextSecondary)

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
				statusText = "已完成"
				statusBg = pal.SuccessLight
				statusTc = pal.Success
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

				if t.Status == model.StatusCompleted && t.FinalPath != "" {
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
						ui.Icon(c, iconEye).FontSize(12).TextColor(pal.TextPrimary)
						ui.Text(c, "预览").FontSize(11).TextColor(pal.TextPrimary)
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
					_ = app.scheduler.Pause(t.ID)
					_ = app.storage.DeleteTask(t.ID)
					app.refreshData()
				}
			})
		})
	})
}

// renderLibraryCards 渲染本地媒体库卡片
func (app *AppUI) renderLibraryCards(c *ui.Context, pal Palette) {
	app.mu.RLock()
	assets := app.assets
	app.mu.RUnlock()

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
				card := ui.Row(c).Padding(12, 16).Gap(12).Radius(10).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
				card.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.03))

				card.Children(func() {
					ui.Icon(c, iconFilm).FontSize(24).TextColor(pal.Accent)

					ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
						ui.Text(c, a.Title).FontSize(13).Bold().TextColor(pal.TextPrimary).SingleLine()
						meta := fmt.Sprintf("%s · %s", formatBytes(a.FileSize), a.FilePath)
						ui.Text(c, meta).FontSize(11).TextColor(pal.TextSecondary).SingleLine()
					})

					ui.Row(c).Gap(6).Children(func() {
						fBtn := ui.Button(c, "在 Finder 中显示").FontSize(11)
						if fBtn.Clicked() {
							_ = app.library.OpenInFinder(a.FilePath)
						}
						qBtn := ui.Button(c, "预览").FontSize(11)
						if qBtn.Clicked() {
							_ = app.library.QuickLook(a.FilePath)
						}
						rmBtn := ui.Button(c, "移除记录").FontSize(11)
						if rmBtn.Clicked() {
							_ = app.storage.DeleteAsset(a.ID)
							app.refreshData()
						}
					})
				})
			}
		})
	})
}

// renderModalDialog 渲染 Spotlight / Raycast 风格的悬浮模态框 (基于 mygo ui.Modal)
func (app *AppUI) renderModalDialog(c *ui.Context, pal Palette) {
	ui.Modal(c, &app.showModal, func() {
		modal := ui.Column(c).Width(540).Padding(20).Gap(14).Radius(14).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder)
		modal.Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.25))

		modal.Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "新建媒体下载").FontSize(16).Bold().TextColor(pal.TextPrimary)
				ui.Spacer(c)
				closeBtn := ui.ButtonBase(c).Size(20, 20).Radius(10).Center().Background(pal.InputBg)
				closeBtn.Children(func() {
					ui.Icon(c, iconClose).FontSize(12).TextColor(pal.TextSecondary)
				})
				if closeBtn.Clicked() {
					app.showModal = false
				}
			})

			ui.Row(c).Height(38).Padding(0, 10).Gap(8).Radius(8).Background(pal.InputBg).BorderWidth(1).BorderColor(pal.InputBorder).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconGlobe).FontSize(14).TextColor(pal.TextMuted)
				in := ui.TextInputBase(c, &app.modalURL).Placeholder("粘贴视频网址或直链地址 (MP4, HLS, B站, YouTube 等)").FontSize(13).Grow(1).MinWidth(0)
				if in.Shortcut(0, ui.KeyEnter) {
					if app.candidate != nil && len(app.candidate.Formats) > 0 {
						app.submitCandidate()
						app.showModal = false
					} else {
						app.startExtract()
					}
				}
				parseBtn := ui.Button(c, "解析").FontSize(12)
				if parseBtn.Clicked() {
					app.startExtract()
				}
			})

			if app.isExtracting {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconRefresh).FontSize(14).TextColor(pal.Accent)
					ui.Text(c, "正在深入探测解析视频规格与流媒体分片...").FontSize(12).TextColor(pal.Accent)
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
								ui.Text(c, fmt.Sprintf("检测到多集视频 (共 %d 集)", len(app.candidate.Episodes))).FontSize(12).Bold().TextColor(pal.TextPrimary)
								if app.candidate.CurrentPage > 0 && app.candidate.CurrentPage <= len(app.candidate.Episodes) {
									curEp := app.candidate.Episodes[app.candidate.CurrentPage-1]
									ui.Spacer(c)
									ui.Text(c, fmt.Sprintf("当前位于: P%d %s", curEp.Index, curEp.Title)).FontSize(11).TextColor(pal.Accent)
								}
							})

							ui.Row(c).Gap(8).Children(func() {
								curLabel := fmt.Sprintf("仅下载当前集 (P%d)", app.candidate.CurrentPage)
								btnCur := ui.Row(c).Height(26).Padding(0, 12).Radius(6).AlignItems(ui.Center)
								if !app.downloadAllEpisodes {
									btnCur.Background(pal.AccentLight).BorderWidth(1).BorderColor(pal.Accent)
									btnCur.Children(func() {
										ui.Text(c, curLabel).FontSize(11).Bold().TextColor(pal.Accent)
									})
								} else {
									btnCur.Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder)
									btnCur.Children(func() {
										ui.Text(c, curLabel).FontSize(11).TextColor(pal.TextSecondary)
									})
								}
								if btnCur.Clicked() {
									app.downloadAllEpisodes = false
								}

								allLabel := fmt.Sprintf("批量下载全部 %d 集", len(app.candidate.Episodes))
								btnAll := ui.Row(c).Height(26).Padding(0, 12).Radius(6).AlignItems(ui.Center)
								if app.downloadAllEpisodes {
									btnAll.Background(pal.AccentLight).BorderWidth(1).BorderColor(pal.Accent)
									btnAll.Children(func() {
										ui.Text(c, allLabel).FontSize(11).Bold().TextColor(pal.Accent)
									})
								} else {
									btnAll.Background(pal.InputBg).BorderWidth(1).BorderColor(pal.CardBorder)
									btnAll.Children(func() {
										ui.Text(c, allLabel).FontSize(11).TextColor(pal.TextSecondary)
									})
								}
								if btnAll.Clicked() {
									app.downloadAllEpisodes = true
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

			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				cancelBtn := ui.Button(c, "取消").FontSize(12)
				if cancelBtn.Clicked() {
					app.showModal = false
				}

				if app.candidate != nil {
					btnText := "立即开始下载"
					if len(app.candidate.Episodes) > 1 {
						if app.downloadAllEpisodes {
							btnText = fmt.Sprintf("立即批量下载全部 %d 集", len(app.candidate.Episodes))
						} else {
							btnText = fmt.Sprintf("立即下载当前集 (P%d)", app.candidate.CurrentPage)
						}
					}
					confirmBtn := ui.Row(c).Height(32).Padding(0, 16).Radius(7).Background(pal.Accent).AlignItems(ui.Center)
					confirmBtn.Children(func() {
						ui.Text(c, btnText).FontSize(12).Bold().TextColor(ui.Hex("#ffffff"))
					})
					if confirmBtn.Clicked() {
						app.submitCandidate()
						app.showModal = false
					}
				}
			})
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

	// 模式 A: 批量下载全部分集
	if len(app.candidate.Episodes) > 1 && app.downloadAllEpisodes {
		for _, ep := range app.candidate.Episodes {
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
			_ = app.scheduler.Submit(task)
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

	_ = app.scheduler.Submit(task)
	app.refreshData()
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


// renderBrowserLauncherModal 渲染 Raycast 风格的沉浸式网址输入与快速导航启动器
func (app *AppUI) renderBrowserLauncherModal(c *ui.Context, pal Palette) {
	ui.Modal(c, &app.showBrowserLauncher, func() {
		ui.Column(c).Width(580).Gap(14).Children(func() {
			// 1. Raycast 风格大输入行
			ui.Row(c).Height(46).Padding(0, 14).Gap(10).Radius(10).
				Background(pal.CardBg).BorderWidth(1).BorderColor(pal.Accent).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconGlobe).FontSize(18).TextColor(pal.Accent)
				in := ui.TextInput(c, &app.launcherInput).Placeholder("输入视频网页地址或搜索关键词，按回车启动专用浏览器...").
					FontSize(13).Grow(1).TextColor(pal.TextPrimary)
				in.Focus()
				if in.Shortcut(0, ui.KeyEnter) {
					app.launchBrowserFromInput()
				}

				openBadge := ui.Row(c).Height(24).Padding(0, 8).Radius(6).Background(pal.AccentLight).AlignItems(ui.Center)
				openBadge.Children(func() {
					ui.Text(c, "前往 ↩").FontSize(11).Bold().TextColor(pal.Accent)
				})
				if openBadge.Clicked() {
					app.launchBrowserFromInput()
				}
			})

			// 2. 常用视频平台快捷直达标签
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "常用视频平台快捷直达").FontSize(11).Bold().TextColor(pal.TextMuted)
				hubs := []struct {
					name string
					url  string
				}{
					{"哔哩哔哩 Bilibili", "https://www.bilibili.com"},
					{"YouTube", "https://www.youtube.com"},
					{"抖音 Douyin", "https://www.douyin.com"},
					{"Twitter / X", "https://x.com"},
					{"腾讯视频", "https://v.qq.com"},
					{"爱奇艺", "https://www.iqiyi.com"},
				}

				ui.Row(c).Gap(6).Children(func() {
					for _, hub := range hubs[:3] {
						h := hub
						tag := ui.Row(c).Height(28).Padding(0, 10).Gap(6).Radius(6).
							Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
						tag.Children(func() {
							ui.Text(c, h.name).FontSize(11).TextColor(pal.TextSecondary)
						})
						if tag.Clicked() {
							app.showBrowserLauncher = false
							app.browserMgr.Open(h.url)
						}
					}
				})
				ui.Row(c).Gap(6).Children(func() {
					for _, hub := range hubs[3:] {
						h := hub
						tag := ui.Row(c).Height(28).Padding(0, 10).Gap(6).Radius(6).
							Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
						tag.Children(func() {
							ui.Text(c, h.name).FontSize(11).TextColor(pal.TextSecondary)
						})
						if tag.Clicked() {
							app.showBrowserLauncher = false
							app.browserMgr.Open(h.url)
						}
					}
				})
			})

			// 3. 底部微提示
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconCheckCircle).FontSize(11).TextColor(pal.Success)
				ui.Box(c).Width(4)
				ui.Text(c, "专用浏览器内置 WebKit Chrome 131 桌面引擎与流媒体深度嗅探").FontSize(11).TextColor(pal.TextMuted)
				ui.Spacer(c)
				ui.Text(c, "Esc 关闭").FontSize(10).TextColor(pal.TextMuted)
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

// renderSettingsModal 渲染现代 Raycast / macOS 系统设置双分栏面板 (对标 media_1791632651200)
func (app *AppUI) renderSettingsModal(c *ui.Context, pal Palette) {
	cfg := config.Global()
	s := cfg.Get()

	ui.Modal(c, &app.showSettings, func() {
		// 整体分栏容器: 宽 760, 高 490
		ui.Row(c).Width(760).Height(490).Radius(12).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).Children(func() {
			// =========================
			// 1. 左侧导航边栏 (宽 210)
			// =========================
			ui.Column(c).Width(210).Shrink(0).Padding(16, 12).Gap(10).Background(pal.SidebarBg).Children(func() {
				// 应用身份卡片
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Row(c).Size(28, 28).Radius(7).Background(pal.Accent).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconFilm).FontSize(15).TextColor(ui.Hex("#ffffff"))
					})
					ui.Column(c).Gap(1).Children(func() {
						ui.Text(c, "MediaGo").FontSize(13).Bold().TextColor(pal.TextPrimary)
						ui.Text(c, "设置中心 · Pro").FontSize(10).TextColor(pal.TextMuted)
					})
				})

				// 搜索设置输入框
				ui.Row(c).Height(28).Padding(0, 8).Gap(6).Radius(6).
					Background(pal.InputBg).BorderWidth(1).BorderColor(pal.InputBorder).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconSearch).FontSize(12).TextColor(pal.TextMuted)
					ui.TextInput(c, &app.settingsSearch).Placeholder("搜索设置...").FontSize(11).Grow(1).TextColor(pal.TextPrimary)
				})

				ui.Box(c).Height(4)

				// 分类导航项
				navTabs := []struct {
					id    string
					label string
					icon  *ui.SVG
				}{
					{"general", "通用设置 (General)", iconSettings},
					{"downloader", "下载与引擎 (Downloader)", iconDownload},
					{"browser", "嗅探浏览器 (Browser)", iconGlobe},
					{"storage", "存储与目录 (Storage)", iconFolder},
					{"about", "关于与快捷键 (About)", iconInfo},
				}

				ui.Column(c).Gap(3).Grow(1).Children(func() {
					for _, tab := range navTabs {
						t := tab
						isSel := app.settingsTab == t.id
						bg := ui.Transparent
						textC := pal.TextSecondary
						if isSel {
							bg = pal.AccentLight
							textC = pal.Accent
						}

						item := ui.Row(c).Height(32).Padding(0, 10).Gap(8).Radius(6).Background(bg).AlignItems(ui.Center)
						item.Children(func() {
							ui.Icon(c, t.icon).FontSize(13).TextColor(textC)
							ui.Text(c, t.label).FontSize(12).TextColor(textC)
						})
						if item.Clicked() {
							app.settingsTab = t.id
						}
					}
				})
			})

			// 细腻垂直分割线
			ui.Box(c).Width(1).Height(490).Background(pal.CardBorder)

			// =========================
			// 2. 右侧详情配置区 (宽 549)
			// =========================
			ui.Column(c).Width(548).Shrink(0).Padding(16, 20).Gap(16).Children(func() {
				// 顶部 Header: 标题与极简关闭图标
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					titleText := "通用设置"
					switch app.settingsTab {
					case "downloader":
						titleText = "下载与引擎"
					case "browser":
						titleText = "嗅探浏览器"
					case "storage":
						titleText = "存储与目录"
					case "about":
						titleText = "关于 MediaGo"
					}
					ui.Text(c, titleText).FontSize(16).Bold().TextColor(pal.TextPrimary)
					ui.Spacer(c)

					// 纯图标关闭按钮 (Esc 退出，无任何多余“完成”按钮)
					closeBtn := ui.Row(c).Size(24, 24).Radius(12).Justify(ui.Center).AlignItems(ui.Center)
					closeBtn.Children(func() {
						ui.Icon(c, iconClose).FontSize(13).TextColor(pal.TextSecondary)
					})
					if closeBtn.Clicked() {
						app.showSettings = false
					}
				})

				// 详情面板根据 settingsTab 动态渲染
				switch app.settingsTab {
				case "general":
					app.renderSettingsGeneral(c, pal, cfg, s)
				case "downloader":
					app.renderSettingsDownloader(c, pal, cfg, s)
				case "browser":
					app.renderSettingsBrowser(c, pal, cfg, s)
				case "storage":
					app.renderSettingsStorage(c, pal, cfg, s)
				case "about":
					app.renderSettingsAbout(c, pal, cfg, s)
				default:
					app.renderSettingsGeneral(c, pal, cfg, s)
				}
			})
		})
	})
}

// renderSettingsGeneral 渲染通用设置 (外观、通知、启动)
func (app *AppUI) renderSettingsGeneral(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(14).Children(func() {
		// 外观主题
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "APPEARANCE 外观").FontSize(10).Bold().TextColor(pal.TextMuted)

			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "界面色彩外观 (Theme)").FontSize(12).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "跟随 macOS 系统设置或指定明暗模式").FontSize(11).TextColor(pal.TextSecondary)
				})

				themes := []struct{ id, label string }{
					{"system", "跟随系统"},
					{"light", "浅色"},
					{"dark", "深色"},
				}
				ui.Row(c).Gap(4).Children(func() {
					for _, th := range themes {
						isSel := s.Theme == th.id
						bg := pal.CardBg
						textC := pal.TextSecondary
						borderC := pal.CardBorder
						if isSel {
							bg = pal.AccentLight
							textC = pal.Accent
							borderC = pal.Accent
						}
						btn := ui.Row(c).Height(26).Padding(0, 10).Radius(5).Background(bg).BorderWidth(1).BorderColor(borderC).AlignItems(ui.Center)
						btn.Children(func() {
							ui.Text(c, th.label).FontSize(11).Bold().TextColor(textC)
						})
						if btn.Clicked() {
							cfg.Update(func(st *config.Settings) { st.Theme = th.id })
						}
					}
				})
			})
		})

		// 系统行为
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "NOTIFICATIONS & ACTIONS 交互").FontSize(10).Bold().TextColor(pal.TextMuted)

			// 通知开关
			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "任务完成系统原生通知").FontSize(12).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "视频下载或转码完成时在通知中心显示提示").FontSize(11).TextColor(pal.TextSecondary)
				})

				swBg := pal.CardBorder
				if s.Notifications {
					swBg = pal.Accent
				}
				sw := ui.Row(c).Size(36, 20).Radius(10).Background(swBg).Padding(2, 2).AlignItems(ui.Center)
				if s.Notifications {
					sw.Justify(ui.End)
				}
				sw.Children(func() {
					ui.Box(c).Size(16, 16).Radius(8).Background(ui.Hex("#ffffff"))
				})
				if sw.Clicked() {
					cfg.Update(func(st *config.Settings) { st.Notifications = !st.Notifications })
				}
			})

			// 自动开始
			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "自动启动新加入的任务").FontSize(12).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "新建任务确认后自动进入调度队列立即开始").FontSize(11).TextColor(pal.TextSecondary)
				})

				swBg := pal.CardBorder
				if s.AutoStart {
					swBg = pal.Accent
				}
				sw := ui.Row(c).Size(36, 20).Radius(10).Background(swBg).Padding(2, 2).AlignItems(ui.Center)
				if s.AutoStart {
					sw.Justify(ui.End)
				}
				sw.Children(func() {
					ui.Box(c).Size(16, 16).Radius(8).Background(ui.Hex("#ffffff"))
				})
				if sw.Clicked() {
					cfg.Update(func(st *config.Settings) { st.AutoStart = !st.AutoStart })
				}
			})
		})
	})
}

// renderSettingsDownloader 渲染下载与引擎设置
func (app *AppUI) renderSettingsDownloader(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(14).Children(func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "CONCURRENCY 并发控制").FontSize(10).Bold().TextColor(pal.TextMuted)

			// 并发数行
			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "最大并发下载任务数").FontSize(12).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "全局同时处于活动下载状态的任务上限限制").FontSize(11).TextColor(pal.TextSecondary)
				})

				limits := []int{1, 2, 3, 4, 6}
				ui.Row(c).Gap(4).Children(func() {
					for _, limit := range limits {
						isSel := s.MaxConcurrent == limit
						bg := pal.CardBg
						textC := pal.TextSecondary
						borderC := pal.CardBorder
						if isSel {
							bg = pal.AccentLight
							textC = pal.Accent
							borderC = pal.Accent
						}
						label := fmt.Sprintf("%d 个", limit)
						btn := ui.Row(c).Height(26).Padding(0, 8).Radius(5).Background(bg).BorderWidth(1).BorderColor(borderC).AlignItems(ui.Center)
						btn.Children(func() {
							ui.Text(c, label).FontSize(11).Bold().TextColor(textC)
						})
						if btn.Clicked() {
							cfg.Update(func(st *config.Settings) { st.MaxConcurrent = limit })
						}
					}
				})
			})
		})

		// 核心引擎状态
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "CORE BINARIES 核心组件就绪状态").FontSize(10).Bold().TextColor(pal.TextMuted)

			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(6).Grow(1).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconCheckCircle).FontSize(13).TextColor(pal.Success)
						ui.Text(c, "yt-dlp 多站点媒体提取核心: 已就绪 (v2026.08.01)").FontSize(11).Bold().TextColor(pal.TextPrimary)
					})
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconCheckCircle).FontSize(13).TextColor(pal.Success)
						ui.Text(c, "ffmpeg DASH 音视频自动 Muxing 转码合并管道: 就绪").FontSize(11).TextColor(pal.TextSecondary)
					})
				})
			})
		})
	})
}

// renderSettingsBrowser 渲染嗅探浏览器设置
func (app *AppUI) renderSettingsBrowser(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(14).Children(func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "SNIFFER & WEBVIEW 专用浏览器配置").FontSize(10).Bold().TextColor(pal.TextMuted)

			// Chrome 顶栏地址栏开关
			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "页面顶部 Chrome 风格 Omnibox 地址栏").FontSize(12).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "在专用浏览器顶部悬浮地址输入框与后退/前进/刷新控制条").FontSize(11).TextColor(pal.TextSecondary)
				})

				swBg := pal.CardBorder
				if s.InjectedOmnibox {
					swBg = pal.Accent
				}
				sw := ui.Row(c).Size(36, 20).Radius(10).Background(swBg).Padding(2, 2).AlignItems(ui.Center)
				if s.InjectedOmnibox {
					sw.Justify(ui.End)
				}
				sw.Children(func() {
					ui.Box(c).Size(16, 16).Radius(8).Background(ui.Hex("#ffffff"))
				})
				if sw.Clicked() {
					cfg.Update(func(st *config.Settings) { st.InjectedOmnibox = !st.InjectedOmnibox })
				}
			})

			// Chrome 131 桌面 UA 伪装
			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "桌面版 Chrome 131 User-Agent 伪装").FontSize(12).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "避免 Bilibili、YouTube 等视频站点提示浏览器版本过低").FontSize(11).TextColor(pal.TextSecondary)
				})

				swBg := pal.CardBorder
				if s.UserAgentSpoof {
					swBg = pal.Accent
				}
				sw := ui.Row(c).Size(36, 20).Radius(10).Background(swBg).Padding(2, 2).AlignItems(ui.Center)
				if s.UserAgentSpoof {
					sw.Justify(ui.End)
				}
				sw.Children(func() {
					ui.Box(c).Size(16, 16).Radius(8).Background(ui.Hex("#ffffff"))
				})
				if sw.Clicked() {
					cfg.Update(func(st *config.Settings) { st.UserAgentSpoof = !st.UserAgentSpoof })
				}
			})

			// 播放页深度自动嗅探
			ui.Row(c).Width(508).Padding(10, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "播放页流媒体深度感知").FontSize(12).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "浏览视频时自动检测 m3u8/mp4 及剧集列表并提示下载").FontSize(11).TextColor(pal.TextSecondary)
				})

				swBg := pal.CardBorder
				if s.AutoDetectStream {
					swBg = pal.Accent
				}
				sw := ui.Row(c).Size(36, 20).Radius(10).Background(swBg).Padding(2, 2).AlignItems(ui.Center)
				if s.AutoDetectStream {
					sw.Justify(ui.End)
				}
				sw.Children(func() {
					ui.Box(c).Size(16, 16).Radius(8).Background(ui.Hex("#ffffff"))
				})
				if sw.Clicked() {
					cfg.Update(func(st *config.Settings) { st.AutoDetectStream = !st.AutoDetectStream })
				}
			})
		})
	})
}

// renderSettingsStorage 渲染存储与目录设置
func (app *AppUI) renderSettingsStorage(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(14).Children(func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "STORAGE PATH 下载文件存放位置").FontSize(10).Bold().TextColor(pal.TextMuted)

			ui.Column(c).Width(508).Padding(12, 14).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).Gap(10).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconFolder).FontSize(14).TextColor(pal.Accent)
					ui.Box(c).Width(6)
					ui.Text(c, s.DownloadDir).FontSize(11).TextColor(pal.TextPrimary).SingleLine().Grow(1).MinWidth(0)
				})

				ui.Row(c).Gap(8).Children(func() {
					changeBtn := ui.Row(c).Height(26).Padding(0, 10).Radius(5).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
					changeBtn.Children(func() {
						ui.Text(c, "更改保存目录…").FontSize(11).TextColor(pal.TextPrimary)
					})
					if changeBtn.Clicked() {
						go func() {
							paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
								Title:       "选择媒体下载保存目录",
								Directory:   true,
								DefaultPath: s.DownloadDir,
							})
							if err == nil && len(paths) > 0 {
								cfg.Update(func(st *config.Settings) {
									st.DownloadDir = paths[0]
								})
							}
						}()
					}

					finderBtn := ui.Row(c).Height(26).Padding(0, 10).Radius(5).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
					finderBtn.Children(func() {
						ui.Text(c, "在 Finder 中显示").FontSize(11).TextColor(pal.TextSecondary)
					})
					if finderBtn.Clicked() {
						_ = exec.Command("open", s.DownloadDir).Start()
					}
				})
			})
		})
	})
}

// renderSettingsAbout 渲染关于与配置文件
func (app *AppUI) renderSettingsAbout(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(14).Children(func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "ABOUT MEDIAGO 应用与配置文件").FontSize(10).Bold().TextColor(pal.TextMuted)

			ui.Column(c).Width(508).Padding(12, 14).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).Gap(8).Children(func() {
				ui.Text(c, "MediaGo · 纯 Go 原生高性能媒体下载管理器").FontSize(13).Bold().TextColor(pal.TextPrimary)
				ui.Text(c, "基于 MyGo 原生 GPU 渲染框架构建 · 极简极速 · 零 Webview 纯原生 UI").FontSize(11).TextColor(pal.TextSecondary)

				ui.Box(c).Height(4)
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "配置文件路径:").FontSize(11).TextColor(pal.TextMuted)
					ui.Text(c, cfg.Path()).FontSize(11).TextColor(pal.TextSecondary)
				})

				ui.Row(c).Gap(8).Children(func() {
					openCfgBtn := ui.Row(c).Height(26).Padding(0, 10).Radius(5).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
					openCfgBtn.Children(func() {
						ui.Text(c, "在系统编辑器中打开配置文件 (Open Config)").FontSize(11).TextColor(pal.Accent)
					})
					if openCfgBtn.Clicked() {
						_ = exec.Command("open", cfg.Path()).Start()
					}
				})
			})
		})

		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "KEYBOARD SHORTCUTS 核心快捷键").FontSize(10).Bold().TextColor(pal.TextMuted)
			keys := [][2]string{
				{"新建下载任务", "⌘N"},
				{"打开网址启动器 (Raycast 风格)", "⌘B"},
				{"打开偏好设置", "⌘,"},
				{"快捷键速查", "⌘/"},
				{"退出 / 关闭浮层", "Esc"},
			}
			ui.Row(c).Width(508).Padding(8, 12).Radius(8).Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).Children(func() {
				ui.Column(c).Gap(4).Grow(1).Children(func() {
					for _, k := range keys {
						ui.Row(c).MinHeight(22).AlignItems(ui.Center).Children(func() {
							ui.Text(c, k[0]).FontSize(11).Grow(1).TextColor(pal.TextSecondary)
							ui.Text(c, k[1]).FontSize(10).Bold().Padding(1, 5).Radius(4).Background(pal.CardBorder).TextColor(pal.TextPrimary)
						})
					}
				})
			})
		})
	})
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
