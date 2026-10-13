//go:build darwin && mediago_native

package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"
	"mediago/internal/config"
	"mediago/internal/engine"
	"mediago/internal/library"
	"mediago/internal/storage"
)

// Run with go test -tags mediago_native ./internal/ui -run TestNativeMenuBar.
// This exercises real AppKit windows and status items in a separate test process.
func TestMain(m *testing.M) {
	runtime.LockOSThread()
	result := 1
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		go func() { result = m.Run(); mygo.App.Quit() }()
	})
	if err := mygo.App.Run(); err != nil {
		panic(err)
	}
	os.Exit(result)
}

func TestNativeMenuBarLifecycle(t *testing.T) {
	original := config.Global().Get()
	config.Global().Update(func(s *config.Settings) { s.MenuBarEnabled = true })
	defer config.Global().Update(func(s *config.Settings) { *s = original })
	dir := t.TempDir()
	st, err := storage.NewStorage(filepath.Join(dir, "native.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib, err := library.NewLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	sc := engine.NewScheduler(st, 1)
	app := NewAppUI(st, sc, nil, lib)
	var win *mygo.Window
	mygo.RunOnMain(func() {
		win = mygo.NewWindow(mygo.WindowOptions{Title: "MediaGo native smoke", Width: 860, Height: 540, Content: mygoui.View(app.View)})
		app.SetWindow(win)
		app.SetupMenuBar()
	})
	defer mygo.RunOnMain(func() { app.menuBar.Destroy(); win.Destroy() })
	time.Sleep(250 * time.Millisecond)
	var panel *mygo.Window
	var trayBounds mygo.Rectangle
	var hasAnchor bool
	mygo.RunOnMain(func() {
		trayBounds = app.menuBar.tray.Bounds()
		hasAnchor = menuBarAnchorAvailable(trayBounds, mygo.Screen.Displays())
		item := objc.ID(app.menuBar.tray.NativeHandle())
		if item == 0 {
			t.Error("no native status item")
		}
		if item.Send(objc.RegisterName("respondsToSelector:"), uintptr(objc.RegisterName("expandedInterfaceDelegate"))) != 0 {
			if item.Send(objc.RegisterName("expandedInterfaceDelegate")) == 0 {
				t.Error("macOS 27 status item has no expanded-interface delegate")
			}
		}
		if item.Send(objc.RegisterName("autosaveName")) == 0 {
			t.Error("no stable status item identity")
		}
		app.menuBar.openPanel()
		panel = app.menuBar.panel
	})
	if !hasAnchor {
		if panel != nil || app.showModal {
			t.Fatal("off-screen status item must not open a panel or a main-window fallback")
		}
		t.Logf("off-screen proxy rejected: %+v; macOS 27 delegate installed", trayBounds)
	} else {
		if panel == nil || !panel.IsFlyout() || !panel.IsVisible() {
			t.Fatal("native flyout did not open")
		}
		bounds := panel.Bounds()
		if bounds.Y <= trayBounds.Y || bounds.Y > trayBounds.Y+trayBounds.Height+30 || bounds.X > trayBounds.X+trayBounds.Width/2 || bounds.X+bounds.Width < trayBounds.X+trayBounds.Width/2 {
			t.Fatalf("panel is not under its status item: item=%+v panel=%+v", trayBounds, bounds)
		}
		if path := os.Getenv("MEDIAGO_NATIVE_SNAPSHOT"); path != "" {
			data, err := panel.CapturePage()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	mygo.RunOnMain(func() {
		app.menuBar.closePanel()
		win.Hide()
		app.menuBar.SetEnabled(false)
	})
	if !win.IsVisible() {
		t.Fatal("disabling the tray must restore a hidden main window")
	}
	mygo.RunOnMain(func() {
		if app.menuBar.tray != nil || app.menuBar.panel != nil {
			t.Error("disabled entry retained native resources")
		}
		app.menuBar.SetEnabled(true)
	})
	mygo.RunOnMain(func() {
		if app.menuBar.tray == nil {
			t.Error("re-enabled entry did not recreate its status item")
		}
		app.OpenSettings()
		if app.menuBar.panel != nil {
			t.Error("opening settings did not dismiss the flyout")
		}
	})
	// Destroy the settings window created by this test.
	mygo.RunOnMain(func() {
		if app.settingsWin.win != nil {
			app.settingsWin.win.Destroy()
		}
	})
}
