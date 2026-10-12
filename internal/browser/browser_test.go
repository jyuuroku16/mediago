package browser

import (
	"strings"
	"testing"

	"mediago/internal/model"
)

func sampleCandidate() *model.Candidate {
	return &model.Candidate{
		Title:       "Go 语言入门",
		SourceURL:   "https://www.bilibili.com/video/BV1xA411b7ri?p=2",
		CurrentPage: 2,
		Formats: []model.MediaFormat{
			{FormatID: "bv*[height<=1080]+ba/b", Resolution: "1080P", Ext: "mp4", Protocol: "extractor"},
			{FormatID: "hls-720", Resolution: "720P", Ext: "ts", Protocol: "m3u8", URL: "https://cdn.example.com/720.m3u8"},
		},
		Episodes: []model.Episode{
			{Index: 1, Title: "第一课", URL: "https://www.bilibili.com/video/BV1xA411b7ri?p=1"},
			{Index: 2, Title: "第二课", URL: "https://www.bilibili.com/video/BV1xA411b7ri?p=2"},
			{Index: 3, Title: "第三课", URL: "https://www.bilibili.com/video/BV1xA411b7ri?p=3"},
		},
	}
}

// TestParseConfirmParams 校验浏览器卡片确认协议的参数解析 (含 URL 编码与缺省模式)
func TestParseConfirmParams(t *testing.T) {
	fmtID, mode, eps := parseConfirmParams("mediago-sniff://confirm-download?fmtId=bv*%5Bheight%3C%3D1080%5D%2Bba%2Fb&mode=batch&eps=1%2C3")
	if fmtID != "bv*[height<=1080]+ba/b" || mode != "batch" || eps != "1,3" {
		t.Fatalf("解析结果不符: fmtId=%q mode=%q eps=%q", fmtID, mode, eps)
	}

	fmtID, mode, eps = parseConfirmParams("mediago-sniff://confirm-download?fmtId=abc")
	if fmtID != "abc" || mode != "current" || eps != "" {
		t.Fatalf("缺省模式应为 current: fmtId=%q mode=%q eps=%q", fmtID, mode, eps)
	}
}

// TestBuildConfirmTasksBatchSelectedEpisodes 批量模式按勾选分 P 逐集建任务
func TestBuildConfirmTasksBatchSelectedEpisodes(t *testing.T) {
	cand := sampleCandidate()
	tasks := buildConfirmTasks(cand, "bv*[height<=1080]+ba/b", "batch", "1,3", "/tmp/out")
	if len(tasks) != 2 {
		t.Fatalf("应按勾选创建 2 个任务，实际 %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Title, "[P01 第一课]") || !strings.Contains(tasks[1].Title, "[P03 第三课]") {
		t.Fatalf("分集标题不符: %q / %q", tasks[0].Title, tasks[1].Title)
	}
	if tasks[0].SourceURL != cand.Episodes[0].URL || tasks[1].SourceURL != cand.Episodes[2].URL {
		t.Fatalf("分集任务应使用各自分 P 的独立 URL: %q / %q", tasks[0].SourceURL, tasks[1].SourceURL)
	}
	for _, task := range tasks {
		if task.DownloadURL != "bv*[height<=1080]+ba/b" {
			t.Fatalf("extractor 规格应以 yt-dlp 选择器作为 DownloadURL，实际 %q", task.DownloadURL)
		}
		if task.Engine != model.EngineExtractor || task.Status != model.StatusQueued {
			t.Fatalf("任务引擎/状态不符: %s / %s", task.Engine, task.Status)
		}
		if task.OutputDir != "/tmp/out" || !strings.HasSuffix(task.OutputFilename, ".mp4") {
			t.Fatalf("输出目录/文件名不符: %q / %q", task.OutputDir, task.OutputFilename)
		}
	}
	if tasks[0].ID == tasks[1].ID {
		t.Fatal("批量任务 ID 必须唯一")
	}
}

// TestBuildConfirmTasksBatchWithoutSelectionFallsBack 批量模式未勾选任何分集时回退为当前分 P 单任务
func TestBuildConfirmTasksBatchWithoutSelectionFallsBack(t *testing.T) {
	cand := sampleCandidate()
	tasks := buildConfirmTasks(cand, "", "batch", "", "/tmp/out")
	if len(tasks) != 1 {
		t.Fatalf("未勾选任何分集时应回退为单任务，实际 %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Title, "[P02 第二课]") {
		t.Fatalf("回退任务应为当前分 P: %q", tasks[0].Title)
	}
}

// TestBuildConfirmTasksCurrentEpisode 单集模式只建当前分 P 任务，eps 参数被忽略
func TestBuildConfirmTasksCurrentEpisode(t *testing.T) {
	cand := sampleCandidate()
	tasks := buildConfirmTasks(cand, "", "current", "1,2,3", "/tmp/out")
	if len(tasks) != 1 {
		t.Fatalf("单集模式应只创建 1 个任务，实际 %d", len(tasks))
	}
	if tasks[0].SourceURL != cand.SourceURL {
		t.Fatalf("单集任务应使用当前页面 URL: %q", tasks[0].SourceURL)
	}
	if !strings.Contains(tasks[0].Title, "[P02 第二课]") {
		t.Fatalf("多分集视频单集任务标题应附带分 P 信息: %q", tasks[0].Title)
	}
}

// TestBuildConfirmTasksHLSFormat m3u8 规格走 HLS 引擎并使用直链
func TestBuildConfirmTasksHLSFormat(t *testing.T) {
	cand := sampleCandidate()
	tasks := buildConfirmTasks(cand, "hls-720", "current", "", "/tmp/out")
	if len(tasks) != 1 {
		t.Fatalf("应创建 1 个任务，实际 %d", len(tasks))
	}
	if tasks[0].Engine != model.EngineHLS || tasks[0].DownloadURL != "https://cdn.example.com/720.m3u8" {
		t.Fatalf("m3u8 规格应走 HLS 引擎并使用直链: %s / %q", tasks[0].Engine, tasks[0].DownloadURL)
	}
	if !strings.HasSuffix(tasks[0].OutputFilename, ".ts") {
		t.Fatalf("HLS 任务输出文件名应以 .ts 结尾: %q", tasks[0].OutputFilename)
	}
}

// TestBuildConfirmTasksSingleVideoNoSuffix 无分集视频不附加分 P 后缀
func TestBuildConfirmTasksSingleVideoNoSuffix(t *testing.T) {
	cand := sampleCandidate()
	cand.Episodes = nil
	cand.CurrentPage = 0
	tasks := buildConfirmTasks(cand, "", "batch", "1", "/tmp/out")
	if len(tasks) != 1 || tasks[0].Title != "Go 语言入门" {
		t.Fatalf("无分集视频不应附加分 P 后缀: %d 个任务, 标题 %q", len(tasks), tasks[0].Title)
	}
}

// TestJSString 校验 JS 字符串字面量编码可安全处理引号、换行与反斜杠
func TestJSString(t *testing.T) {
	got := jsString("a\"b\\c\nd")
	if got != `"a\"b\\c\nd"` {
		t.Fatalf("jsString 编码不符: %s", got)
	}
}
