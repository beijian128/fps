// Package bot 是机器人身份的唯一真相。
//
// 机器人以「队列里的普通成员」的身份进对局：它没有客户端、没有 gate、没有会话，
// 唯一能把它和真人区分开的就是 uid 前缀。有两个服务必须对同一条约定达成一致：
//
//   - match：配对时跳过「读在线登记 + 请 gate 写会话数据」（机器人必然两者皆无，
//     照真人路径走会被 100% 当成掉线剔除）；
//   - game：建实例时认出「这个槽位没有客户端」，从而不推帧、不进注册表、不参与回局。
//
// 判据写两遍就是两份真相，改一处漏一处，所以它单独成包。本包不 import 任何
// 本仓库的包，也不含任何业务逻辑。
package bot

import "strings"

// Prefix 是机器人 uid 的前缀。真人的 uid 是纯十进制的 accountID（见 account 服务），
// 永远撞不上这个前缀，因此 Is 不会误判真人。
const Prefix = "bot:"

// New 用给定的 n 拼出一个机器人 uid。n 由调用方生成（match 里用 nuid），
// 本包不关心它怎么来，只保证前缀。uid 本身是不透明的：配对、推送、注册表
// 全都只把它当字符串用，因此这里不需要全局计数器。
func New(n string) string { return Prefix + n }

// Is 报告 uid 是否是机器人 uid。前缀必须出现在开头，大小写敏感。
func Is(uid string) bool { return strings.HasPrefix(uid, Prefix) }
