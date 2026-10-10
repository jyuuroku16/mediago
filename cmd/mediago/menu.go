package main

import (
	"runtime"

	"github.com/egoist/mygo"
	"mediago/internal/browser"
	"mediago/internal/ui"
)

// buildAppMenu 构建 macOS 原生系统顶部菜单栏
func buildAppMenu(appUI *ui.AppUI, browserMgr *browser.Manager) *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
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
					Label:       "专用浏览器",
					Accelerator: "CmdOrCtrl+B",
					Click: func(*mygo.MenuItem, *mygo.Window) {
						if browserMgr != nil {
							browserMgr.Open("https://www.bilibili.com")
						}
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
					Label:       "重新加载",
					Accelerator: "CmdOrCtrl+R",
					Click: func(_ *mygo.MenuItem, win *mygo.Window) {
						if win != nil {
							win.Invalidate()
						}
					},
				},
				mygo.Separator(),
				{
					Role:   mygo.RoleToggleDevTools,
					Hidden: runtime.GOOS != "darwin",
				},
			},
		},
		{
			Label: "Window",
			Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleMinimize},
				{Role: mygo.RoleZoom},
				mygo.Separator(),
				{Role: mygo.RoleFront},
			},
		},
	})
}
