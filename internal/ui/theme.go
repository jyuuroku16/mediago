package ui

import (
	"runtime"

	"github.com/egoist/mygo/ui"
)

// Palette 定义符合 macOS Apple HIG 的调色板
type Palette struct {
	SidebarBg     ui.Color // 侧栏背景 (macOS 上为透明以透出磨砂玻璃)
	ContentBg     ui.Color // 主工作区底色
	CardBg        ui.Color // 任务卡片背景
	CardBorder    ui.Color // 卡片边框微轮廓
	CardHover     ui.Color // 卡片悬停微背景
	HeaderBg      ui.Color // 顶部 Toolbar 背景
	HeaderBorder  ui.Color // 分隔线
	TextPrimary   ui.Color // 标题主字色
	TextSecondary ui.Color // 次级辅助字色
	TextMuted     ui.Color // 弱化字色
	Accent        ui.Color // 系统强调色 (Apple Blue)
	AccentLight   ui.Color // 强调色微弱半透明 (胶囊背景)
	Success       ui.Color // 成功绿色
	SuccessLight  ui.Color // 成功绿色弱化背景
	Warning       ui.Color // 警告橙色
	WarningLight  ui.Color // 警告橙色弱化背景
	Danger        ui.Color // 危险红色
	DangerLight   ui.Color // 危险红色弱化背景
	ProgressTrack ui.Color // 进度条槽底色
	InputBg       ui.Color // 搜索框/输入框底色
	InputBorder   ui.Color // 输入框边框
}

var (
	lightPalette = Palette{
		SidebarBg:     ui.Transparent,
		ContentBg:     ui.Hex("#f5f5f7"),
		CardBg:        ui.Hex("#ffffff"),
		CardBorder:    ui.RGBA(0, 0, 0, 0.09),
		CardHover:     ui.RGBA(0, 0, 0, 0.02),
		HeaderBg:      ui.RGBA(255, 255, 255, 0.75),
		HeaderBorder:  ui.RGBA(0, 0, 0, 0.08),
		TextPrimary:   ui.Hex("#1d1d1f"),
		TextSecondary: ui.Hex("#86868b"),
		TextMuted:     ui.Hex("#a1a1a6"),
		Accent:        ui.Hex("#0071e3"),
		AccentLight:   ui.RGBA(0, 113, 227, 0.12),
		Success:       ui.Hex("#34c759"),
		SuccessLight:  ui.RGBA(52, 199, 89, 0.14),
		Warning:       ui.Hex("#ff9500"),
		WarningLight:  ui.RGBA(255, 149, 0, 0.14),
		Danger:        ui.Hex("#ff3b30"),
		DangerLight:   ui.RGBA(255, 59, 48, 0.14),
		ProgressTrack: ui.Hex("#e5e5ea"),
		InputBg:       ui.RGBA(0, 0, 0, 0.06),
		InputBorder:   ui.RGBA(0, 0, 0, 0.08),
	}

	darkPalette = Palette{
		SidebarBg:     ui.Transparent,
		ContentBg:     ui.Hex("#1a1a1c"),
		CardBg:        ui.Hex("#262629"),
		CardBorder:    ui.RGBA(255, 255, 255, 0.09),
		CardHover:     ui.RGBA(255, 255, 255, 0.03),
		HeaderBg:      ui.RGBA(38, 38, 41, 0.75),
		HeaderBorder:  ui.RGBA(255, 255, 255, 0.08),
		TextPrimary:   ui.Hex("#ffffff"),
		TextSecondary: ui.Hex("#98989d"),
		TextMuted:     ui.Hex("#6e6e73"),
		Accent:        ui.Hex("#2997ff"),
		AccentLight:   ui.RGBA(41, 151, 255, 0.20),
		Success:       ui.Hex("#30d158"),
		SuccessLight:  ui.RGBA(48, 209, 88, 0.20),
		Warning:       ui.Hex("#ff9f0a"),
		WarningLight:  ui.RGBA(255, 159, 10, 0.20),
		Danger:        ui.Hex("#ff453a"),
		DangerLight:   ui.RGBA(255, 69, 58, 0.20),
		ProgressTrack: ui.Hex("#3a3a3c"),
		InputBg:       ui.RGBA(255, 255, 255, 0.08),
		InputBorder:   ui.RGBA(255, 255, 255, 0.10),
	}
)

func getPalette(t *ui.Theme) Palette {
	p := lightPalette
	if t.Dark {
		p = darkPalette
	}

	// 动态跟随 macOS 操作系统 Accent Color (用户在系统设置->外观->强调色中所选)
	if t != nil && t.Accent.A > 0 {
		p.Accent = t.Accent
		if t.Dark {
			p.AccentLight = t.Accent.Alpha(0.20)
		} else {
			p.AccentLight = t.Accent.Alpha(0.12)
		}
	}

	if runtime.GOOS != "darwin" {
		if t.Dark {
			p.SidebarBg = ui.Hex("#242426")
		} else {
			p.SidebarBg = ui.Hex("#ebebed")
		}
	}
	return p
}
