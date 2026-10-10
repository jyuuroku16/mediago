package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"mediago/internal/browser"
	"mediago/internal/engine"
	"mediago/internal/library"
	"mediago/internal/model"
	"mediago/internal/storage"
	"mediago/internal/ui"
)

func main() {
	// 1. 初始化应用数据目录与数据库
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("获取用户目录失败: %v", err)
	}

	appDataDir := filepath.Join(home, "Library", "Application Support", "MediaGo")
	_ = os.MkdirAll(appDataDir, 0755)
	dbPath := filepath.Join(appDataDir, "mediago.db")

	store, err := storage.NewStorage(dbPath)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer store.Close()

	// 2. 初始化本地媒体库
	lib, err := library.NewLibrary()
	if err != nil {
		log.Fatalf("初始化媒体库失败: %v", err)
	}

	// 3. 初始化并发下载调度器
	scheduler := engine.NewScheduler(store, 3)
	scheduler.Start()
	defer scheduler.Stop()

	// 4. 初始化控制器与浏览器管理器
	var appUI *ui.AppUI
	browserMgr := browser.NewManager(func(c *model.Candidate) {
		if appUI != nil {
			appUI.HandleSniffedCandidate(c)
		}
	})

	appUI = ui.NewAppUI(store, scheduler, browserMgr, lib)

	mygo.App.WhenReady(func() {
		// 注册 macOS 原生系统顶部菜单栏 (包含 Settings ⌘,)
		mygo.App.SetMenu(buildAppMenu(appUI, browserMgr))

		win := mygo.NewWindow(mygo.WindowOptions{
			Title:          "MediaGo",
			Width:          1200,
			Height:         780,
			MinWidth:       860,
			MinHeight:      540,
			StateKey:       "mediago_main_window",
			TitleBarStyle:  mygo.TitleBarHiddenInset,
			TitleBarHeight: 52,
			Vibrancy:       mygo.VibrancySidebar,
			Content:        mygoui.View(appUI.View),
		})

		appUI.SetWindow(win)

		win.OnClosed(func() {
			scheduler.Stop()
			os.Exit(0)
		})
	})

	// 6. 运行 MyGo 事件循环
	if err := mygo.App.Run(); err != nil {
		log.Fatalf("应用运行异常: %v", err)
	}
}
