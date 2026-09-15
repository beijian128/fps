extends Control
## 全局背景层：大厅与各子界面的同一张底图 + 一层压暗。
##
## 它是 screen_manager 建屏幕时的**第一**个子节点，所以永远在所有界面之下；登录页与结算层各有
## 自己的遮罩，HUD 是半透明浮层 —— 谁都不会被它挡住。
##
## 底图由 tools/gen_art.py 生成（程序化），换图只替换 assets/ui/bg_lobby.png，代码不动。

const Tokens := preload("res://theme/tokens.gd")

func _ready() -> void:
	# 压暗一层：底图里已经有一道琥珀斜光，不压暗的话卡片上的次要文字会读不清。
	%Scrim.color = Color(Tokens.BG, 0.55)
