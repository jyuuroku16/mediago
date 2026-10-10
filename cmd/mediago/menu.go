package main

import (
	"fmt"
	"os/exec"

	"github.com/egoist/mygo"
	"mediago/internal/browser"
	"mediago/internal/config"
	"mediago/internal/ui"
)

var appMenu *mygo.Menu

// buildAppMenu 构建完全对齐 egoist/godiff 规范的 macOS 原生系统顶部菜单栏
func buildAppMenu(appUI *ui.AppUI, browserMgr *browser.Manager) *mygo.Menu {
	cfg := config.Global()
	s := cfg.Get()

	setTheme := func(theme string) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) {
			cfg.Update(func(s *config.Settings) { s.Theme = theme })
		}
	}

	setConcur := func(n int) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) {
			cfg.Update(func(s *config.Settings) { s.MaxConcurrent = n })
		}
	}

	appMenu = mygo.NewMenu([]*mygo.MenuItem{
		{
			Role: mygo.RoleAppMenu,
			Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleAbout},
				mygo.Separator(),
				{
					Label:       "Settings…",
					Accelerator: "CmdOrCtrl+,",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						if appUI != nil {
							appUI.OpenSettings()
						}
					},
				},
				{
					Label: "Open Config File…",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						_ = exec.Command("open", cfg.Path()).Start()
					},
				},
				mygo.Separator(),
				{Role: mygo.RoleServices},
				mygo.Separator(),
				{Role: mygo.RoleHide},
				{Role: mygo.RoleHideOthers},
				{Role: mygo.RoleUnhide},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			},
		},
		{
			Label: "File",
			Submenu: []*mygo.MenuItem{
				{
					Label:       "新建下载…",
					Accelerator: "CmdOrCtrl+N",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						if appUI != nil {
							appUI.OpenNewDownload()
						}
					},
				},
				{
					Label:       "网址启动器 (专用浏览器)…",
					Accelerator: "CmdOrCtrl+B",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						if appUI != nil {
							appUI.OpenBrowserLauncher()
						}
					},
				},
				mygo.Separator(),
				{
					Label: "选择下载保存目录…",
					Click: func(_ *mygo.MenuItem, win *mygo.Window) {
						go func() {
							paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
								Title:       "选择媒体下载保存目录",
								Directory:   true,
								DefaultPath: cfg.Get().DownloadDir,
							})
							if err == nil && len(paths) > 0 {
								cfg.Update(func(s *config.Settings) {
									s.DownloadDir = paths[0]
								})
							}
						}()
					},
				},
				{
					Label: "在 Finder 中打开下载目录",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						_ = exec.Command("open", cfg.Get().DownloadDir).Start()
					},
				},
				mygo.Separator(),
				{Role: mygo.RoleClose},
			},
		},
		{
			Label: "Edit",
			Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleUndo},
				{Role: mygo.RoleRedo},
				mygo.Separator(),
				{Role: mygo.RoleCut},
				{Role: mygo.RoleCopy},
				{Role: mygo.RolePaste},
				{Role: mygo.RoleSelectAll},
			},
		},
		{
			Label: "View",
			Submenu: []*mygo.MenuItem{
				{
					Label: "下载并发数",
					Submenu: []*mygo.MenuItem{
						{ID: "concur-1", Label: "1 个任务", Type: mygo.MenuItemRadio, Checked: s.MaxConcurrent == 1, Click: setConcur(1)},
						{ID: "concur-2", Label: "2 个任务", Type: mygo.MenuItemRadio, Checked: s.MaxConcurrent == 2, Click: setConcur(2)},
						{ID: "concur-3", Label: "3 个任务 (默认)", Type: mygo.MenuItemRadio, Checked: s.MaxConcurrent == 3, Click: setConcur(3)},
						{ID: "concur-4", Label: "4 个任务", Type: mygo.MenuItemRadio, Checked: s.MaxConcurrent == 4, Click: setConcur(4)},
						{ID: "concur-6", Label: "6 个任务", Type: mygo.MenuItemRadio, Checked: s.MaxConcurrent == 6, Click: setConcur(6)},
					},
				},
				{
					Label: "外观主题",
					Submenu: []*mygo.MenuItem{
						{ID: "theme-system", Label: "跟随系统 (System)", Type: mygo.MenuItemRadio, Checked: s.Theme == "system", Click: setTheme("system")},
						{ID: "theme-light", Label: "浅色外观 (Light)", Type: mygo.MenuItemRadio, Checked: s.Theme == "light", Click: setTheme("light")},
						{ID: "theme-dark", Label: "深色外观 (Dark)", Type: mygo.MenuItemRadio, Checked: s.Theme == "dark", Click: setTheme("dark")},
					},
				},
				mygo.Separator(),
				{
					Label:       "切换侧边栏",
					Accelerator: "CmdOrCtrl+\\",
					Click: func(_ *mygo.MenuItem, win *mygo.Window) {
						if appUI != nil {
							appUI.ToggleSidebar()
						}
					},
				},
				{
					Label:       "重新加载",
					Accelerator: "CmdOrCtrl+R",
					Click: func(_ *mygo.MenuItem, win *mygo.Window) {
						if win != nil {
							win.Invalidate()
						}
					},
				},
				mygo.Separator(),
				{Role: mygo.RoleToggleFullScreen},
				{
					Role:   mygo.RoleToggleDevTools,
					Label:  "开发者审查工具 (Inspector)",
					Hidden: !mygo.IsDev(),
				},
			},
		},
		{
			Label:   "Window",
			Submenu: roleSubmenu(mygo.RoleWindowMenu),
		},
		{
			Role: mygo.RoleHelp,
			Submenu: []*mygo.MenuItem{
				{
					Label:       "键盘快捷键速查 (Shortcuts)",
					Accelerator: "CmdOrCtrl+/",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						if appUI != nil {
							appUI.OpenShortcutsHelp()
						}
					},
				},
			},
		},
	})

	return appMenu
}

// syncAppMenu 在设置发生变动时同步系统菜单栏的勾选状态
func syncAppMenu(s config.Settings) {
	if appMenu == nil {
		return
	}
	setChecked := func(id string, checked bool) {
		if it := appMenu.ItemByID(id); it != nil {
			it.SetChecked(checked)
		}
	}

	setChecked("theme-system", s.Theme == "system")
	setChecked("theme-light", s.Theme == "light")
	setChecked("theme-dark", s.Theme == "dark")

	concurs := []int{1, 2, 3, 4, 6}
	for _, c := range concurs {
		setChecked(fmt.Sprintf("concur-%d", c), s.MaxConcurrent == c)
	}
}

func roleSubmenu(role mygo.MenuRole) []*mygo.MenuItem {
	return []*mygo.MenuItem{
		{Role: mygo.RoleMinimize},
		{Role: mygo.RoleZoom},
		mygo.Separator(),
		{Role: mygo.RoleFront},
	}
}
