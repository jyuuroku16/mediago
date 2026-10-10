package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mediago/internal/config"
)

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

	ui.Row(c).Fill().Background(pal.CardBg).Children(func() {
		// =====================================
		// 1. 左侧纯中文分类导航栏 (宽 180px)
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

				// 选中项使用参考图 5 的优雅高亮胶囊 (紫色/Accent)，白字白图标
				bg := ui.Transparent
				textC := pal.TextSecondary
				iconC := pal.TextSecondary
				if isSel {
					bg = ui.Hex("#9333ea") // 高级紫 (对标参考图)
					textC = ui.Hex("#ffffff")
					iconC = ui.Hex("#ffffff")
				}

				item := ui.Row(c).Height(34).Padding(0, 12).Gap(10).Radius(8).
					Background(bg).AlignItems(ui.Center)

				item.Children(func() {
					ui.Icon(c, t.icon).FontSize(15).TextColor(iconC)
					ui.Text(c, t.label).FontSize(13).Bold().TextColor(textC)
				})

				if item.Clicked() {
					sw.activeTab = t.id
					sw.refresh()
				}
			}
		})

		// 垂直中线分割线 (显式定高，避免在 ui.Row 中抢占水平空间)
		ui.Box(c).Width(1).Height(480).Background(pal.CardBorder)

		// =====================================
		// 2. 右侧大方舒展的设置详情区 (宽 499)
		// =====================================
		ui.Column(c).Width(499).Shrink(0).Padding(36, 28, 24, 28).Gap(20).Children(func() {
			// 顶部大标题 (纯中文，无自绘关闭按钮，窗口左上角由 macOS 原生控制)
			titleText := "通用"
			switch sw.activeTab {
			case "downloader":
				titleText = "下载"
			case "storage":
				titleText = "存储"
			case "about":
				titleText = "关于"
			}
			ui.Text(c, titleText).FontSize(20).Bold().TextColor(pal.TextPrimary)

			// 分页面渲染
			switch sw.activeTab {
			case "general":
				sw.renderGeneralTab(c, pal, cfg, s)
			case "downloader":
				sw.renderDownloaderTab(c, pal, cfg, s)
			case "storage":
				sw.renderStorageTab(c, pal, cfg, s)
			case "about":
				sw.renderAboutTab(c, pal, cfg, s)
			default:
				sw.renderGeneralTab(c, pal, cfg, s)
			}
		})
	})
}

// renderGeneralTab 渲染通用设置 (外观、通知、常规)
func (sw *SettingsWindow) renderGeneralTab(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(18).Children(func() {
		// 外观分组
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "外观").FontSize(13).Bold().TextColor(pal.TextMuted)

			card := ui.Row(c).Width(443).Padding(12, 16).Radius(10).Background(pal.SidebarBg).
				BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
			card.Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "界面色彩外观").FontSize(13).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "跟随系统偏好或指定浅色、深色模式").FontSize(12).TextColor(pal.TextSecondary)
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
							bg = ui.Hex("#9333ea")
							textC = ui.Hex("#ffffff")
							borderC = ui.Hex("#9333ea")
						}
						btn := ui.Row(c).Height(28).Padding(0, 10).Radius(6).
							Background(bg).BorderWidth(1).BorderColor(borderC).AlignItems(ui.Center)
						btn.Children(func() {
							ui.Text(c, th.label).FontSize(12).Bold().TextColor(textC)
						})
						if btn.Clicked() {
							cfg.Update(func(st *config.Settings) { st.Theme = th.id })
						}
					}
				})
			})
		})

		// 行为与交互分组
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "通知与行为").FontSize(13).Bold().TextColor(pal.TextMuted)

			groupCard := ui.Column(c).Width(443).Radius(10).Background(pal.SidebarBg).
				BorderWidth(1).BorderColor(pal.CardBorder)
			groupCard.Children(func() {
				// 行 1: 通知开关
				r1 := ui.Row(c).Width(443).Padding(12, 16).AlignItems(ui.Center)
				r1.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "任务完成通知").FontSize(13).Bold().TextColor(pal.TextPrimary)
						ui.Text(c, "视频下载或转码完成时显示系统通知").FontSize(12).TextColor(pal.TextSecondary)
					})

					swBg := pal.CardBorder
					if s.Notifications {
						swBg = ui.Hex("#9333ea")
					}
					swBtn := ui.Row(c).Size(40, 22).Radius(11).Background(swBg).Padding(2, 2).AlignItems(ui.Center)
					if s.Notifications {
						swBtn.Justify(ui.End)
					}
					swBtn.Children(func() {
						ui.Box(c).Size(18, 18).Radius(9).Background(ui.Hex("#ffffff"))
					})
					if swBtn.Clicked() {
						cfg.Update(func(st *config.Settings) { st.Notifications = !st.Notifications })
					}
				})

				ui.Box(c).Height(1).Fill().Background(pal.CardBorder)

				// 行 2: 自动开始开关
				r2 := ui.Row(c).Width(443).Padding(12, 16).AlignItems(ui.Center)
				r2.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "自动开始新加入的任务").FontSize(13).Bold().TextColor(pal.TextPrimary)
						ui.Text(c, "解析确认后任务立即加入并发调度并开始下载").FontSize(12).TextColor(pal.TextSecondary)
					})

					swBg := pal.CardBorder
					if s.AutoStart {
						swBg = ui.Hex("#9333ea")
					}
					swBtn := ui.Row(c).Size(40, 22).Radius(11).Background(swBg).Padding(2, 2).AlignItems(ui.Center)
					if s.AutoStart {
						swBtn.Justify(ui.End)
					}
					swBtn.Children(func() {
						ui.Box(c).Size(18, 18).Radius(9).Background(ui.Hex("#ffffff"))
					})
					if swBtn.Clicked() {
						cfg.Update(func(st *config.Settings) { st.AutoStart = !st.AutoStart })
					}
				})
			})
		})
	})
}

// renderDownloaderTab 渲染下载设置
func (sw *SettingsWindow) renderDownloaderTab(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(18).Children(func() {
		// 并发控制
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "调度控制").FontSize(13).Bold().TextColor(pal.TextMuted)

			groupCard := ui.Column(c).Width(443).Radius(10).Background(pal.SidebarBg).
				BorderWidth(1).BorderColor(pal.CardBorder)
			groupCard.Children(func() {
				// 行 1: 并发数
				r1 := ui.Row(c).Width(443).Padding(12, 16).AlignItems(ui.Center)
				r1.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "最大并发下载任务数").FontSize(13).Bold().TextColor(pal.TextPrimary)
						ui.Text(c, "同时进行下载的任务上限").FontSize(12).TextColor(pal.TextSecondary)
					})

					limits := []int{1, 2, 3, 4, 6}
					ui.Row(c).Gap(4).Children(func() {
						for _, limit := range limits {
							isSel := s.MaxConcurrent == limit
							bg := pal.CardBg
							textC := pal.TextSecondary
							borderC := pal.CardBorder
							if isSel {
								bg = ui.Hex("#9333ea")
								textC = ui.Hex("#ffffff")
								borderC = ui.Hex("#9333ea")
							}
							label := fmt.Sprintf("%d", limit)
							btn := ui.Row(c).Height(28).Width(28).Radius(6).
								Background(bg).BorderWidth(1).BorderColor(borderC).
								Justify(ui.Center).AlignItems(ui.Center)
							btn.Children(func() {
								ui.Text(c, label).FontSize(12).Bold().TextColor(textC)
							})
							if btn.Clicked() {
								cfg.Update(func(st *config.Settings) { st.MaxConcurrent = limit })
							}
						}
					})
				})

				ui.Box(c).Height(1).Fill().Background(pal.CardBorder)

				// 行 2: 优先清晰度
				r2 := ui.Row(c).Width(443).Padding(12, 16).AlignItems(ui.Center)
				r2.Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "首选视频画质").FontSize(13).Bold().TextColor(pal.TextPrimary)
						ui.Text(c, "解析多规格视频时默认优先选择的清晰度").FontSize(12).TextColor(pal.TextSecondary)
					})

					qualities := []struct{ id, label string }{
						{"best", "最佳画质"},
						{"1080p", "1080P"},
						{"720p", "720P"},
					}
					ui.Row(c).Gap(4).Children(func() {
						for _, q := range qualities {
							isSel := s.PreferredQuality == q.id
							bg := pal.CardBg
							textC := pal.TextSecondary
							borderC := pal.CardBorder
							if isSel {
								bg = ui.Hex("#9333ea")
								textC = ui.Hex("#ffffff")
								borderC = ui.Hex("#9333ea")
							}
							btn := ui.Row(c).Height(28).Padding(0, 10).Radius(6).
								Background(bg).BorderWidth(1).BorderColor(borderC).AlignItems(ui.Center)
							btn.Children(func() {
								ui.Text(c, q.label).FontSize(12).Bold().TextColor(textC)
							})
							if btn.Clicked() {
								cfg.Update(func(st *config.Settings) { st.PreferredQuality = q.id })
							}
						}
					})
				})
			})
		})

		// 引擎组件状态
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "核心引擎").FontSize(13).Bold().TextColor(pal.TextMuted)

			statusCard := ui.Column(c).Width(443).Padding(12, 16).Gap(8).Radius(10).
				Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder)
			statusCard.Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconCheckCircle).FontSize(14).TextColor(pal.Success)
					ui.Text(c, "yt-dlp 多站点流媒体解析引擎: 已就绪").FontSize(12).TextColor(pal.TextPrimary)
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconCheckCircle).FontSize(14).TextColor(pal.Success)
					ui.Text(c, "ffmpeg DASH 音视频自动封装合成管道: 已就绪").FontSize(12).TextColor(pal.TextPrimary)
				})
			})
		})
	})
}

// renderStorageTab 渲染存储目录设置
func (sw *SettingsWindow) renderStorageTab(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(18).Children(func() {
		// 存储路径分组
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "默认保存位置").FontSize(13).Bold().TextColor(pal.TextMuted)

			pathCard := ui.Column(c).Width(443).Padding(14, 16).Gap(10).Radius(10).
				Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder)
			pathCard.Children(func() {
				ui.Text(c, s.DownloadDir).FontSize(13).Bold().TextColor(pal.TextPrimary)

				ui.Row(c).Gap(8).Children(func() {
					chooseBtn := ui.Row(c).Height(28).Padding(0, 12).Radius(6).
						Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
					chooseBtn.Children(func() {
						ui.Text(c, "更改目录…").FontSize(12).Bold().TextColor(pal.TextPrimary)
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

					finderBtn := ui.Row(c).Height(28).Padding(0, 12).Radius(6).
						Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
					finderBtn.Children(func() {
						ui.Text(c, "在访达中显示").FontSize(12).TextColor(pal.TextSecondary)
					})
					if finderBtn.Clicked() {
						_ = exec.Command("open", s.DownloadDir).Start()
					}
				})
			})
		})

		// 配置文件管理
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "配置文件").FontSize(13).Bold().TextColor(pal.TextMuted)

			cfgCard := ui.Row(c).Width(443).Padding(12, 16).Radius(10).
				Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
			cfgCard.Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "本地偏好设置文件").FontSize(13).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "~/.mediago/mediago.jsonc").FontSize(12).TextColor(pal.TextSecondary)
				})

				openBtn := ui.Row(c).Height(28).Padding(0, 12).Radius(6).
					Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
				openBtn.Children(func() {
					ui.Text(c, "在编辑器中打开").FontSize(12).TextColor(pal.TextSecondary)
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
func (sw *SettingsWindow) renderAboutTab(c *ui.Context, pal Palette, cfg *config.Config, s config.Settings) {
	ui.Column(c).Gap(18).Children(func() {
		// 关于 MediaGo
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "关于应用").FontSize(13).Bold().TextColor(pal.TextMuted)

			infoCard := ui.Row(c).Width(443).Padding(14, 16).Radius(10).
				Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
			infoCard.Children(func() {
				ui.Column(c).Gap(3).Grow(1).Children(func() {
					ui.Text(c, "MediaGo").FontSize(15).Bold().TextColor(pal.TextPrimary)
					ui.Text(c, "版本 1.0.0 · 原生高性能流媒体提取与下载工具").FontSize(12).TextColor(pal.TextSecondary)
				})

				ghBtn := ui.Row(c).Height(28).Padding(0, 12).Radius(6).
					Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
				ghBtn.Children(func() {
					ui.Text(c, "GitHub 主页").FontSize(12).Bold().TextColor(pal.TextPrimary)
				})
				if ghBtn.Clicked() {
					_ = exec.Command("open", "https://github.com/egoist/mygo").Start()
				}
			})
		})

		// 键盘快捷键对照
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "常用快捷键").FontSize(13).Bold().TextColor(pal.TextMuted)

			kbCard := ui.Column(c).Width(443).Padding(10, 16).Gap(8).Radius(10).
				Background(pal.SidebarBg).BorderWidth(1).BorderColor(pal.CardBorder)
			kbCard.Children(func() {
				shortcuts := []struct{ key, desc string }{
					{"⌘ N", "新建下载任务"},
					{"⌘ B", "网址启动器"},
					{"⌘ ,", "打开偏好设置"},
					{"⌘ W", "关闭当前窗口"},
					{"Esc", "退出对话框 / 启动器"},
				}
				for _, sc := range shortcuts {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Text(c, sc.desc).FontSize(12).TextColor(pal.TextSecondary).Grow(1)
						badge := ui.Row(c).Height(22).Padding(0, 6).Radius(4).
							Background(pal.CardBg).BorderWidth(1).BorderColor(pal.CardBorder).AlignItems(ui.Center)
						badge.Children(func() {
							ui.Text(c, sc.key).FontSize(11).Bold().TextColor(pal.TextPrimary)
						})
					})
				}
			})
		})
	})
}
