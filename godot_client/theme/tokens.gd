extends RefCounted
## 视觉 token 的唯一真相：颜色、字号、间距、字体。
##
## 界面脚本一律从这里取值，不许写死十六进制颜色或裸字号 —— 这是五个界面能保持一致的
## 唯一保证（改一处配色不会漏掉某个界面）。`theme/build_theme.gd` 会把这里的值写进
## `tactical_theme.tres`，`tests/theme_test.gd` 会校验两者一致。
##
## 风格：暗色战术风（近黑底 + 琥珀强调 + 硬边描边 + 等宽数字）。

# ---- 颜色 ----
const BG := Color("0f1114")            # 页面最深底色
const SURFACE := Color("14161a")       # 面板外框
const PANEL := Color("1b1e23")         # 卡片 / 行
const PANEL_ALT := Color("171a1f")     # 侧栏 / 顶栏
const BORDER := Color("2a2e35")
const BORDER_STRONG := Color("3a4048")
const TEXT := Color("e8e6e3")
const TEXT_MUTED := Color("7d8590")
const TEXT_DIM := Color("4a5058")
const ACCENT := Color("ffa028")
const ON_ACCENT := Color("14161a")     # 琥珀底上的文字
const SUCCESS := Color("6ee7a8")
const DANGER := Color("ff6b6b")
const SCRIM := Color(0, 0, 0, 0.62)                    # 面板外遮罩
const SCRIM_STRONG := Color(0.03, 0.035, 0.043, 0.86)  # 结算层遮罩

# ---- 字号档位 ----
const FONT_LABEL := 12
const FONT_BODY := 14
const FONT_SUB := 18
const FONT_TITLE := 24
const FONT_HERO := 34

# ---- 间距刻度 ----
const SP_1 := 4
const SP_2 := 8
const SP_3 := 12
const SP_4 := 16
const SP_5 := 24

## body_font 返回正文字体：显式声明的系统字体回退链（不往仓库塞字体文件）。
## 企业级界面不能依赖 Godot 默认字体（它只有拉丁子集，中文靠系统兜底、字形不可控）。
static func body_font() -> SystemFont:
	var f := SystemFont.new()
	f.font_names = PackedStringArray([
		"Microsoft YaHei UI", "Noto Sans CJK SC", "PingFang SC", "Sans-Serif",
	])
	return f

## mono_font 返回数字 / 等宽字体：统计数字、比分、时长、队列秒数都用它 ——
## 战术感的一半来自等宽数字。
static func mono_font() -> SystemFont:
	var f := SystemFont.new()
	f.font_names = PackedStringArray(["Consolas", "DejaVu Sans Mono", "Monospace"])
	return f
