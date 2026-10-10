package testmedia

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"
)

func TestTestMediaServer(t *testing.T) {
	srv := NewServer()
	defer srv.Close()

	// 1. 测试 MP4 Range 请求
	req, err := http.NewRequest(http.MethodGet, srv.URL()+"/mp4/range", nil)
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("期望 206 Partial Content，实际为 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 1024 {
		t.Fatalf("期望读取 1024 字节，实际为 %d", len(body))
	}

	// 2. 测试 429 退避
	// 第一次和第二次应该 429
	resp1, _ := http.Get(srv.URL() + "/retry/429")
	if resp1.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("期望第 1 次返回 429，实际为 %d", resp1.StatusCode)
	}
	resp2, _ := http.Get(srv.URL() + "/retry/429")
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("期望第 2 次返回 429，实际为 %d", resp2.StatusCode)
	}
	// 第三次应该返回 200
	resp3, _ := http.Get(srv.URL() + "/retry/429")
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("期望第 3 次返回 200，实际为 %d", resp3.StatusCode)
	}
	fullData, _ := io.ReadAll(resp3.Body)
	h := sha256.Sum256(fullData)
	if hex.EncodeToString(h[:]) != srv.DataSHA256 {
		t.Fatalf("完整数据哈希不一致")
	}
}
