extends RefCounted
## 设计 token：颜色、圆角、字号、间距、字体的取值来源。
##
## `theme/build_theme.gd` 把这里的值写进 `theme/tactical_theme.tres`；界面通过
## `theme_type_variation` 取样式，只有「必须参与运算」的地方（按血量比例变色、按用户名
## 生成头像色）才 import 本文件。`tests/theme_test.gd` 校验生成物与这里一致。
##
## 风格：暗色战术风 —— 近黑分层底色 + 琥珀强调 + 小圆角 + 等宽数字。
## 与旧版的差别是圆角与分层：旧版全直角、面板同色，界面像一张表单；这一版给控件 3–10px
## 圆角并按「底 / 壳 / 卡 / 悬停」四层拉开明度，层级靠色阶而不是靠描边数量表达。

# ---- 颜色：底色四层 ----
const BG := Color("0b0d10")            # 页面最深底色
const SURFACE := Color("12151a")       # 面板外框 / 侧栏 / 顶栏
const PANEL := Color("171b21")         # 卡片 / 行
const PANEL_ALT := Color("1d222a")     # 悬停态 / 选中底

# ---- 颜色：描边与分隔 ----
const BORDER := Color("262c34")
const BORDER_STRONG := Color("3a424e")

# ---- 颜色：文字 ----
const TEXT := Color("e8eaed")
const TEXT_MUTED := Color("8b95a3")    # 次要说明
const TEXT_DIM := Color("55606d")      # 占位 / 禁用

# ---- 颜色：语义色 ----
const ACCENT := Color("ffb020")        # 品牌琥珀：主按钮 / 焦点 / 当前项
const ON_ACCENT := Color("12151a")     # 琥珀底上的文字
const SUCCESS := Color("4ec27a")
const DANGER := Color("ff5f56")
const INFO := Color("4aa3ff")          # 中立提示

# ---- 遮罩 ----
const SCRIM := Color(0, 0, 0, 0.62)                    # 面板外遮罩
const SCRIM_STRONG := Color(0.02, 0.025, 0.03, 0.88)   # 结算层遮罩

# ---- 圆角 ----
const RADIUS_SM := 3                    # 血条 / 进度条 / 徽标
const RADIUS_MD := 6                    # 卡片 / 面板 / 输入框
const RADIUS_LG := 10                   # 顶层对话框

# ---- 字号档位 ----
const FONT_CAPTION := 11
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
const SP_6 := 32

# ---- 控件尺寸 ----
## 可点区域最小高度：44px 是触屏底线，桌面同样受益（目标太小 = 精确操作）。
const HIT_MIN := 44
const BUTTON_H := 40

## body_font 返回正文字体：显式声明的系统字体回退链（不往仓库塞字体文件）。
## 企业级界面不能依赖 Godot 默认字体（它只有拉丁子集，中文靠系统兜底、字形不可控）。
static func body_font() -> SystemFont:
	var f := SystemFont.new()
	f.font_names = PackedStringArray([
		"Microsoft YaHei UI", "Noto Sans CJK SC", "PingFang SC", "Sans-Serif",
	])
	return f

## mono_font 返回数字 / 等宽字体：统计数字、比分、时长、队列秒数都用它 —— 战术感的一半来自等宽数字。
static func mono_font() -> SystemFont:
	var f := SystemFont.new()
	f.font_names = PackedStringArray(["Consolas", "DejaVu Sans Mono", "Monospace"])
	return f

## health_color 按剩余比例给出血条颜色：绿 → 琥珀 → 红。
## 放在 tokens 而不是 HUD 里，是因为「多少算低血」是设计决定，不是某个控件的实现细节。
static func health_color(ratio: float) -> Color:
	if ratio <= 0.3:
		return DANGER
	if ratio <= 0.6:
		return ACCENT
	return SUCCESS
