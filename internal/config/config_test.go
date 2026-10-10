package config

import (
	"os"
	"testing"
)

func TestConfigLifecycle(t *testing.T) {
	cfg := Global()
	orig := cfg.Get()
	defer func() {
		cfg.Update(func(s *Settings) {
			*s = orig
		})
	}()

	changed := false
	stop := cfg.OnChange(func(s Settings) {
		if s.MaxConcurrent == 4 {
			changed = true
		}
	})
	defer stop()

	cfg.Update(func(s *Settings) {
		s.MaxConcurrent = 4
		s.Theme = "dark"
	})

	got := cfg.Get()
	if got.MaxConcurrent != 4 {
		t.Fatalf("更新 MaxConcurrent 失败，期望 4，实际 %d", got.MaxConcurrent)
	}
	if got.Theme != "dark" {
		t.Fatalf("更新 Theme 失败，期望 dark，实际 %s", got.Theme)
	}
	if !changed {
		t.Fatalf("OnChange 未被触发")
	}

	// 验证文件确实已写入
	if _, err := os.Stat(cfg.Path()); err != nil {
		t.Fatalf("配置文件未正确生成: %v", err)
	}
}

func TestStripJSONC(t *testing.T) {
	jsonc := []byte(`{
		// 这是一个注释
		"settings": {
			"theme": "dark", /* 行内注释 */
			"maxConcurrent": 3,
		}
	}`)
	cleaned := stripJSONC(jsonc)
	t.Logf("Cleaned JSON: %s", string(cleaned))
}
