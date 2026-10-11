package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"mediago/internal/browser"
	"mediago/internal/config"
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

	// 2. 加载全局配置并应用主题
	cfg := config.Global()
	s := cfg.Get()
	config.ApplyTheme(s.Theme)

	// 3. 初始化本地媒体库
	lib, err := library.NewLibrary(s.DownloadDir)
	if err != nil {
		log.Fatalf("初始化媒体库失败: %v", err)
	}

	// 4. 初始化并发下载调度器
	scheduler := engine.NewScheduler(store, s.MaxConcurrent)
	scheduler.Start()
	defer scheduler.Stop()

	// 5. 初始化控制器与浏览器管理器
	var appUI *ui.AppUI
	browserMgr := browser.NewManager(func(task *model.Task) {
		scheduler.Submit(task)
		if appUI != nil {
			appUI.RefreshUI()
		}
	}, s.DownloadDir)

	appUI = ui.NewAppUI(store, scheduler, browserMgr, lib)

	// 注册配置监听器
	cfg.OnChange(func(latest config.Settings) {
		scheduler.SetMaxActiveTasks(latest.MaxConcurrent)
		_ = lib.SetBaseDir(latest.DownloadDir)
		browserMgr.SetDownloadDir(latest.DownloadDir)
		config.ApplyTheme(latest.Theme)
		syncAppMenu(latest)
		if appUI != nil {
			appUI.RefreshUI()
		}
	})

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

		if os.Getenv("SHOW_SETTINGS_DEFAULT") == "1" {
			appUI.OpenSettings()
		} else if os.Getenv("SHOW_MODAL_DEFAULT") == "1" {
			appUI.OpenNewDownload()
		} else if os.Getenv("SHOW_BROWSER_DEFAULT") == "1" {
			browserMgr.Open("https://www.bilibili.com")
		}

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
