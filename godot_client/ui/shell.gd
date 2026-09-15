extends Control
## 大厅外壳：左侧竖排导航 + 顶栏（用户名/等级/金币）+ 内容插槽 + 底部匹配状态条。
##
## 它自己不发光：数据从 `screen_manager` 读（`profile()` / `logic_state()`），导航点击只是把
## 意图交给 `screen_manager`。页面内容按 `current_page()` 挂进 `%Content`。

# 由后续任务补全（Task 3）。
