package discovery

import (
	"context"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	raw := "https://example.com/video?id=123&utm_source=twitter&utm_medium=social&share_source=app"
	expected := "https://example.com/video?id=123"
	got := NormalizeURL(raw)
	if got != expected {
		t.Fatalf("规范化失败，期望: %s，实际: %s", expected, got)
	}
}

func TestIsDirectMediaURL(t *testing.T) {
	testCases := []struct {
		url      string
		isDirect bool
		ext      string
	}{
		{"https://cdn.example.com/movie.mp4?token=abc", true, "mp4"},
		{"https://cdn.example.com/live/master.m3u8", true, "m3u8"},
		{"https://example.com/watch?v=123", false, ""},
	}

	for _, tc := range testCases {
		ok, ext := IsDirectMediaURL(tc.url)
		if ok != tc.isDirect || ext != tc.ext {
			t.Fatalf("测试 URL: %s 失败，期望: (%v, %s)，实际: (%v, %s)", tc.url, tc.isDirect, tc.ext, ok, ext)
		}
	}
}

func TestDirectMediaExtract(t *testing.T) {
	extractor := NewYtDlpExtractor()
	cand, err := extractor.Extract(context.Background(), "https://example.com/sample.mp4")
	if err != nil {
		t.Fatalf("直链提取失败: %v", err)
	}
	if len(cand.Formats) != 1 || cand.Formats[0].Ext != "mp4" {
		t.Fatalf("提取出的候选媒体规格不正确: %+v", cand)
	}
}
