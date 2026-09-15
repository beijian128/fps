package match

import (
	"context"
	"crypto/subtle"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
)

// 本文件是 match 侧管理入口的两道闸，只服务于 GM 指令（当前只有 addbots）。
//
// 为什么需要两道：
//
//  1. route 本身不是权限。gate 把 match.* 按前缀转给 match 节点，所以**客户端**
//     发出的 match.match.addbots 会真的到达这里（RPCType_Sys，ctx 里带 Remote
//     会话）；而后端 gm 用 app.RPCTo 发的（RPCType_User）**不带会话**。
//     判据是「ctx 里有没有会话」。
//  2. 只有第 1 条不够：任何后端都能调这条 route。密钥是第二道闸，缺一不可。

// isClientCall 判断这次调用是否来自客户端（而非后端 RPC）。
//
// 与 game 包里的同名函数判据完全一致：客户端经 gate 转发时 pitaya 会在 ctx 里放一个
// Remote 会话，后端 RPCTo 不会。偏严是刻意的 —— 将来若有后端改走带会话的路径，
// 会在日志里被明确拒绝，而不是静默放过一个管理入口。
func isClientCall(ctx context.Context, app pitaya.Pitaya) bool {
	return app != nil && app.GetSessionFromCtx(ctx) != nil
}

// adminKeyAllowed 报告请求携带的管理密钥是否等于本节点配置的密钥。
//
// 空密钥一律拒绝：**没配密钥 = 不提供服务**，而不是「谁都能发指令」。
// 比对走常数时间（subtle.ConstantTimeCompare）—— 这里比的是运维密钥，
// 不该用 == 泄漏「前几个字符对不对」。
func adminKeyAllowed(secret string, provided string) bool {
	if secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(provided)) == 1
}
