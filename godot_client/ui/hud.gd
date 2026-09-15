extends Control
## 对局内 HUD：准星、自己血条与 K/D、对手血条与 K/D、回合进度（先到 10 杀）、击杀播报。
##
## 数据全部来自同步属性（按属性名从 WorldStore 读），不新增任何上行消息。

func update_from_world(_store: Node, _my_slot: int, _fps: float) -> void:
	pass

## notify_hit_landed 由 main 在「对手掉血」时调用（那是唯一的命中判定点），
## 用来把准星短暂点亮。Task 7 实现具体表现。
func notify_hit_landed() -> void:
	pass
