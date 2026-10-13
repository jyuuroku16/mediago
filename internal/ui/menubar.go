package ui

import (
	_ "embed"
	"log"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mediago/internal/config"
)

// MenuBar owns the tray and its transient panel; download state belongs to AppUI.
// All native window and tray mutations run on the application thread.
type MenuBar struct {
	app   *AppUI
	tray  *mygo.Tray
	panel *mygo.Window
}

func (app *AppUI) SetupMenuBar() {
	app.menuBar = &MenuBar{app: app}
	app.menuBar.SetEnabled(config.Global().Get().MenuBarEnabled)
	config.Global().OnChange(func(s config.Settings) {
		mygo.RunOnMain(func() { app.menuBar.SetEnabled(s.MenuBarEnabled) })
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { app.menuBar.Destroy() })
}

func (m *MenuBar) SetEnabled(enabled bool) {
	if !enabled {
		// Keep the app reachable if its main window was hidden to the tray.
		if m.app.window != nil && !m.app.window.IsVisible() {
			m.app.ShowMainWindow()
		}
		m.Destroy()
		return
	}
	if m.tray != nil {
		return
	}
	tray, err := mygo.NewTray(mygo.TrayOptions{
		AutosaveName: "mediago.quick-download",
		Icon:         menuBarIcon(), IconIsTemplate: runtime.GOOS == "darwin", ToolTip: "MediaGo · 快速下载",
	})
	if err != nil {
		log.Printf("mediago: menu bar: %v", err)
		return
	}
	m.tray = tray
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "快速下载…", Click: func(*mygo.MenuItem, *mygo.Window) { m.openPanel() }},
		{Label: "显示 MediaGo", Click: func(*mygo.MenuItem, *mygo.Window) { m.app.ShowMainWindow() }},
		{Label: "打开下载目录", Click: func(*mygo.MenuItem, *mygo.Window) { openDirectory(config.Global().Get().DownloadDir) }},
		mygo.Separator(),
		{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { m.app.OpenSettings() }},
		{Label: "隐藏菜单栏入口", Click: func(*mygo.MenuItem, *mygo.Window) {
			config.Global().Update(func(s *config.Settings) { s.MenuBarEnabled = false })
		}},
		mygo.Separator(),
		{Role: mygo.RoleQuit, Label: "退出 MediaGo"},
	})
	if runtime.GOOS == "linux" {
		tray.SetMenu(menu)
	} else {
		tray.OnExpandedInterfaceClosed(m.closePanel)
		tray.OnClick(func() {
			if m.panel != nil {
				m.closePanel()
			} else {
				m.openPanel()
			}
		})
		tray.OnRightClick(func() {
			m.closePanel()
			tray.SetMenu(menu)
			defer tray.SetMenu(nil)
			tray.PopUpMenu()
		})
	}
}

func (m *MenuBar) openPanel() {
	if m.tray == nil || m.panel != nil {
		return
	}
	// macOS 27 keeps an off-screen proxy until the real status item starts
	// an expanded-interface session. Never show a panel from that proxy.
	if runtime.GOOS == "darwin" && !menuBarAnchorAvailable(m.tray.Bounds(), mygo.Screen.Displays()) {
		log.Printf("mediago: menu bar has no on-screen anchor; open it from the system status item")
		m.tray.CancelExpandedInterface()
		return
	}
	m.app.showModal = false
	m.app.showEpisodesModal = false
	m.app.RefreshUI()
	m.panel = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray: m.tray, Placement: mygo.PlacementBottom, Gap: 4,
		Width: 460, Height: m.app.menuBarHeight(), Focusable: true, Shadow: true, Popover: true,
		Content: ui.View(m.app.menuBarView),
	})
	m.panel.OnClosed(func() {
		m.panel = nil
		m.app.showEpisodesModal = false
		if m.tray != nil {
			m.tray.CancelExpandedInterface()
		}
	})
}

func (m *MenuBar) closePanel() {
	if m.panel != nil {
		m.panel.Close()
	}
}
func (m *MenuBar) Destroy() {
	m.closePanel()
	if m.tray != nil {
		m.tray.Destroy()
		m.tray = nil
	}
}

// A valid anchor must lie within a display's top menu-bar strip, not just
// have a positive height: off-screen macOS 27 proxies also have a height.
func menuBarAnchorAvailable(r mygo.Rectangle, displays []mygo.Display) bool {
	if r.Width <= 0 || r.Height <= 0 {
		return false
	}
	for _, d := range displays {
		b := d.Bounds
		top := d.WorkArea.Y
		if top <= b.Y {
			top = b.Y + r.Height
		}
		if r.X >= b.X && r.X+r.Width <= b.X+b.Width && r.Y >= b.Y-2 && r.Y+r.Height <= top+2 {
			return true
		}
	}
	return false
}

func (app *AppUI) ShowMainWindow() {
	if app.menuBar != nil {
		app.menuBar.closePanel()
	}
	if app.window != nil {
		app.window.Show()
		app.window.Focus()
	}
}

func (app *AppUI) finishDownloadForm(compact bool) {
	app.showModal = false
	if compact {
		// Keep the panel open after submission so the user sees confirmation.
		if app.candidate != nil {
			app.resetDownload()
			app.downloadNotice = "已加入下载列表"
			if !config.Global().Get().AutoStart {
				app.downloadNotice = "已加入下载列表，等待手动开始"
			}
		} else if app.menuBar != nil {
			app.menuBar.closePanel()
		}
	}
	app.RefreshUI()
}

// Keep the empty panel small; results and episode selection have room to scroll.
func (app *AppUI) menuBarHeight() int {
	if app.showEpisodesModal {
		return 540
	}
	if app.candidate != nil {
		height := 260 + ((len(app.candidate.Formats)+1)/2)*36
		if len(app.candidate.Episodes) > 1 {
			height += 120
		}
		return min(540, height)
	}
	if app.extractError != "" {
		return 280
	}
	if app.isExtracting {
		return 240
	}
	return 156
}

func (app *AppUI) menuBarView(c *ui.Context) {
	pal := getPalette(c.Theme())
	width, height := c.Size()
	root := ui.Column(c).FillWidth().Height(height).Padding(12, 14).Gap(10)
	if !c.Vibrancy() {
		root.Background(pal.InputBg)
	}
	root.Children(func() {
		ui.Scroll(c).Vertical().FillWidth().Grow(1).Children(func() {
			app.renderDownloadForm(c, pal, width-28, true)
		})
		if app.downloadNotice != "" {
			ui.Text(c, app.downloadNotice).FontSize(12).TextColor(pal.Accent)
		}
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			browser := app.iconButton(c, iconGlobe, "打开浏览器")
			if browser.Clicked() && app.browserMgr != nil {
				app.menuBar.closePanel()
				app.browserMgr.Open(app.modalURL)
			}
			ui.Spacer(c)
			b := ui.ButtonBase(c).Label("查看下载").Padding(6, 10).Radius(6).Background(pal.InputBg)
			b.Children(func() { ui.Text(c, "查看下载").FontSize(12).TextColor(pal.Accent) })
			if b.Clicked() {
				app.ShowMainWindow()
			}
		})
	})
	if app.showEpisodesModal {
		app.renderEpisodesModal(c, pal, true)
	}
}

// Full App Icon: outer outline, solid inner tile, and transparent download glyph.
//
//go:embed resources/menubar-download.png
var menuBarImage []byte

func menuBarIcon() []byte { return menuBarImage }
