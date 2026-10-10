package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/google/uuid"

	"mediago/internal/browser"
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
}

// NewAppUI 创建主界面控制器
func NewAppUI(
	st *storage.Storage,
	sc *engine.Scheduler,
	bm *browser.Manager,
	lib *library.Library,
) *AppUI {
	app := &AppUI{
		storage:        st,
		scheduler:      sc,
		browserMgr:     bm,
		library:        lib,
		extractor:      discovery.NewYtDlpExtractor(),
		currentTab:     "downloading",
		showModal:      false,
		selectedFormat: 0,
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

	// 全局快捷键处理: ⌘N 新建下载, ⌘B 打开浏览器, Esc 关闭弹窗
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		app.showModal = true
		app.modalURL = ""
		app.candidate = nil
	}
	if c.Shortcut(ui.Cmd, ui.KeyB) {
		app.browserMgr.Open("https://www.bilibili.com")
	}
	if app.showModal && c.Shortcut(0, ui.KeyEscape) {
		app.showModal = false
	}

	// 4. Spotlight / Raycast 风格的悬浮模态弹窗
	if app.showModal {
		app.renderModalDialog(c, pal)
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

		// 底部快捷卡片: 专用浏览器
		ui.Column(c).Padding(10, 12, 14, 12).Children(func() {
			browserCard := ui.Row(c).Height(34).Padding(0, 10).Gap(8).Radius(8).Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
			browserCard.Children(func() {
				ui.Icon(c, iconGlobe).FontSize(14).TextColor(pal.Accent)
				ui.Text(c, "专用浏览器").FontSize(12).Bold().TextColor(pal.TextPrimary)
				ui.Spacer(c)
				ui.Box(c).Size(6, 6).Radius(3).Background(pal.Success)
			})
			if browserCard.Clicked() {
				app.browserMgr.Open("https://www.bilibili.com")
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
				app.browserMgr.Open("https://www.bilibili.com")
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
			if t.Engine == model.EngineHLS {
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

// renderModalDialog 渲染 Spotlight / Raycast 风格的悬浮模态框
func (app *AppUI) renderModalDialog(c *ui.Context, pal Palette) {
	ui.Box(c).Absolute().Fill().Center().Background(ui.RGBA(0, 0, 0, 0.45)).Children(func() {
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
				ui.Column(c).Padding(12).Radius(8).Background(pal.InputBg).Gap(8).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconFilm).FontSize(14).TextColor(pal.Accent)
						ui.Text(c, app.candidate.Title).FontSize(13).Bold().TextColor(pal.TextPrimary).SingleLine().Grow(1).MinWidth(0)
					})

					if len(app.candidate.Formats) > 0 {
						ui.Text(c, "可选画质与格式:").FontSize(11).TextColor(pal.TextSecondary)
						ui.Row(c).Gap(6).Children(func() {
							for idx, f := range app.candidate.Formats {
								isSel := app.selectedFormat == idx
								fBg := pal.CardBg
								fBorder := pal.CardBorder
								fTc := pal.TextPrimary
								if isSel {
									fBg = pal.AccentLight
									fBorder = pal.Accent
									fTc = pal.Accent
								}
								fmtBtn := ui.Row(c).Padding(4, 10).Radius(6).Background(fBg).BorderWidth(1).BorderColor(fBorder).AlignItems(ui.Center)
								fmtLabel := fmt.Sprintf("%s (%s)", f.Resolution, f.Ext)
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

			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				cancelBtn := ui.Button(c, "取消").FontSize(12)
				if cancelBtn.Clicked() {
					app.showModal = false
				}

				if app.candidate != nil {
					confirmBtn := ui.Row(c).Height(32).Padding(0, 16).Radius(7).Background(pal.Accent).AlignItems(ui.Center)
					confirmBtn.Children(func() {
						ui.Text(c, "立即开始下载").FontSize(12).Bold().TextColor(ui.Hex("#ffffff"))
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
	cleanTitle := sanitizeFilename(app.candidate.Title)
	if cleanTitle == "" {
		cleanTitle = "video_" + time.Now().Format("20060102_150405")
	}

	engineType := model.EngineHTTPRange
	if fmtObj.Protocol == "m3u8" || strings.HasSuffix(fmtObj.URL, ".m3u8") {
		engineType = model.EngineHLS
	}

	filename := fmt.Sprintf("%s.%s", cleanTitle, fmtObj.Ext)
	task := &model.Task{
		ID:             uuid.New().String(),
		Title:          app.candidate.Title,
		SourceURL:      app.candidate.SourceURL,
		DownloadURL:    fmtObj.URL,
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
