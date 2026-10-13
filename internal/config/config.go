package config

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
)

// Settings 对应应用的持久化偏好设置，参考 egoist/godiff 的设计
type Settings struct {
	DownloadDir      string `json:"downloadDir"`      // 下载保存目录
	MaxConcurrent    int    `json:"maxConcurrent"`    // 最大活动下载任务数 (1~8)
	Theme            string `json:"theme"`            // system, light, dark
	MenuBarEnabled   bool   `json:"menuBarEnabled"`   // 菜单栏快捷下载入口
	Notifications    bool   `json:"notifications"`    // 下载完成桌面通知
	AutoStart        bool   `json:"autoStart"`        // 自动开始新加入的任务
	InjectedOmnibox  bool   `json:"injectedOmnibox"`  // 专用浏览器启用 Chrome 风格顶部地址栏
	UserAgentSpoof   bool   `json:"userAgentSpoof"`   // Chrome 131 桌面 UA 伪装防拦截
	AutoDetectStream bool   `json:"autoDetectStream"` // 播放页深度流媒体感知
	AutoCategorize   bool   `json:"autoCategorize"`   // 按平台自动分类保存文件夹
	PreferredQuality string `json:"preferredQuality"` // best, 1080p, 720p
}

func defaultSettings() Settings {
	home, _ := os.UserHomeDir()
	defaultDir := filepath.Join(home, "Movies", "MediaGo")
	return Settings{
		DownloadDir:      defaultDir,
		MaxConcurrent:    3,
		Theme:            "system",
		MenuBarEnabled:   true,
		Notifications:    true,
		AutoStart:        true,
		InjectedOmnibox:  true,
		UserAgentSpoof:   true,
		AutoDetectStream: true,
		AutoCategorize:   false,
		PreferredQuality: "1080p",
	}
}

func (s *Settings) normalize() {
	d := defaultSettings()
	s.DownloadDir = strings.TrimSpace(s.DownloadDir)
	if s.DownloadDir == "" {
		s.DownloadDir = d.DownloadDir
	}
	if s.MaxConcurrent <= 0 {
		s.MaxConcurrent = d.MaxConcurrent
	}
	if s.MaxConcurrent > 8 {
		s.MaxConcurrent = 8
	}
	if s.Theme != "system" && s.Theme != "light" && s.Theme != "dark" {
		s.Theme = d.Theme
	}
	if s.PreferredQuality != "best" && s.PreferredQuality != "1080p" && s.PreferredQuality != "720p" {
		s.PreferredQuality = d.PreferredQuality
	}
}

// Config 全局配置管理器，保持配置与文件同步并分发更新事件
type Config struct {
	mu       sync.Mutex
	path     string
	settings Settings
	onChange map[int]func(Settings)
	nextID   int
}

var globalConfig = newConfig()

// Global 获取全局配置管理器单例
func Global() *Config {
	return globalConfig
}

func newConfig() *Config {
	home, _ := os.UserHomeDir()
	c := &Config{
		path:     filepath.Join(home, ".mediago", "mediago.jsonc"),
		settings: defaultSettings(),
		onChange: make(map[int]func(Settings)),
	}
	c.load()
	return c
}

// Path 获取配置文件磁盘路径
func (c *Config) Path() string {
	return c.path
}

// Get 获取当前设置快照
func (c *Config) Get() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

// Update 更新设置并自动写盘通知监听者
func (c *Config) Update(fn func(s *Settings)) {
	c.mu.Lock()
	fn(&c.settings)
	c.settings.normalize()
	s := c.settings
	c.mu.Unlock()

	c.write(s)
	c.notify(s)
}

// OnChange 注册设置变动监听器
func (c *Config) OnChange(fn func(Settings)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	c.onChange[id] = fn
	return func() {
		c.mu.Lock()
		delete(c.onChange, id)
		c.mu.Unlock()
	}
}

func (c *Config) notify(s Settings) {
	c.mu.Lock()
	fns := make([]func(Settings), 0, len(c.onChange))
	for _, fn := range c.onChange {
		fns = append(fns, fn)
	}
	c.mu.Unlock()

	for _, fn := range fns {
		fn(s)
	}
}

func (c *Config) load() bool {
	data, err := os.ReadFile(c.path)
	if err != nil {
		c.write(c.settings)
		return false
	}

	var file struct {
		Settings json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(stripJSONC(data), &file); err != nil {
		log.Printf("mediago: %s: %v", c.path, err)
		return false
	}

	s := defaultSettings()
	if len(file.Settings) > 0 {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(file.Settings, &fields)
		for k, v := range fields {
			one, _ := json.Marshal(map[string]json.RawMessage{k: v})
			_ = json.Unmarshal(one, &s)
		}
	}
	s.normalize()

	c.mu.Lock()
	c.settings = s
	c.mu.Unlock()
	return true
}

func (c *Config) write(s Settings) {
	data, err := json.MarshalIndent(map[string]any{"settings": s}, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(c.path), 0755)
	_ = os.WriteFile(c.path, append(data, '\n'), 0644)
}

// ApplyTheme 动态应用主题设置
func ApplyTheme(theme string) {
	switch theme {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// stripJSONC 去除 JSONC 的注释与尾随逗号 (直接复用 godiff 的高效实现)
func stripJSONC(data []byte) []byte {
	var out bytes.Buffer
	inString := false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out.WriteByte(ch)
			if ch == '\\' && i+1 < len(data) {
				i++
				out.WriteByte(data[i])
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch {
		case ch == '"':
			inString = true
			out.WriteByte(ch)
		case ch == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case ch == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++
		case ch == ',':
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
			out.WriteByte(ch)
		default:
			out.WriteByte(ch)
		}
	}
	return out.Bytes()
}
