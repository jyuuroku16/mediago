package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mediago/internal/config"
	"mediago/internal/discovery"
	"mediago/internal/engine"
	"mediago/internal/library"
	"mediago/internal/model"
	"mediago/internal/storage"
	"mediago/internal/testmedia"
)

func TestSharedDownloadFormInvalidatesEditedURL(t *testing.T) {
	app := &AppUI{modalURL: "https://example.com/old.mp4", parsedURL: "https://example.com/old.mp4", candidate: &model.Candidate{Title: "Old video", Formats: []model.MediaFormat{{Ext: "mp4", Resolution: "Original"}}}}
	tt := ui.NewTester(app.menuBarView, 460, 540)
	if !tt.HasText("Old video") {
		t.Fatal("candidate is not shown")
	}
	if err := tt.Click("媒体地址"); err != nil {
		t.Fatal(err)
	}
	tt.Type("new")
	if app.candidate != nil || app.canSubmitCandidate() {
		t.Fatalf("edited URL retained old candidate: url=%s texts=%v focused=%v", app.modalURL, tt.Texts(), tt.Focused("媒体地址"))
	}
	old := &model.Candidate{Title: "stale"}
	app.completeExtract(app.extractGeneration-1, app.modalURL, old, nil)
	if app.candidate != nil {
		t.Fatal("stale extraction overwrote the edited URL")
	}
}

func TestMenuBarSharedFormDownloadsDirectMedia(t *testing.T) {
	original := config.Global().Get()
	config.Global().Update(func(s *config.Settings) { s.AutoStart = true })
	defer config.Global().Update(func(s *config.Settings) { *s = original })
	srv := testmedia.NewServer()
	defer srv.Close()
	dir := t.TempDir()
	st, err := storage.NewStorage(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib, err := library.NewLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	sc := engine.NewScheduler(st, 1)
	sc.Start()
	defer sc.Stop()
	done := make(chan struct{}, 1)
	sc.OnUpdate(func(task *model.Task) {
		if task.Status == model.StatusCompleted {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	app := &AppUI{storage: st, library: lib, scheduler: sc, extractor: discovery.NewYtDlpExtractor(), modalURL: srv.URL() + "/mp4/range?name=video.mp4"}
	// A direct URL ending in .mp4 uses the same extractor as both entry points.
	cand, err := app.extractor.Extract(context.Background(), app.modalURL)
	if err != nil {
		t.Fatal(err)
	}
	app.completeExtract(app.extractGeneration, app.modalURL, cand, nil)
	tt := ui.NewTester(app.menuBarView, 460, 540)
	if err := tt.Click("立即开始下载"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("已加入下载列表") || app.candidate != nil {
		t.Fatal("submission did not show confirmation and clear the draft")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("download did not complete")
	}
	assets, err := st.ListAssets()
	if err != nil || len(assets) != 1 {
		t.Fatalf("assets: %v %v", assets, err)
	}
	data, err := os.ReadFile(assets[0].FilePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != srv.DataSHA256 {
		t.Fatal("download checksum mismatch")
	}
}

func TestMenuBarLayoutAndEmptyEpisodeSelection(t *testing.T) {
	app := &AppUI{modalURL: "https://example.com/video", parsedURL: "https://example.com/video", selectedEpisodesMap: map[int]bool{}, candidate: &model.Candidate{Title: "A long video title for a compact menu bar download panel", CurrentPage: 1, Episodes: []model.Episode{{Index: 1, Title: "Episode one"}, {Index: 2, Title: "Episode two"}}, Formats: []model.MediaFormat{{Resolution: "1080P 超清", Ext: "mp4"}, {Resolution: "720P 高清", Ext: "mp4"}, {Resolution: "Original", Ext: "mp4"}}}}
	app.downloadAllEpisodes = true
	if app.canSubmitCandidate() {
		t.Fatal("empty episode selection should not submit")
	}
	tt := ui.NewTester(app.menuBarView, 460, 540)
	if !tt.HasText("请先选择分集") {
		t.Fatal("empty selection missing feedback")
	}
	if err := tt.Click("批量下载 (已选 0 集)"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("选择批量下载分集") {
		t.Fatal("episode chooser did not open")
	}
	if err := tt.Click("全选"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确定"); err != nil {
		t.Fatal(err)
	}
	if !app.canSubmitCandidate() {
		t.Fatalf("selected episodes cannot submit: selected=%v batch=%v texts=%v", app.selectedEpisodesMap, app.downloadAllEpisodes, tt.Texts())
	}
	if path := os.Getenv("MEDIAGO_TEST_SNAPSHOT"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSettingsMenuBarSwitch(t *testing.T) {
	original := config.Global().Get()
	defer config.Global().Update(func(s *config.Settings) { *s = original })
	config.Global().Update(func(s *config.Settings) { s.MenuBarEnabled = true })
	sw := &SettingsWindow{activeTab: "general"}
	tt := ui.NewTester(sw.View, 680, 480)
	if err := tt.Click("菜单栏入口开关"); err != nil {
		t.Fatal(err)
	}
	if config.Global().Get().MenuBarEnabled {
		t.Fatal("switch failed to disable the menu bar")
	}
	if err := tt.Click("菜单栏入口开关"); err != nil {
		t.Fatal(err)
	}
	if !config.Global().Get().MenuBarEnabled {
		t.Fatal("switch failed to re-enable the menu bar")
	}
}

func TestMenuBarSubmissionRespectsAutoStartAndReportsFailure(t *testing.T) {
	original := config.Global().Get()
	defer config.Global().Update(func(s *config.Settings) { *s = original })
	config.Global().Update(func(s *config.Settings) { s.AutoStart = false })
	dir := t.TempDir()
	st, err := storage.NewStorage(filepath.Join(dir, "idle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib, err := library.NewLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := &AppUI{storage: st, library: lib, scheduler: engine.NewScheduler(st, 1), modalURL: "https://example.com/video.mp4", parsedURL: "https://example.com/video.mp4", candidate: &model.Candidate{Title: "video", SourceURL: "https://example.com/video.mp4", Formats: []model.MediaFormat{{Ext: "mp4", Resolution: "Original", URL: "https://example.com/video.mp4", Protocol: "http"}}}}
	tt := ui.NewTester(app.menuBarView, 460, 540)
	if err := tt.Click("立即开始下载"); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.ListTasks("")
	if err != nil || len(tasks) != 1 || tasks[0].Status != model.StatusPaused {
		t.Fatalf("AutoStart disabled: tasks=%v error=%v", tasks, err)
	}
	if !tt.HasText("已加入下载列表，等待手动开始") {
		t.Fatal("paused task confirmation missing")
	}
	app.modalURL = "https://example.com/another.mp4"
	app.parsedURL = app.modalURL
	app.candidate = &model.Candidate{Title: "another", SourceURL: app.modalURL, Formats: []model.MediaFormat{{Ext: "mp4", Resolution: "Original", URL: app.modalURL, Protocol: "http"}}}
	st.Close()
	tt.Frame()
	if err := tt.Click("立即开始下载"); err != nil {
		t.Fatal(err)
	}
	if app.candidate == nil || app.extractError == "" {
		t.Fatal("failed submission lost the draft or suppressed the error")
	}
}

func TestMenuBarRejectsOffScreenProxyAnchor(t *testing.T) {
	displays := []mygo.Display{{Bounds: mygo.Rectangle{Width: 1512, Height: 982}, WorkArea: mygo.Rectangle{Y: 33, Width: 1512, Height: 949}}}
	for _, r := range []mygo.Rectangle{{X: 0, Y: 982, Width: 34, Height: 33}, {X: 0, Y: 0, Width: 34, Height: 0}, {X: -34, Y: 0, Width: 34, Height: 33}} {
		if menuBarAnchorAvailable(r, displays) {
			t.Fatalf("invalid anchor accepted: %+v", r)
		}
	}
	if !menuBarAnchorAvailable(mygo.Rectangle{X: 1200, Y: 0, Width: 34, Height: 33}, displays) {
		t.Fatal("visible top menu bar anchor rejected")
	}
}
