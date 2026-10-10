package discovery

import (
	"context"
	"testing"
	"time"
)

func TestExtractBVIDAndPage(t *testing.T) {
	cases := []struct {
		url      string
		wantBVID string
		wantPage int
	}{
		{
			url:      "https://www.bilibili.com/video/BV1xA411b7ri/?spm_id_from=333.788.player.switch&vd_source=fa2ce602313887c5a6607aee51e857a1&p=5",
			wantBVID: "BV1xA411b7ri",
			wantPage: 5,
		},
		{
			url:      "https://www.bilibili.com/video/BV1xA411b7ri",
			wantBVID: "BV1xA411b7ri",
			wantPage: 1,
		},
		{
			url:      "BV1xA411b7ri",
			wantBVID: "BV1xA411b7ri",
			wantPage: 1,
		},
	}

	for _, tc := range cases {
		gotBVID := ExtractBVID(tc.url)
		if gotBVID != tc.wantBVID {
			t.Errorf("ExtractBVID(%s) = %s; want %s", tc.url, gotBVID, tc.wantBVID)
		}
		gotPage := ExtractPageNumber(tc.url)
		if gotPage != tc.wantPage {
			t.Errorf("ExtractPageNumber(%s) = %d; want %d", tc.url, gotPage, tc.wantPage)
		}
	}
}

func TestFetchBilibiliPages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	bvid := "BV1xA411b7ri"
	episodes, title, cover, err := FetchBilibiliPages(ctx, bvid)
	if err != nil {
		t.Fatalf("FetchBilibiliPages 失败: %v", err)
	}

	if len(episodes) != 41 {
		t.Fatalf("分集数量预期为 41，实际为 %d", len(episodes))
	}

	t.Logf("视频主标题: %s", title)
	t.Logf("封面图: %s", cover)
	t.Logf("总集数: %d", len(episodes))

	// 验证第 5 集
	p5 := episodes[4]
	if p5.Index != 5 {
		t.Errorf("P5 序号预期为 5，实际为 %d", p5.Index)
	}
	if p5.Title != "第五課" {
		t.Errorf("P5 标题预期为 '第五課'，实际为 '%s'", p5.Title)
	}
	t.Logf("P5 验证通过: 序号=%d, 标题=%s, 时长=%d秒, URL=%s", p5.Index, p5.Title, p5.Duration, p5.URL)
}
