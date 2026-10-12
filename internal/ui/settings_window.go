package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mediago/internal/config"
	"mediago/internal/discovery"
)

func openDirectory(dirPath string) {
	_ = os.MkdirAll(dirPath, 0755)
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("explorer", dirPath).Start()
	case "darwin":
		_ = exec.Command("open", dirPath).Start()
	default:
		_ = exec.Command("xdg-open", dirPath).Start()
	}
}

// SettingsWindow 管理独立的 macOS 原生偏好设置视窗 (对标 OrbStack / macOS 原生规范)
type SettingsWindow struct {
	mu        sync.Mutex
	win       *mygo.Window
	activeTab string // "general", "downloader", "storage", "about"
}

// NewSettingsWindow 创建设置窗口管理器
func NewSettingsWindow() *SettingsWindow {
	sw := &SettingsWindow{
		activeTab: "general",
	}

	// 监听配置变动，当外部或菜单栏变更时自动重绘设置窗口
	config.Global().OnChange(func(_ config.Settings) {
		sw.refresh()
	})

	// 监听工具安装器状态，当下载/解压进度变动时自动重绘设置视窗
	discovery.DefaultInstaller().OnProgress(func(_ discovery.InstallProgress) {
		sw.refresh()
	})

	return sw
}

func (sw *SettingsWindow) refresh() {
	sw.mu.Lock()
	w := sw.win
	sw.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

// Open 打开或聚焦独立的 macOS 原生设置窗口
func (sw *SettingsWindow) Open(initialTab ...string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if len(initialTab) > 0 && initialTab[0] != "" {
		sw.activeTab = initialTab[0]
	} else if envTab := os.Getenv("SHOW_SETTINGS_TAB"); envTab != "" && sw.win == nil {
		sw.activeTab = envTab
	}

	if sw.win != nil {
		sw.win.Show()
		sw.win.Focus()
		if sw.win != nil {
			sw.win.Invalidate()
		}
		return
	}

	if sw.activeTab == "" {
		sw.activeTab = "general"
	}

	win := mygo.NewWindow(mygo.WindowOptions{
		Title:          "偏好设置",
		Width:          680,
		Height:         480,
		MinWidth:       620,
		MinHeight:      420,
		StateKey:       "mediago_settings_window",
		TitleBarStyle:  mygo.TitleBarHiddenInset,
		TitleBarHeight: 44,
		Vibrancy:       mygo.VibrancySidebar,
		Content:        ui.View(sw.View),
	})

	sw.win = win

	win.OnClosed(func() {
		sw.mu.Lock()
		sw.win = nil
		sw.mu.Unlock()
	})
}

// Close 关闭设置窗口
func (sw *SettingsWindow) Close() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.win != nil {
		sw.win.Close()
	}
}

// View 渲染原生设置窗口内容 (双分栏: 左侧纯中文导航，右侧大方卡片内容区)
func (sw *SettingsWindow) View(c *ui.Context) {
	theme := c.Theme()
	pal := getPalette(theme)
	cfg := config.Global()
	s := cfg.Get()

	// 键盘快捷键: Cmd+W 或 Esc 快速关闭设置窗口
	if c.Shortcut(ui.Cmd, ui.KeyW) || c.Shortcut(0, ui.KeyEscape) {
		sw.Close()
		return
	}

	ui.Row(c).Fill().AlignItems(ui.Stretch).Background(pal.CardBg).Children(func() {
		// =====================================
		// 1. 左侧纯中文分类导航栏 (宽 180px, 顶格贴顶)
		// =====================================
		ui.Column(c).Width(180).Shrink(0).Padding(44, 12, 16, 12).Gap(4).Background(pal.SidebarBg).Children(func() {
			navTabs := []struct {
				id    string
				label string
				icon  *ui.SVG
			}{
				{"general", "通用", iconSettings},
				{"downloader", "下载", iconDownload},
				{"storage", "存储", iconFolder},
				{"about", "关于", iconInfo},
			}

			for _, tab := range navTabs {
				t := tab
				isSel := sw.activeTab == t.id

				// 选中项使用与主窗口一致的 pal.Accent
				bg := ui.Transparent
				textC := pal.TextSecondary
				iconC := pal.TextSecondary
				if isSel {
					bg = pal.Accent
					textC = ui.Hex("#ffffff")
					iconC = ui.Hex("#ffffff")
				}

				item := ui.Row(c).Height(34).Width(156).Padding(0, 12).Gap(10).Radius(8).
					Background(bg).AlignItems(ui.Center)

				item.Children(func() {
					ui.Icon(c, t.icon).FontSize(15).TextColor(iconC)
					ui.Text(c, t.label).FontSize(13).TextColor(textC)
				})

				if item.Clicked() {
					sw.activeTab = t.id
					sw.refresh()
				}
			}

			ui.Spacer(c)
		})

		// 垂直中线分割线
		ui.Box(c).Width(1).Shrink(0).Background(pal.CardBorder)

		// =====================================
		// 2. 右侧大方舒展的设置详情区 (顶格贴顶，模拟原生设置)
		// =====================================
		ui.Scroll(c).Vertical().Fill().Children(func() {
			ui.Column(c).Padding(30, 20, 24, 20).Fill().Gap(20).Children(func() {
				// 顶部标题 (对标原生: 15px 加粗紧凑)
				titleText := "通用"
				switch sw.activeTab {
				case "downloader":
					titleText = "下载"
				case "storage":
					titleText = "存储"
				case "about":
					titleText = "关于"
				}
				ui.Text(c, titleText).FontSize(15).Bold().TextColor(pal.TextPrimary)

				// 分页面渲染
				switch sw.activeTab {
				case "general":
					sw.renderGeneralTab(c, theme, pal, cfg, s)
				case "downloader":
					sw.renderDownloaderTab(c, theme, pal, cfg, s)
				case "storage":
					sw.renderStorageTab(c, theme, pal, cfg, s)
				case "about":
					sw.renderAboutTab(c, theme, pal, cfg, s)
				default:
					sw.renderGeneralTab(c, theme, pal, cfg, s)
				}
			})
		})
	})
}

// cardBg 原生无边框半透明底板
func (sw *SettingsWindow) cardBg(theme *ui.Theme) ui.Color {
	if theme != nil && !theme.Dark {
		return ui.RGBA(0, 0, 0, 0.04)
	}
	return ui.RGBA(255, 255, 255, 0.05)
}

// cardSep 卡片内细腻水平分割线
func (sw *SettingsWindow) cardSep(theme *ui.Theme) ui.Color {
	if theme != nil && !theme.Dark {
		return ui.RGBA(0, 0, 0, 0.04)
	}
	return ui.RGBA(255, 255, 255, 0.04)
}

// renderToggle 原生 macOS 规范紧凑开关 (38x22)
func (sw *SettingsWindow) renderToggle(c *ui.Context, theme *ui.Theme, pal Palette, checked bool, onToggle func()) {
	bg := ui.RGBA(255, 255, 255, 0.16)
	if theme != nil && !theme.Dark {
		bg = ui.RGBA(0, 0, 0, 0.14)
	}
	if checked {
		bg = pal.Accent
	}
	btn := ui.Row(c).Size(38, 22).Radius(11).Background(bg).Padding(2, 2).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	if checked {
		btn.Justify(ui.End)
	} else {
		btn.Justify(ui.Start)
	}
	btn.Children(func() {
		ui.Box(c).Size(18, 18).Radius(9).Background(ui.Hex("#ffffff"))
	})
	if btn.Clicked() {
		onToggle()
	}
}

// renderGeneralTab 渲染通用设置 (外观、通知、常规)
func (sw *SettingsWindow) renderGeneralTab(c *ui.Context, theme *ui.Theme, pal Palette, cfg *config.Config, s config.Settings) {
	cardBg := sw.cardBg(theme)
	sepColor := sw.cardSep(theme)

	ui.Column(c).Fill().Gap(20).Children(func() {
		// 外观分组 (Section 名置于卡片上方，次级优雅字重)
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "界面外观").FontSize(12).TextColor(pal.TextSecondary)

			card := ui.Row(c).Fill().Padding(10, 16).Radius(10).Background(cardBg).AlignItems(ui.Center)
			card.Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "界面色彩外观").FontSize(13).TextColor(pal.TextPrimary)
					ui.Text(c, "跟随系统偏好或指定浅色、深色模式").FontSize(11).TextColor(pal.TextSecondary)
				})

				themeOpt := "跟随系统"
				switch s.Theme {
				case "light":
					themeOpt = "浅色模式"
				case "dark":
					themeOpt = "深色模式"
				}
				themeSel := ui.Select(c, &themeOpt, []string{"跟随系统", "浅色模式", "深色模式"}).Width(120)
				if themeSel.Changed() {
					newT := "system"
					switch themeOpt {
					case "浅色模式":
						newT = "light"
					case "深色模式":
						newT = "dark"
					}
					cfg.Update(func(st *config.Settings) { st.Theme = newT })
				}
			})
		})

		// 通知与行为分组
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "通知与行为").FontSize(12).TextColor(pal.TextSecondary)

			groupCard := ui.Column(c).Fill().Radius(10).Background(cardBg)
			groupCard.Children(func() {
				// 行 1: 通知开关
				r1 := ui.Row(c).Fill().Padding(10, 16).AlignItems(ui.Center)
				r1.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "任务完成通知").FontSize(13).TextColor(pal.TextPrimary)
						ui.Text(c, "视频下载或转码完成时显示系统通知").FontSize(11).TextColor(pal.TextSecondary)
					})

					sw.renderToggle(c, theme, pal, s.Notifications, func() {
						cfg.Update(func(st *config.Settings) { st.Notifications = !st.Notifications })
					})
				})

				ui.Box(c).Height(1).Fill().Background(sepColor)

				// 行 2: 自动开始开关
				r2 := ui.Row(c).Fill().Padding(10, 16).AlignItems(ui.Center)
				r2.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "自动开始新加入的任务").FontSize(13).TextColor(pal.TextPrimary)
						ui.Text(c, "解析确认后任务立即加入并发调度并开始下载").FontSize(11).TextColor(pal.TextSecondary)
					})

					sw.renderToggle(c, theme, pal, s.AutoStart, func() {
						cfg.Update(func(st *config.Settings) { st.AutoStart = !st.AutoStart })
					})
				})
			})
		})
	})
}

// renderDownloaderTab 渲染下载设置
func (sw *SettingsWindow) renderDownloaderTab(c *ui.Context, theme *ui.Theme, pal Palette, cfg *config.Config, s config.Settings) {
	cardBg := sw.cardBg(theme)
	sepColor := sw.cardSep(theme)

	ui.Column(c).Fill().Gap(20).Children(func() {
		// 并发控制
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "调度控制").FontSize(12).TextColor(pal.TextSecondary)

			groupCard := ui.Column(c).Fill().Radius(10).Background(cardBg)
			groupCard.Children(func() {
				// 行 1: 并发数选择 Select
				r1 := ui.Row(c).Fill().Padding(10, 16).AlignItems(ui.Center)
				r1.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "最大并发下载任务数").FontSize(13).TextColor(pal.TextPrimary)
						ui.Text(c, "同时进行下载的任务上限限制").FontSize(11).TextColor(pal.TextSecondary)
					})

					concurOpt := fmt.Sprintf("%d 个任务", s.MaxConcurrent)
					concurSel := ui.Select(c, &concurOpt, []string{"1 个任务", "2 个任务", "3 个任务", "4 个任务", "6 个任务"}).Width(120)
					if concurSel.Changed() {
						var n int
						fmt.Sscanf(concurOpt, "%d", &n)
						if n > 0 {
							cfg.Update(func(st *config.Settings) { st.MaxConcurrent = n })
						}
					}
				})

				ui.Box(c).Height(1).Fill().Background(sepColor)

				// 行 2: 优先清晰度选择 Select
				r2 := ui.Row(c).Fill().Padding(10, 16).AlignItems(ui.Center)
				r2.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "首选视频画质").FontSize(13).TextColor(pal.TextPrimary)
						ui.Text(c, "解析多规格视频时默认优先选择的清晰度").FontSize(11).TextColor(pal.TextSecondary)
					})

					qualOpt := "1080P 超清"
					switch s.PreferredQuality {
					case "best":
						qualOpt = "最佳画质"
					case "720p":
						qualOpt = "720P 高清"
					}
					qualSel := ui.Select(c, &qualOpt, []string{"最佳画质", "1080P 超清", "720P 高清"}).Width(120)
					if qualSel.Changed() {
						newQ := "1080p"
						switch qualOpt {
						case "最佳画质":
							newQ = "best"
						case "720P 高清":
							newQ = "720p"
						}
						cfg.Update(func(st *config.Settings) { st.PreferredQuality = newQ })
					}
				})
			})
		})
	})
}

// renderStorageTab 渲染存储目录设置
func (sw *SettingsWindow) renderStorageTab(c *ui.Context, theme *ui.Theme, pal Palette, cfg *config.Config, s config.Settings) {
	cardBg := sw.cardBg(theme)

	ui.Column(c).Fill().Gap(20).Children(func() {
		// 存储路径分组
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "默认保存位置").FontSize(12).TextColor(pal.TextSecondary)

			pathCard := ui.Column(c).Fill().Padding(12, 16).Gap(10).Radius(10).Background(cardBg)
			pathCard.Children(func() {
				ui.Text(c, s.DownloadDir).FontSize(13).TextColor(pal.TextPrimary)

				ui.Row(c).Gap(8).Children(func() {
					chooseBtn := ui.Row(c).Height(26).Padding(0, 10).Radius(6).
						Background(pal.CardBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
					chooseBtn.Children(func() {
						ui.Text(c, "更改目录…").FontSize(11).TextColor(pal.TextPrimary)
					})
					if chooseBtn.Clicked() {
						paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
							Title:       "选择媒体下载保存目录",
							Directory:   true,
							DefaultPath: s.DownloadDir,
						})
						if err == nil && len(paths) > 0 {
							cfg.Update(func(st *config.Settings) { st.DownloadDir = paths[0] })
						}
					}

					finderBtn := ui.Row(c).Height(26).Padding(0, 10).Radius(6).
						Background(pal.CardBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
					finderBtn.Children(func() {
						ui.Text(c, "在访达中显示").FontSize(11).TextColor(pal.TextSecondary)
					})
					if finderBtn.Clicked() {
						_ = exec.Command("open", s.DownloadDir).Start()
					}
				})
			})
		})

		// 配置文件管理
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "配置文件").FontSize(12).TextColor(pal.TextSecondary)

			cfgCard := ui.Row(c).Fill().Padding(12, 16).Radius(10).Background(cardBg).AlignItems(ui.Center)
			cfgCard.Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "本地偏好设置文件").FontSize(13).TextColor(pal.TextPrimary)
					ui.Text(c, "~/.mediago/mediago.jsonc").FontSize(11).TextColor(pal.TextSecondary)
				})

				openBtn := ui.Row(c).Height(26).Padding(0, 10).Radius(6).
					Background(pal.CardBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				openBtn.Children(func() {
					ui.Text(c, "在编辑器中打开").FontSize(11).TextColor(pal.TextSecondary)
				})
				if openBtn.Clicked() {
					home, _ := filepath.Abs(filepath.Join(s.DownloadDir, "..", "..", ".mediago", "mediago.jsonc"))
					_ = exec.Command("open", "-t", home).Start()
				}
			})
		})
	})
}

// renderAboutTab 渲染关于页面
func (sw *SettingsWindow) renderAboutTab(c *ui.Context, theme *ui.Theme, pal Palette, cfg *config.Config, s config.Settings) {
	cardBg := sw.cardBg(theme)
	sepColor := sw.cardSep(theme)

	ui.Column(c).Fill().Gap(20).Children(func() {
		// 关于 MediaGo
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "关于应用").FontSize(12).TextColor(pal.TextSecondary)

			infoCard := ui.Row(c).Fill().Padding(12, 16).Radius(10).Background(cardBg).AlignItems(ui.Center)
			infoCard.Children(func() {
				ui.Column(c).Gap(3).Grow(1).Children(func() {
					ui.Text(c, "MediaGo").FontSize(14).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "版本 1.0.0 · 原生高性能流媒体提取与下载工具").FontSize(11).TextColor(pal.TextSecondary)
				})

				ghBtn := ui.Row(c).Height(26).Padding(0, 10).Radius(6).
					Background(pal.CardBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				ghBtn.Children(func() {
					ui.Text(c, "GitHub 主页").FontSize(11).TextColor(pal.TextPrimary)
				})
				if ghBtn.Clicked() {
					_ = exec.Command("open", "https://github.com/jyuuroku16/mediago").Start()
				}
			})
		})

		// 核心外部工具套件 (严格对齐下方运行时环境的单行规格)
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Row(c).Fill().AlignItems(ui.Center).Children(func() {
				ui.Text(c, "流媒体核心工具").FontSize(12).TextColor(pal.TextSecondary).Grow(1)

				openDirBtn := ui.Row(c).Height(22).Padding(0, 8).Radius(4).
					Background(pal.CardBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
				openDirBtn.Children(func() {
					ui.Text(c, "打开工具目录").FontSize(11).TextColor(pal.TextSecondary)
				})
				if openDirBtn.Clicked() {
					openDirectory(discovery.GetToolsDir())
				}
			})

			toolsCard := ui.Column(c).Fill().Radius(10).Background(cardBg)
			toolsCard.Children(func() {
				installer := discovery.DefaultInstaller()
				instState := installer.State()
				isBusy := instState.Status == discovery.StatusDownloading || instState.Status == discovery.StatusExtracting

				// 若处于安装状态，在卡片顶端显示细腻的微进度条
				if isBusy {
					ui.Column(c).Fill().Padding(8, 16).Gap(4).Background(ui.RGBA(0, 122, 255, 0.08)).Children(func() {
						ui.Row(c).Fill().AlignItems(ui.Center).Children(func() {
							ui.Text(c, instState.Message).FontSize(11).Bold().TextColor(pal.Accent).Grow(1)
							ui.Text(c, fmt.Sprintf("%.0f%%", instState.Percent*100)).FontSize(11).Bold().TextColor(pal.Accent)
						})
						ui.Row(c).Fill().Height(3).Radius(1.5).Background(ui.RGBA(127, 127, 127, 0.2)).Children(func() {
							pct := float32(max(0.02, min(1.0, instState.Percent)))
							ui.Box(c).Grow(pct).Height(3).Radius(1.5).Background(pal.Accent)
							if pct < 1.0 {
								ui.Spacer(c)
							}
						})
					})
					ui.Box(c).Height(1).Fill().Background(sepColor)
				} else if instState.Status == discovery.StatusFailed && instState.Error != "" {
					ui.Row(c).Fill().Padding(8, 16).Background(ui.RGBA(255, 69, 58, 0.1)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "安装出错: "+instState.Error).FontSize(11).TextColor(ui.Hex("#ff453a")).Grow(1)
					})
					ui.Box(c).Height(1).Fill().Background(sepColor)
				}

				ytVer, ffVer := discovery.GetToolVersions()
				items := []struct {
					key, name, ver string
				}{
					{"ffmpeg", "ffmpeg", ffVer},
					{"yt-dlp", "yt-dlp", ytVer},
				}

				for idx, it := range items {
					item := it
					installed := item.ver != "未安装"

					ui.Row(c).Fill().Height(36).Padding(0, 16).AlignItems(ui.Center).Children(func() {
						// 左侧名称：完全对齐下方的字号与主文本颜色
						ui.Text(c, item.name).FontSize(12).TextColor(pal.TextPrimary).Grow(1)

						// 右侧区域：版本徽标 + 微操作按钮 (统一高度 20，对齐基准线)
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							// 版本 Badge (与下方运行时环境完全一致)
							badge := ui.Row(c).Height(20).Padding(0, 8).Radius(4).
								Background(pal.CardBg).AlignItems(ui.Center)
							badge.Children(func() {
								ui.Text(c, item.ver).FontSize(11).TextColor(pal.TextSecondary)
							})

							// 操作微按钮 (高度 20，低饱和度胶囊)
							btnLabel := "更新"
							btnBg := pal.CardBg
							btnTextColor := pal.TextSecondary
							if !installed {
								btnLabel = "安装"
								btnBg = pal.Accent
								btnTextColor = ui.Hex("#ffffff")
							}
							if isBusy {
								btnLabel = "..."
							}

							actionBtn := ui.Row(c).Height(20).Padding(0, 8).Radius(4).
								Background(btnBg).AlignItems(ui.Center).Cursor(ui.CursorPointer)
							actionBtn.Children(func() {
								ui.Text(c, btnLabel).FontSize(11).TextColor(btnTextColor)
							})

							if !isBusy && actionBtn.Clicked() {
								go func(toolKey string) {
									_ = discovery.DefaultInstaller().InstallTool(context.Background(), toolKey)
								}(item.key)
							}
						})
					})

					if idx < len(items)-1 {
						ui.Box(c).Height(1).Fill().Background(sepColor)
					}
				}
			})
		})

		// 运行时与框架环境
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "运行时环境").FontSize(12).TextColor(pal.TextSecondary)

			envCard := ui.Column(c).Fill().Radius(10).Background(cardBg)
			envCard.Children(func() {
				envItems := []struct{ name, ver string }{
					{"MyGo", "v0.3.7 (Metal GPU / Direct3D)"},
					{"Go", runtime.Version()},
				}
				for idx, it := range envItems {
					row := it
					ui.Row(c).Fill().Height(36).Padding(0, 16).AlignItems(ui.Center).Children(func() {
						ui.Text(c, row.name).FontSize(12).TextColor(pal.TextPrimary).Grow(1)
						badge := ui.Row(c).Height(20).Padding(0, 8).Radius(4).
							Background(pal.CardBg).AlignItems(ui.Center)
						badge.Children(func() {
							ui.Text(c, row.ver).FontSize(11).TextColor(pal.TextSecondary)
						})
					})
					if idx < len(envItems)-1 {
						ui.Box(c).Height(1).Fill().Background(sepColor)
					}
				}
			})
		})

		// 键盘快捷键对照
		ui.Column(c).Fill().Gap(8).Children(func() {
			ui.Text(c, "常用快捷键").FontSize(12).TextColor(pal.TextSecondary)

			kbCard := ui.Column(c).Fill().Radius(10).Background(cardBg)
			kbCard.Children(func() {
				shortcuts := []struct{ key, desc string }{
					{"⌘ N", "新建下载任务"},
					{"⌘ B", "专用浏览器"},
					{"⌘ ,", "打开偏好设置"},
					{"⌘ W", "关闭当前窗口"},
					{"Esc", "退出对话框 / 启动器"},
				}
				for idx, sc := range shortcuts {
					item := sc
					ui.Row(c).Fill().Height(36).Padding(0, 16).AlignItems(ui.Center).Children(func() {
						ui.Text(c, item.desc).FontSize(12).TextColor(pal.TextSecondary).Grow(1)
						badge := ui.Row(c).Height(20).Padding(0, 6).Radius(4).
							Background(pal.CardBg).AlignItems(ui.Center)
						badge.Children(func() {
							ui.Text(c, item.key).FontSize(11).TextColor(pal.TextPrimary)
						})
					})
					if idx < len(shortcuts)-1 {
						ui.Box(c).Height(1).Fill().Background(sepColor)
					}
				}
			})
		})
	})
}
