package engine

import (
	"testing"
	"time"
)

// TestAutoRetryDelay 校验自动重试退避策略: 间隔递增、有界，超出上限返回 0
func TestAutoRetryDelay(t *testing.T) {
	if maxAutoRetries != 3 {
		t.Fatalf("自动重试次数上限应为 3，当前为 %d", maxAutoRetries)
	}

	prev := time.Duration(0)
	for attempt := 1; attempt <= maxAutoRetries; attempt++ {
		d := autoRetryDelay(attempt)
		if d <= 0 {
			t.Fatalf("第 %d 次自动重试延迟应为正值，当前为 %v", attempt, d)
		}
		if d <= prev {
			t.Fatalf("第 %d 次自动重试延迟应严格递增: %v <= %v", attempt, d, prev)
		}
		prev = d
	}

	if got := autoRetryDelay(maxAutoRetries + 1); got != 0 {
		t.Fatalf("超出重试上限应返回 0，当前为 %v", got)
	}
}
