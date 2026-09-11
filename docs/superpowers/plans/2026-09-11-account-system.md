# 账号体系实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给服务端加上用户名/密码账号体系，服务端签发可吊销凭证，四个服务角色的节点本地状态全部清零，账号/凭证/会话归属/配对队列都放 Redis。

**Architecture:** 新增第 4 个角色 `account`（`-type account`）承担唯一的鉴权边界；登录走 pitaya 的 Request/Response 路径（NATS 下 uid 未绑定时 Push 必然失败）；登录成功即 `s.Bind(ctx, accountID)`，会话 UID 从此是 accountID，`game.rejoin` / 推送 / 实例索引全部零改动。gate 写一条 best-effort 的 `online:{accountID} → gateID` 归属登记，match 靠它定点请 gate 写会话数据。

**Tech Stack:** Go 1.26 + pitaya v3（内置 `third_party/pitaya`）+ go-redis/v9 + bcrypt + miniredis（测试）；客户端 Godot 4.7 / GDScript。

**依据 spec:** `docs/superpowers/specs/2026-09-11-account-system-design.md`

## Global Constraints

- **依赖版本**（已验证可获取，且传递依赖都在本地模块缓存里）：`github.com/redis/go-redis/v9 v9.7.3`、`github.com/alicebob/miniredis/v2 v2.39.0`（测试）、`golang.org/x/crypto`（bcrypt，已在 go.mod 里作 indirect）。
- **提交规范**：本仓库**直接提交 `main`**，不开 feature branch、不走 PR；提交信息 `<type>: <subject>`（feat/fix/docs/refactor/test），AI 提交结尾加 `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`。
- **文档同步**：改代码必须同步文档（`AGENTS.md` 的硬约定）。
- **gofmt**：存量文件是 CRLF，`gofmt -l .` 在干净 main 上也会标红——**只看自己碰过的文件**，不要试图修全仓库。
- **测试要带 PATH**：`./game` 与 `./physics` 需要 `libjolt_c.dll`，命令前加 `PATH="$PWD:$PATH"`。
- **route 一律三段式**：`server.service.method`。
- **protoc 生成**：`protoc` 在 PATH 上（libprotoc 35.1），但 `protoc-gen-go.exe` 在 `$(go env GOPATH)/bin` **不在 PATH**，命令必须带 `PATH="$(go env GOPATH)/bin:$PATH"`。已验证重新生成当前 proto 是字节级可复现的。
- **Redis 数据目录不清空**（与 etcd 相反）：etcd 里只有服务发现这种瞬时状态，Redis 里是账号。

### 与 spec 的两处偏离（实施时按本计划，文档任务里一并说明）

1. **限流键从 IP 改为用户名**：spec §5 写的是 `rl:ip:{ip}`，但 account 服务拿不到客户端 IP——后端 agent 是 `Remote`，`RemoteAddr()` 返回 nil（`third_party/pitaya/pkg/agent/agent_remote.go:157`），会话上也没有 frontendID 的 getter。改成 `rl:user:{规范化用户名}`，粒度是「每账号每分钟 10 次」。这实际上比 IP 更抗分布式暴力破解（换 IP 无效）。
2. **新增 `joltgo/online/` 包**：spec §10 的文件结构里只有 `kv/`。会话归属登记被 gate（写）、account（读）、match（读）三个角色共用，放进 `account` 会让 gate/match 反向依赖 account 包，所以独立成 `online/`。

### 分阶段可用性说明

Task 1 之后、Task 7 之前，**集群端到端不可用**：`match.Join` 改读会话 UID 后，会话永远未绑定（account 服务还没接入），Join 会被静默忽略。这是分阶段实施的预期状态——每个任务自身的单测是绿的，端到端在 Task 7 完成后恢复。

---

## 文件结构

```
joltgo/
├── kv/redis.go            # 新增：Redis 客户端构造（唯一入口）
├── online/online.go       # 新增：会话归属登记（gate 写 / account+match 读）
├── online/online_test.go  # 新增
├── account/
│   ├── token.go           # 新增：token 生成/校验、bcrypt、用户名密码格式校验（纯函数）
│   ├── token_test.go      # 新增
│   ├── store.go           # 新增：账号 + 凭证的 Redis 读写
│   ├── store_test.go      # 新增
│   ├── component.go       # 新增：register/login/resume 三个 handler
│   └── component_test.go  # 新增
├── gate/
│   ├── gate.go            # 改：加 account.* 轮询路由
│   └── session.go         # 新增：会话归属钩子 + bindgame handler（含客户端守卫）
├── match/
│   ├── queue.go           # 新增：Redis 配对队列（ZSET + 两段 Lua）
│   ├── queue_test.go      # 新增
│   ├── match.go           # 改：Join 读会话 UID；开局链路按 uid 定址
│   └── match_test.go      # 改：删除 removeQueued 用例，改造 Join 用例
├── game/protos/game.proto # 改：4 条新消息 + JoinMsg 清空 + 2 条 bindgame 消息
├── main.go                # 改：第 4 个角色 + -redis flag + 各角色 Redis 装配
└── deploy/
    ├── start-infra.ps1    # 改：加起 Redis
    ├── start-all.ps1      # 改：加第 4 个进程
    └── README.md          # 改：Redis 一节

godot_client/
├── scripts/fps_client.gd  # 改：Response 编解码 + 登录流程
├── scripts/main.gd        # 改：登录面板 + 鼠标捕获守卫
└── tests/
    ├── login_reply_decode_test.gd  # 新增：Response 帧 + LoginReply 解码
    └── login_smoke.gd              # 新增：端到端冒烟（需活集群）
```

---

## Task 1: 协议变更 + match.Join 改读会话 UID

**Files:**
- Modify: `joltgo/game/protos/game.proto`
- Regenerate: `joltgo/game/protos/game.pb.go`
- Modify: `joltgo/match/match.go`（`Join` 函数）
- Modify: `joltgo/match/match_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `protos.RegisterMsg{Username, Password string}`、`protos.LoginMsg{Username, Password string}`、`protos.ResumeMsg{Token string}`、`protos.LoginReply{Ok bool; Token, Username, AccountId, Reason string}`、`protos.JoinMsg{}`（空）、`protos.BindGameMsg{Uid, GameServerId, MatchId string; PlayerIdx int32}`、`protos.BindGameReply{Found bool}`

- [ ] **Step 1: 改 proto**

在 `joltgo/game/protos/game.proto` 里，把 `JoinMsg` 替换成空消息，并追加新消息（放在 `JoinMsg` 之后）：

```proto
// JoinMsg 是加入匹配的请求。身份来自会话绑定（account 服务在登录时做的），
// 这里不再携带任何凭证 —— 字段清空后 route 与 handler 签名都不变。
message JoinMsg {}

// RegisterMsg 是注册请求。用户名 3-16 位 [a-zA-Z0-9_]，密码 6-64 位。
message RegisterMsg {
  string username = 1;
  string password = 2;
}

// LoginMsg 是登录请求。
message LoginMsg {
  string username = 1;
  string password = 2;
}

// ResumeMsg 是凭证恢复请求：客户端持有 token，换回会话。
message ResumeMsg {
  string token = 1;
}

// LoginReply 是 register / login / resume 的统一应答，客户端用同一段代码处理。
// ok=false 时 reason 是机器可读的原因码（bad_credentials / name_taken /
// bad_username / bad_password / rate_limited / token_invalid / internal）。
message LoginReply {
  bool ok = 1;
  string token = 2;      // ok=true 时有效
  string username = 3;   // 保留用户输入的原始大小写，供 UI 显示
  string account_id = 4; // 十进制字符串，仅供诊断
  string reason = 5;     // ok=false 时的原因码
}

// BindGameMsg 是 match → gate 的 RPC 请求（route "gate.gate.bindgame"）：请玩家
// 所属的 gate 把对局归属写进它自己的会话数据（gate 据 gameServerId 定点路由
// game.*）。game_server_id 为空表示回滚（清掉归属）。
message BindGameMsg {
  string uid = 1;
  string game_server_id = 2;
  string match_id = 3;
  int32 player_idx = 4;
}

// BindGameReply 是 gate.gate.bindgame 的应答。found=false 表示该 gate 上没有
// 这个会话（玩家已掉线）。
message BindGameReply {
  bool found = 1;
}
```

同时更新文件顶部的注释块，把第一行改成：

```proto
//   - 客户端 → gate → account：account.register / account.login / account.resume
//     （Request/Response，不是 Notify/Push —— 见 docs/API.md）
```

- [ ] **Step 2: 重新生成 Go 码**

```bash
cd joltgo
PATH="$(go env GOPATH)/bin:$PATH" protoc --go_out=. --go_opt=paths=source_relative -I . game/protos/game.proto
```

Expected: 无输出（成功）。验证新类型已生成：

```bash
grep -c "type LoginReply struct\|type BindGameMsg struct\|type ResumeMsg struct" game/protos/game.pb.go
```

Expected: `3`

- [ ] **Step 3: 先跑测试，看它怎么失败**

```bash
cd joltgo && PATH="$PWD:$PATH" go build ./... 2>&1 | head -20
```

Expected: 编译失败，`match/match.go` 报 `msg.Token undefined (type *protos.JoinMsg has no field or method Token)`。

- [ ] **Step 4: 改 Join 读会话 UID**

把 `joltgo/match/match.go` 的 `Join` 整个函数替换成：

```go
// Join 是远端 RPC handler（route "match.join"）：把已登录的会话加入匹配队列。
//
// 身份来自会话绑定 —— account 服务在登录成功时做过 s.Bind(ctx, accountID)，
// 这里只读会话 UID，不再看任何客户端传来的凭证。未绑定的会话说明客户端没登录
// （或登录失败后擅自发了 join），静默忽略并记日志：客户端契约上不会这样，
// 「未登录」这个状态客户端自己知道，不需要服务端告诉它。
func (c *Component) Join(ctx context.Context, msg *protos.JoinMsg) {
	s := c.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		log.Printf("match: join rejected: session not bound")
		return
	}

	// 回局优先：存量对局还在就直接回去，不入匹配队列。
	if c.tryRejoin(ctx, s, uid) {
		return
	}

	c.mu.Lock()
	// 同一个 uid 在**排队等待期间**断线重连会再走一次 Join。不去重的话队列
	// 里会留下两条同 uid 的记录：tryMatch 可能把它们俩配成一对（game.create
	// 的 Uids 变成 [T, T]，一个人占满两个槽位），单人兜底时更会给同一个人
	// 先后开两局、推两条 onMatched。
	c.queue = removeQueued(c.queue, uid)
	c.queue = append(c.queue, queuedPlayer{uid: uid, session: s, joinedAt: time.Now()})
	c.mu.Unlock()

	c.tryMatch()
}
```

同时删掉 `Join` 里原来用到的 `nuid` import（如果 `nuid` 在文件里没有别处使用——`startMatch` 里还有 `nuid.New().Next()`，所以 **import 保留**）。

- [ ] **Step 5: 改 match 的测试**

`joltgo/match/match_test.go` 里 `joinTestSession` 现在要能返回 uid（不再靠 `Bind` 写入）。把该类型和 `TestJoinDedupsQueuedToken` 替换成：

```go
// joinTestSession 只实现 Join 用到的 UID。
type joinTestSession struct {
	session.Session
	uid string
}

func (s *joinTestSession) UID() string { return s.uid }

// 排队等待期间断线重连：同一个 uid 再 Join 一次，队列里必须还是只有一条
// （不去重的话这里会是 2 条，进而可能自己跟自己配对、或单人兜底时开两局）。
func TestJoinDedupsQueuedUid(t *testing.T) {
	c := New(&joinTestApp{sess: &joinTestSession{uid: "T"}})
	ctx := context.Background()

	c.Join(ctx, &protos.JoinMsg{})
	if len(c.queue) != 1 {
		t.Fatalf("首次 Join 应入队一条，得到 %d", len(c.queue))
	}
	c.Join(ctx, &protos.JoinMsg{})
	if len(c.queue) != 1 {
		t.Fatalf("同 uid 重连不应重复入队，得到 %d", len(c.queue))
	}
	if c.queue[0].uid != "T" {
		t.Fatalf("队列里应是最新那条会话，得到 %q", c.queue[0].uid)
	}
}

// 未登录（会话未绑定）的 Join 必须被忽略：不 Bind、不入队。
func TestJoinRejectsUnboundSession(t *testing.T) {
	c := New(&joinTestApp{sess: &joinTestSession{uid: ""}})
	c.Join(context.Background(), &protos.JoinMsg{})
	if len(c.queue) != 0 {
		t.Fatalf("未绑定会话不应入队，得到 %d 条", len(c.queue))
	}
}
```

`joinTestApp` 保持不变（它已经只覆盖 `GetSessionFromCtx` 与 `GetServersByType`）。

- [ ] **Step 6: 跑测试确认通过**

```bash
cd joltgo && PATH="$PWD:$PATH" go build ./... && PATH="$PWD:$PATH" go test -count=1 ./match
```

Expected: `ok  	joltgo/match`

- [ ] **Step 7: 确认自己碰过的文件格式没问题**

```bash
cd joltgo && gofmt -l match game/protos
```

Expected: 只可能列出 `match/match.go`（存量 CRLF）——若列出别的文件则是新增格式问题，必须修。

- [ ] **Step 8: 提交**

```bash
git add joltgo/game/protos/game.proto joltgo/game/protos/game.pb.go joltgo/match/match.go joltgo/match/match_test.go
git commit -m "feat(proto): 账号协议消息；match.Join 改读会话 UID

JoinMsg 清空 token 字段，身份改由会话绑定提供（account 服务在登录时
做 s.Bind）。新增 Register/Login/Resume/LoginReply 与 bindgame 两条 RPC
消息。match.Join 未绑定会话时静默忽略。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: account/token.go（纯函数层）

**Files:**
- Create: `joltgo/account/token.go`
- Test: `joltgo/account/token_test.go`

**Interfaces:**
- Consumes: `golang.org/x/crypto/bcrypt`
- Produces:
  - `account.NewToken() (string, error)`
  - `account.ValidateToken(t string) error`
  - `account.HashPassword(pw string) (string, error)`
  - `account.CheckPassword(hash, pw string) bool`
  - `account.ValidateUsername(u string) error`
  - `account.ValidatePassword(pw string) error`
  - `account.NormalizeUsername(u string) string`
  - 常量 `TokenBytes = 32`、`MinUsernameLen = 3`、`MaxUsernameLen = 16`、`MinPasswordLen = 6`、`MaxPasswordLen = 64`
  - 错误 `ErrBadUsername`、`ErrBadPassword`、`ErrBadToken`

- [ ] **Step 1: 写失败的测试**

创建 `joltgo/account/token_test.go`：

```go
package account

import (
	"strings"
	"testing"
)

func TestNewTokenIsRandomAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken 不该报错: %v", err)
		}
		if err := ValidateToken(tok); err != nil {
			t.Fatalf("刚生成的 token 应通过校验，得到 %q: %v", tok, err)
		}
		if seen[tok] {
			t.Fatalf("token 重复了: %q", tok)
		}
		seen[tok] = true
	}
}

func TestValidateTokenRejectsGarbage(t *testing.T) {
	bad := []string{
		"",
		"short",
		strings.Repeat("a", 42),                       // 合法 base64url，但解码只有 31 字节
		strings.Repeat("a", 44),                       // 解码 33 字节
		strings.Repeat("!", 43),                       // 非法字符
	}
	for _, s := range bad {
		if err := ValidateToken(s); err == nil {
			t.Fatalf("%q 应被判为非法 token", s)
		}
	}
}

// 边界：43 个 base64url 字符解码正好是 32 字节，必须被接受 ——
// 别把它写成「非法」用例（那会让上面的测试以错误理由通过）。
func TestValidateTokenAcceptsExactly32Bytes(t *testing.T) {
	s := strings.Repeat("a", 43)
	if err := ValidateToken(s); err != nil {
		t.Fatalf("43 字符 = 32 字节，应是合法形态，得到 %v", err)
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword 不该报错: %v", err)
	}
	if h == "hunter2" {
		t.Fatal("哈希不能等于明文")
	}
	if !CheckPassword(h, "hunter2") {
		t.Fatal("正确密码应校验通过")
	}
	if CheckPassword(h, "hunter3") {
		t.Fatal("错误密码不应通过")
	}
	// bcrypt 加了随机盐，同一密码两次哈希必须不同。
	h2, _ := HashPassword("hunter2")
	if h == h2 {
		t.Fatal("两次哈希相同说明没有加盐")
	}
}

func TestHashPasswordRejectsBadLength(t *testing.T) {
	for _, pw := range []string{"", "12345", strings.Repeat("x", 65)} {
		if _, err := HashPassword(pw); err == nil {
			t.Fatalf("%d 位的密码应被拒绝", len(pw))
		}
	}
}

func TestValidateUsername(t *testing.T) {
	ok := []string{"abc", "a_1", "User123", strings.Repeat("a", 16)}
	for _, u := range ok {
		if err := ValidateUsername(u); err != nil {
			t.Fatalf("%q 应是合法用户名: %v", u, err)
		}
	}
	bad := []string{"", "ab", strings.Repeat("a", 17), "a b", "a-b", "用户名", "a.b"}
	for _, u := range bad {
		if err := ValidateUsername(u); err == nil {
			t.Fatalf("%q 应被判为非法用户名", u)
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	if got := NormalizeUsername("Alice"); got != "alice" {
		t.Fatalf("应转小写，得到 %q", got)
	}
	if got := NormalizeUsername("a_b_1"); got != "a_b_1" {
		t.Fatalf("已是小写应原样返回，得到 %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./account
```

Expected: FAIL — `undefined: NewToken` 等（包不存在则报 `no Go files` 或 `undefined`）。

- [ ] **Step 3: 写实现**

创建 `joltgo/account/token.go`：

```go
// Package account 实现账号体系：注册、登录、凭证恢复。
//
// 本文件只有纯函数（无 I/O）：凭证生成与校验、密码哈希与校验、用户名密码的
// 格式规则。Redis 读写在同包的 store.go，pitaya handler 在 component.go。
package account

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// 用户名与密码的格式上下限。用户名允许字母/数字/下划线；密码只限长度 ——
// 不强制复杂度（demo 定位，避免造出「必须含数字」这类无意义规则把人挡在门外）。
const (
	MinUsernameLen = 3
	MaxUsernameLen = 16
	MinPasswordLen = 6
	MaxPasswordLen = 64

	// TokenBytes 是凭证的随机字节数（base64url 无填充后 43 字符）。
	TokenBytes = 32
)

var (
	// ErrBadUsername 表示用户名不符合格式要求。
	ErrBadUsername = errors.New("account: bad username")
	// ErrBadPassword 表示密码不符合长度要求。
	ErrBadPassword = errors.New("account: bad password")
	// ErrBadToken 表示凭证字符串形态非法。
	ErrBadToken = errors.New("account: bad token")
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)

// NewToken 生成一个新的会话凭证：32 字节 crypto/rand，base64url 无填充。
// 用不透明随机串而不是 JWT：可随时吊销（DEL 一个键）、无密钥管理、泄露面小，
// 与「状态都在 Redis」的取向一致。
func NewToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateToken 校验凭证字符串的形态（长度与字符集）。
// 这不是安全校验（真伪由 Redis 查表决定），只是避免拿垃圾串去打 Redis。
func ValidateToken(t string) error {
	b, err := base64.RawURLEncoding.DecodeString(t)
	if err != nil || len(b) != TokenBytes {
		return ErrBadToken
	}
	return nil
}

// HashPassword 用 bcrypt 哈希密码（DefaultCost，约 50ms/次）。
func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword 校验密码是否匹配哈希。
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ValidateUsername 校验用户名格式：3-16 位字母/数字/下划线。
func ValidateUsername(u string) error {
	if !usernameRe.MatchString(u) {
		return ErrBadUsername
	}
	return nil
}

// ValidatePassword 校验密码长度：6-64 位。
func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLen || len(pw) > MaxPasswordLen {
		return ErrBadPassword
	}
	return nil
}

// NormalizeUsername 返回用户名的规范化形式，用于占名与查询（大小写不敏感，
// 防「Alice」和「alice」被当成两个账号抢注）。展示用的原始大小写存在账号 Hash 里。
func NormalizeUsername(u string) string {
	return strings.ToLower(u)
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./account
```

Expected: `ok  	joltgo/account`

- [ ] **Step 5: 格式检查**

```bash
cd joltgo && gofmt -l account
```

Expected: 无输出（新文件应是 LF、格式正确）。

- [ ] **Step 6: 提交**

```bash
git add joltgo/account/token.go joltgo/account/token_test.go
git commit -m "feat(account): 凭证生成与密码哈希（纯函数层）

32 字节随机 token + bcrypt(DefaultCost)；用户名 3-16 位字母数字下划线，
密码 6-64 位；用户名规范化大小写无关。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Redis 层（kv + online + account/store）

**Files:**
- Create: `joltgo/kv/redis.go`
- Create: `joltgo/online/online.go`
- Test: `joltgo/online/online_test.go`
- Create: `joltgo/account/store.go`
- Test: `joltgo/account/store_test.go`
- Modify: `joltgo/go.mod`、`joltgo/go.sum`（加依赖）

**Interfaces:**
- Consumes: `account.NewToken`、`account.NormalizeUsername`（Task 2）
- Produces:
  - `kv.Open(ctx context.Context, addr string) (*redis.Client, error)`
  - `online.TTL`（`time.Duration`，24h）、`online.NewStore(rdb *redis.Client) *Store`、`(*Store).Set(ctx, accountID, gateID string) error`、`(*Store).Clear(ctx, accountID string) error`、`(*Store).Gate(ctx, accountID string) (string, error)`
  - `account.NewStore(rdb *redis.Client) *Store`、`account.Account{ID, Username, PassHash string; CreatedAt int64}`、`account.ErrNameTaken`、`account.RateLimit = 10`
  - `(*Store).Create(ctx, username, passHash string) (string, error)`
  - `(*Store).LookupByName(ctx, username string) (string, bool, error)`
  - `(*Store).GetAccount(ctx, id string) (Account, bool, error)`
  - `(*Store).IssueToken(ctx, accountID string) (string, error)`
  - `(*Store).ResolveToken(ctx, token string) (string, bool, error)`
  - `(*Store).RevokeToken(ctx, token string) error`
  - `(*Store).Allow(ctx, username string) (bool, error)`

- [ ] **Step 1: 加依赖**

```bash
cd joltgo && go get github.com/redis/go-redis/v9@v9.7.3 && go get github.com/alicebob/miniredis/v2@v2.39.0
```

Expected: 两个 `go: added ...` 行。若报网络错误，设 `GOPROXY=https://goproxy.cn,direct` 重试。

- [ ] **Step 2: 写失败的测试（online）**

创建 `joltgo/online/online_test.go`：

```go
package online

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewStore(rdb), mr
}

func TestGateUnknownAccountIsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.Gate(context.Background(), "42")
	if err != nil {
		t.Fatalf("查不存在的账号不该报错: %v", err)
	}
	if got != "" {
		t.Fatalf("不存在的账号应返回空串，得到 %q", got)
	}
}

func TestSetAndClear(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.Set(ctx, "42", "gate-1"); err != nil {
		t.Fatalf("Set 报错: %v", err)
	}
	got, err := s.Gate(ctx, "42")
	if err != nil || got != "gate-1" {
		t.Fatalf("应读到 gate-1，得到 %q err=%v", got, err)
	}

	// 同一账号再登记到另一个 gate（换设备登录）应覆盖。
	if err := s.Set(ctx, "42", "gate-2"); err != nil {
		t.Fatalf("Set 覆盖报错: %v", err)
	}
	if got, _ := s.Gate(ctx, "42"); got != "gate-2" {
		t.Fatalf("应覆盖为 gate-2，得到 %q", got)
	}

	if err := s.Clear(ctx, "42"); err != nil {
		t.Fatalf("Clear 报错: %v", err)
	}
	if got, _ := s.Gate(ctx, "42"); got != "" {
		t.Fatalf("Clear 后应为空，得到 %q", got)
	}
}

func TestSetHasTTL(t *testing.T) {
	s, mr := newTestStore(t)
	if err := s.Set(context.Background(), "42", "gate-1"); err != nil {
		t.Fatalf("Set 报错: %v", err)
	}
	if ttl := mr.TTL("online:42"); ttl != TTL {
		t.Fatalf("TTL 应为 %v，得到 %v", TTL, ttl)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./online
```

Expected: FAIL — `undefined: NewStore` / `no Go files in .../online`。

- [ ] **Step 4: 写 kv 与 online 的实现**

创建 `joltgo/kv/redis.go`：

```go
// Package kv 是 Redis 连接的唯一入口：只负责按地址建客户端并探活，
// 不含任何业务键名。各业务包（account / online / match / gate）自己定义键空间。
package kv

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// DefaultAddr 是本地默认地址（与 deploy/redis-server.exe 的监听端口一致）。
const DefaultAddr = "localhost:6379"

// Open 按地址建 Redis 客户端并 Ping 一次。尽早探活是为了让「地址配错」
// 在进程启动时就暴露，而不是等到第一个玩家登录才报错。
func Open(ctx context.Context, addr string) (*redis.Client, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("kv: ping %s: %w", addr, err)
	}
	return c, nil
}
```

创建 `joltgo/online/online.go`：

```go
// Package online 维护「账号当前在线于哪个 gate」这一条登记。
//
// 三个角色用它：gate 在会话绑定/断开时写，account 在顶号时读（定点踢旧连接），
// match 在开局前读（定点请该 gate 写会话数据）。
//
// 它是 best-effort 的：读不到只会降级（少踢一次 / 该玩家被当作掉线剔除），
// 正确性由凭证轮换兜底 —— 被顶掉的客户端拿着已作废的 token，重连也 resume 不回来。
package online

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// TTL 给得很宽（与凭证同量级）。陈旧条目无害：它指向一个已经没有该会话的 gate，
// 定点踢过去会得到 ErrSessionNotFound 并被忽略。真正有害的是「有会话却没登记」——
// 那会让顶号踢不掉、match 的探活把活人当死人，所以宁可留久一点。
const TTL = 24 * time.Hour

// Store 是会话归属的读写层。
type Store struct {
	rdb *redis.Client
}

// NewStore 构造 Store。
func NewStore(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

func key(accountID string) string { return "online:" + accountID }

// Set 登记账号当前在线的 gate 节点（覆盖写：换设备登录就是换个 gate）。
func (s *Store) Set(ctx context.Context, accountID, gateID string) error {
	return s.rdb.Set(ctx, key(accountID), gateID, TTL).Err()
}

// Clear 清除登记（连接断开时调用）。
func (s *Store) Clear(ctx context.Context, accountID string) error {
	return s.rdb.Del(ctx, key(accountID)).Err()
}

// Gate 返回账号当前在线的 gate 节点 id。第二个返回值语义上是「找到了吗」，
// 这里用空串表示未知：调用方对「没登记」与「登记为空」的处理完全一样。
func (s *Store) Gate(ctx context.Context, accountID string) (string, error) {
	v, err := s.rdb.Get(ctx, key(accountID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}
```

- [ ] **Step 5: 跑 online 测试确认通过**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./online
```

Expected: `ok  	joltgo/online`

- [ ] **Step 6: 写失败的测试（store）**

创建 `joltgo/account/store_test.go`：

```go
package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewStore(rdb), mr
}

// createTestAccount 建一个测试账号，返回 accountID。
func createTestAccount(t *testing.T, s *Store, username, pw string) string {
	t.Helper()
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword 报错: %v", err)
	}
	id, err := s.Create(context.Background(), username, hash)
	if err != nil {
		t.Fatalf("Create 报错: %v", err)
	}
	return id
}

func TestCreateAssignsIncreasingIDs(t *testing.T) {
	s, _ := newTestStore(t)
	a := createTestAccount(t, s, "alice", "hunter2")
	b := createTestAccount(t, s, "bob", "hunter2")
	if a == b {
		t.Fatalf("两个账号的 id 不能相同，都是 %q", a)
	}
	if a != "1" || b != "2" {
		t.Fatalf("id 应是从 1 开始递增的十进制串，得到 %q / %q", a, b)
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	s, _ := newTestStore(t)
	createTestAccount(t, s, "alice", "hunter2")

	hash, _ := HashPassword("hunter2")
	if _, err := s.Create(context.Background(), "alice", hash); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("重名应返回 ErrNameTaken，得到 %v", err)
	}
	// 大小写不同也算重名（占名键用规范化用户名）。
	if _, err := s.Create(context.Background(), "ALICE", hash); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("大小写不同的重名也应被拒，得到 %v", err)
	}
}

func TestCreateRollsBackNameOnFailure(t *testing.T) {
	s, mr := newTestStore(t)
	createTestAccount(t, s, "alice", "hunter2")

	// 制造「占名成功、分配 id 失败」这个中间态：把计数器设成非整数，
	// INCR 会报 "ERR value is not an integer or out of range"。
	//
	// 不能用 mr.SetError()：那会让**所有**命令失败，包括最开始的 SETNX ——
	// 名字压根没被占用，回滚分支根本走不到，测试就变成了假绿。
	mr.Set("acct:seq", "notanumber")

	hash, _ := HashPassword("hunter2")
	if _, err := s.Create(context.Background(), "bob", hash); err == nil {
		t.Fatal("INCR 失败时 Create 应报错")
	}
	// 回滚必须发生，否则 bob 会变成「占着名字却没有任何账号」的僵尸名 ——
	// 谁都注册不了它，也谁都登录不了它。
	if mr.Exists("acct:name:bob") {
		t.Fatal("失败后应回滚占名")
	}

	// 计数器修好后应能重新占名。
	mr.Set("acct:seq", "1")
	if id, err := s.Create(context.Background(), "bob", hash); err != nil {
		t.Fatalf("回滚后应能重新占名，得到 %v (id=%s)", err, id)
	}
}

func TestLookupByNameIsCaseInsensitive(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "Alice", "hunter2")

	for _, q := range []string{"Alice", "alice", "ALICE"} {
		got, ok, err := s.LookupByName(context.Background(), q)
		if err != nil || !ok || got != id {
			t.Fatalf("查 %q 应命中 %s，得到 %q ok=%v err=%v", q, id, got, ok, err)
		}
	}

	if _, ok, err := s.LookupByName(context.Background(), "nobody"); ok || err != nil {
		t.Fatalf("不存在的名字应返回 ok=false err=nil，得到 ok=%v err=%v", ok, err)
	}
}

func TestGetAccountKeepsOriginalCase(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "Alice", "hunter2")

	a, ok, err := s.GetAccount(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("应读到账号，得到 ok=%v err=%v", ok, err)
	}
	if a.Username != "Alice" {
		t.Fatalf("应保留原始大小写 Alice，得到 %q", a.Username)
	}
	if !CheckPassword(a.PassHash, "hunter2") {
		t.Fatal("存下来的哈希应能校验原密码")
	}
	if a.CreatedAt == 0 {
		t.Fatal("created_at 应被写入")
	}
}

func TestGetAccountMissing(t *testing.T) {
	s, _ := newTestStore(t)
	if _, ok, err := s.GetAccount(context.Background(), "999"); ok || err != nil {
		t.Fatalf("不存在的账号应 ok=false err=nil，得到 ok=%v err=%v", ok, err)
	}
}

func TestIssueTokenRotatesAndRevokesOld(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	t1, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("IssueToken 报错: %v", err)
	}
	if got, ok, _ := s.ResolveToken(ctx, t1); !ok || got != id {
		t.Fatalf("t1 应解析到 %s，得到 %q ok=%v", id, got, ok)
	}

	t2, err := s.IssueToken(ctx, id)
	if err != nil {
		t.Fatalf("第二次 IssueToken 报错: %v", err)
	}
	if t1 == t2 {
		t.Fatal("轮换必须换出新 token")
	}
	if _, ok, _ := s.ResolveToken(ctx, t1); ok {
		t.Fatal("旧 token 必须立即失效（单会话强制）")
	}
	if got, ok, _ := s.ResolveToken(ctx, t2); !ok || got != id {
		t.Fatalf("t2 应解析到 %s，得到 %q ok=%v", id, got, ok)
	}
}

func TestResolveTokenExtendsTTL(t *testing.T) {
	s, mr := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	tok, _ := s.IssueToken(ctx, id)
	// 让时间走掉大半，再解析一次，TTL 应被续满。
	mr.FastForward(6 * 24 * time.Hour)
	if _, ok, _ := s.ResolveToken(ctx, tok); !ok {
		t.Fatal("6 天后 token 应还有效")
	}
	if ttl := mr.TTL("sess:" + tok); ttl != keySessTTL {
		t.Fatalf("解析后 TTL 应续满 %v，得到 %v", keySessTTL, ttl)
	}
}

func TestResolveTokenExpires(t *testing.T) {
	s, mr := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	tok, _ := s.IssueToken(ctx, id)
	mr.FastForward(keySessTTL + time.Hour)
	if _, ok, _ := s.ResolveToken(ctx, tok); ok {
		t.Fatal("过期 token 不应解析成功")
	}
}

func TestRevokeToken(t *testing.T) {
	s, _ := newTestStore(t)
	id := createTestAccount(t, s, "alice", "hunter2")
	ctx := context.Background()

	tok, _ := s.IssueToken(ctx, id)
	if err := s.RevokeToken(ctx, tok); err != nil {
		t.Fatalf("RevokeToken 报错: %v", err)
	}
	if _, ok, _ := s.ResolveToken(ctx, tok); ok {
		t.Fatal("登出后 token 应失效")
	}
	// 再撤回一次不应报错（登出是幂等的）。
	if err := s.RevokeToken(ctx, tok); err != nil {
		t.Fatalf("重复 RevokeToken 应幂等，得到 %v", err)
	}
	// 撤回后该账号应能重新签发。
	if _, err := s.IssueToken(ctx, id); err != nil {
		t.Fatalf("重新签发应成功，得到 %v", err)
	}
}

func TestAllowRateLimitsPerUsername(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < RateLimit; i++ {
		ok, err := s.Allow(ctx, "alice")
		if err != nil {
			t.Fatalf("第 %d 次 Allow 报错: %v", i+1, err)
		}
		if !ok {
			t.Fatalf("第 %d 次应放行（上限 %d）", i+1, RateLimit)
		}
	}
	if ok, _ := s.Allow(ctx, "alice"); ok {
		t.Fatalf("超过 %d 次后应被限流", RateLimit)
	}

	// 另一个用户名不受影响。
	if ok, _ := s.Allow(ctx, "bob"); !ok {
		t.Fatal("限流应按用户名隔离")
	}

	// 窗口过去后恢复。
	mr.FastForward(keyRateTTL + time.Second)
	if ok, _ := s.Allow(ctx, "alice"); !ok {
		t.Fatal("限流窗口过后应恢复")
	}
}

func TestAllowNormalizesUsername(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < RateLimit; i++ {
		s.Allow(ctx, "Alice")
	}
	// 换个大小写不应绕开限流。
	if ok, _ := s.Allow(ctx, "alice"); ok {
		t.Fatal("大小写不同不应绕开限流（限流键用规范化用户名）")
	}
}
```

- [ ] **Step 7: 跑测试确认失败**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./account
```

Expected: FAIL — `undefined: NewStore`、`undefined: RateLimit`、`undefined: keySessTTL`。

- [ ] **Step 8: 写实现**

创建 `joltgo/account/store.go`：

```go
package account

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// 键空间与参数。集中在这里，避免键名散落在各处拼字符串。
const (
	keySeq = "acct:seq"

	keySessTTL = 7 * 24 * time.Hour // 凭证有效期（每次 resume 续满）
	keyRateTTL = time.Minute        // 限流窗口

	// RateLimit 是每个用户名在 keyRateTTL 窗口内的最大尝试次数。
	//
	// 按**用户名**而不是 IP：account 服务拿不到客户端 IP —— 后端 agent 是
	// pitaya 的 Remote，RemoteAddr() 返回 nil，会话上也没有 frontendID 的
	// getter。按用户名限流其实比按 IP 更抗分布式暴力破解（换 IP 绕不开），
	// 代价是攻击者可以持续刷某个用户名、把他挡在门外（比「账号锁定」轻得多：
	// 只有一分钟窗口，且不影响已登录的会话）。
	RateLimit = 10
)

func keyName(username string) string { return "acct:name:" + NormalizeUsername(username) }
func keyAcct(id string) string       { return "acct:" + id }
func keySess(token string) string    { return "sess:" + token }
func keySessAcct(id string) string   { return "sess:acct:" + id }
func keyRate(username string) string { return "rl:user:" + NormalizeUsername(username) }

// ErrNameTaken 表示用户名已被占用。
var ErrNameTaken = errors.New("account: name taken")

// Account 是一个账号的持久数据（凭证不入此结构，见 keySess）。
type Account struct {
	ID        string
	Username  string // 保留用户输入的原始大小写，供 UI 显示
	PassHash  string // bcrypt
	CreatedAt int64
}

// Store 是账号数据的 Redis 读写层：只做键的存取，不做规则判断
// （格式校验、错误码映射、顶号决策都在 component.go）。
type Store struct {
	rdb *redis.Client
}

// NewStore 构造 Store。
func NewStore(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

// Create 占名并创建账号，返回分配到的 accountID（十进制字符串）。
//
// 用 SETNX 占名而不是「先 GET 再 SET」：多节点/多请求并发下只有 SETNX 能保证
// 只有一个成功。占名键先写空串、拿到 id 后再回填 —— 中间态的空值在
// LookupByName 里被当作「不存在」，不会被误当成有效账号。
//
// 任一步失败都回滚占名，否则会留下「名字被占了、却没有任何账号」的僵尸名。
func (s *Store) Create(ctx context.Context, username, passHash string) (string, error) {
	nameKey := keyName(username)
	ok, err := s.rdb.SetNX(ctx, nameKey, "", 0).Result()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNameTaken
	}

	n, err := s.rdb.Incr(ctx, keySeq).Result()
	if err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		return "", err
	}
	id := strconv.FormatInt(n, 10)

	if err := s.rdb.HSet(ctx, keyAcct(id), map[string]any{
		"username":   username,
		"pass_hash":  passHash,
		"created_at": time.Now().Unix(),
	}).Err(); err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		return "", err
	}

	if err := s.rdb.Set(ctx, nameKey, id, 0).Err(); err != nil {
		_ = s.rdb.Del(ctx, nameKey).Err()
		_ = s.rdb.Del(ctx, keyAcct(id)).Err()
		return "", err
	}
	return id, nil
}

// LookupByName 按用户名查 accountID（大小写不敏感）。第二个返回值表示是否存在。
func (s *Store) LookupByName(ctx context.Context, username string) (string, bool, error) {
	id, err := s.rdb.Get(ctx, keyName(username)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if id == "" {
		return "", false, nil // 占名事务进行中
	}
	return id, true, nil
}

// GetAccount 读账号数据。第二个返回值表示是否存在。
func (s *Store) GetAccount(ctx context.Context, id string) (Account, bool, error) {
	m, err := s.rdb.HGetAll(ctx, keyAcct(id)).Result()
	if err != nil {
		return Account{}, false, err
	}
	if len(m) == 0 {
		return Account{}, false, nil
	}
	created, _ := strconv.ParseInt(m["created_at"], 10, 64)
	return Account{
		ID:        id,
		Username:  m["username"],
		PassHash:  m["pass_hash"],
		CreatedAt: created,
	}, true, nil
}

// IssueToken 签发新凭证，并让该账号的旧凭证立即失效（单会话强制）。
//
// 「轮换」是顶号的**权威**手段：被顶掉的客户端拿着已删除的 token，即使重连
// 也 resume 不回来、只能回到登录面板。定点踢（见 online 包）只负责让它及时
// 闭嘴，不负责正确性。
func (s *Store) IssueToken(ctx context.Context, accountID string) (string, error) {
	old, err := s.rdb.Get(ctx, keySessAcct(accountID)).Result()
	if err != nil && err != redis.Nil {
		return "", err
	}

	token, err := NewToken()
	if err != nil {
		return "", err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, keySess(token), accountID, keySessTTL)
	pipe.Set(ctx, keySessAcct(accountID), token, keySessTTL)
	if old != "" {
		pipe.Del(ctx, keySess(old))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}
	return token, nil
}

// ResolveToken 查凭证对应的 accountID 并续期（活跃用户凭证不过期）。
// 第二个返回值表示凭证是否有效。
func (s *Store) ResolveToken(ctx context.Context, token string) (string, bool, error) {
	id, err := s.rdb.Get(ctx, keySess(token)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// 续期失败不当作认证失败：凭证本身是有效的，只是 TTL 没续上。
	_ = s.rdb.Expire(ctx, keySess(token), keySessTTL).Err()
	return id, true, nil
}

// RevokeToken 删除凭证（登出）。幂等：凭证不存在也算成功。
func (s *Store) RevokeToken(ctx context.Context, token string) error {
	id, err := s.rdb.Get(ctx, keySess(token)).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, keySess(token))
	pipe.Del(ctx, keySessAcct(id))
	_, err = pipe.Exec(ctx)
	return err
}

// Allow 是登录限流：同一用户名在 keyRateTTL 窗口内最多 RateLimit 次。
// 返回 false 表示超限，调用方应回 rate_limited 而不是继续校验密码。
func (s *Store) Allow(ctx context.Context, username string) (bool, error) {
	k := keyRate(username)
	n, err := s.rdb.Incr(ctx, k).Result()
	if err != nil {
		return false, err
	}
	if n == 1 {
		if err := s.rdb.Expire(ctx, k, keyRateTTL).Err(); err != nil {
			return false, err
		}
	}
	return n <= RateLimit, nil
}
```

- [ ] **Step 9: 跑测试确认通过**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./account ./online ./kv
```

Expected: `ok  	joltgo/account`、`ok  	joltgo/online`

- [ ] **Step 10: 收尾依赖**

```bash
cd joltgo && go mod tidy && PATH="$PWD:$PATH" go build ./... && gofmt -l account online kv
```

Expected: `go mod tidy` 把 `golang.org/x/crypto` 提为直接依赖；`gofmt -l` 无输出。

- [ ] **Step 11: 提交**

```bash
git add joltgo/kv joltgo/online joltgo/account joltgo/go.mod joltgo/go.sum
git commit -m "feat(account): Redis 层（连接、会话归属、账号与凭证存储）

kv 只做客户端构造与探活；online 是会话语义归属（gate 写、account/match 读）；
account.Store 做账号 CRUD 与凭证签发/轮换/撤销。限流按用户名而非 IP ——
后端 agent 是 pitaya Remote，拿不到客户端 IP。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: account/component.go（三个 handler）

**Files:**
- Create: `joltgo/account/component.go`
- Test: `joltgo/account/component_test.go`

**Interfaces:**
- Consumes: `account.Store`（Task 3）、`protos.*`（Task 1）、`online.Store`（Task 3）
- Produces:
  - `account.New(app pitaya.Pitaya, store *Store, onl *online.Store) *Component`
  - `(*Component).Register(ctx, msg *protos.RegisterMsg) (*protos.LoginReply, error)`
  - `(*Component).Login(ctx, msg *protos.LoginMsg) (*protos.LoginReply, error)`
  - `(*Component).Resume(ctx, msg *protos.ResumeMsg) (*protos.LoginReply, error)`
  - 原因码常量：`ReasonBadCredentials = "bad_credentials"`、`ReasonNameTaken = "name_taken"`、`ReasonBadUsername = "bad_username"`、`ReasonBadPassword = "bad_password"`、`ReasonRateLimited = "rate_limited"`、`ReasonTokenInvalid = "token_invalid"`、`ReasonInternal = "internal"`

- [ ] **Step 1: 写失败的测试**

创建 `joltgo/account/component_test.go`：

```go
package account

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	// pitaya 的 RPCTo 用的是老的 github.com/golang/protobuf/proto.Message
	// （见 third_party/pitaya/pkg/app.go:32 —— 别换成 google.golang.org/protobuf
	// 的同名类型，那样 fake 就满足不了 pitaya.Pitaya 接口）。
	"github.com/golang/protobuf/proto"
	"github.com/redis/go-redis/v9"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

// fakeSession 嵌入 session.Session 接口：只覆盖组件用到的几个方法，
// 未覆盖的方法一旦被调用就 panic（暴露意料之外的依赖）。
//
// onBind 用来模拟 gate 侧的 after-bind 钩子：真实链路里 Bind 会让 gate 把
// online 登记改写成「自己」。没有它就没法构造「跨 gate 顶号」这个场景 ——
// 而那是本次顶号设计最核心的一条断言。
type fakeSession struct {
	session.Session
	uid     string
	bindErr error
	onBind  func(uid string)
}

func (s *fakeSession) UID() string { return s.uid }

func (s *fakeSession) Bind(_ context.Context, uid string) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	s.uid = uid
	if s.onBind != nil {
		s.onBind(uid)
	}
	return nil
}

// rpcCall 记录一次 RPCTo 调用。
type rpcCall struct {
	serverID string
	route    string
}

// fakeApp 只实现组件用到的部分：GetSessionFromCtx 与 RPCTo。
type fakeApp struct {
	pitaya.Pitaya
	sess  *fakeSession
	calls []rpcCall
}

func (a *fakeApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *fakeApp) RPCTo(_ context.Context, serverID, routeStr string, _ proto.Message, _ proto.Message) error {
	a.calls = append(a.calls, rpcCall{serverID: serverID, route: routeStr})
	return nil
}

// testEnv 把一次测试要碰的东西打包，避免每个用例拖一长串返回值。
type testEnv struct {
	comp   *Component
	store  *Store
	online *online.Store
	app    *fakeApp
	sess   *fakeSession
}

func newTestComponent(t *testing.T) *testEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	sess := &fakeSession{}
	app := &fakeApp{sess: sess}
	store := NewStore(rdb)
	onl := online.NewStore(rdb)
	return &testEnv{
		comp:   New(app, store, onl),
		store:  store,
		online: onl,
		app:    app,
		sess:   sess,
	}
}

// mustRegister 建一个账号（失败直接终止用例）。
func (e *testEnv) mustRegister(t *testing.T, username, password string) {
	t.Helper()
	r, err := e.comp.Register(context.Background(), &protos.RegisterMsg{Username: username, Password: password})
	if err != nil || !r.Ok {
		t.Fatalf("准备账号失败: %+v err=%v", r, err)
	}
}

func TestRegisterHappyPath(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()

	reply, err := env.comp.Register(ctx, &protos.RegisterMsg{Username: "Alice", Password: "hunter2"})
	if err != nil {
		t.Fatalf("Register 不该报错: %v", err)
	}
	if !reply.Ok {
		t.Fatalf("注册应成功，得到 reason=%q", reply.Reason)
	}
	if err := ValidateToken(reply.Token); err != nil {
		t.Fatalf("签发的 token 形态应合法: %v", err)
	}
	if reply.Username != "Alice" || reply.AccountId != "1" {
		t.Fatalf("应回显原始大小写与 accountID，得到 %+v", reply)
	}
	if env.sess.uid != "1" {
		t.Fatalf("会话应绑定到 accountID，得到 %q", env.sess.uid)
	}
	if id, ok, _ := env.store.ResolveToken(ctx, reply.Token); !ok || id != "1" {
		t.Fatalf("token 应解析到 1，得到 %q ok=%v", id, ok)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()

	// 格式错误走 reason，不返回 Go error —— 客户端要的是可展示的原因码。
	reply, err := env.comp.Register(ctx, &protos.RegisterMsg{Username: "ab", Password: "hunter2"})
	if err != nil {
		t.Fatalf("格式错误不该返回 Go error: %v", err)
	}
	if reply.Ok || reply.Reason != ReasonBadUsername {
		t.Fatalf("应回 bad_username，得到 %+v", reply)
	}

	reply, _ = env.comp.Register(ctx, &protos.RegisterMsg{Username: "alice", Password: "123"})
	if reply.Ok || reply.Reason != ReasonBadPassword {
		t.Fatalf("应回 bad_password，得到 %+v", reply)
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	r, err := env.comp.Register(ctx, &protos.RegisterMsg{Username: "alice", Password: "hunter2"})
	if err != nil {
		t.Fatalf("重名不该返回 Go error: %v", err)
	}
	if r.Ok || r.Reason != ReasonNameTaken {
		t.Fatalf("应回 name_taken，得到 %+v", r)
	}
}

func TestLoginHappyPathAndWrongPassword(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	// 换一个会话，模拟另一台设备。
	env.app.sess = &fakeSession{}

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil || !r.Ok {
		t.Fatalf("登录应成功，得到 %+v err=%v", r, err)
	}
	if id, ok, _ := env.store.ResolveToken(ctx, r.Token); !ok || id != "1" {
		t.Fatalf("新 token 应解析到 1，得到 %q ok=%v", id, ok)
	}

	// 错密码与不存在的用户名必须给同一个 reason（不泄露账号是否存在）。
	r1, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "wrong!"})
	r2, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "nobody", Password: "hunter2"})
	if r1.Ok || r2.Ok {
		t.Fatal("错密码与不存在的用户都不该登录成功")
	}
	if r1.Reason != ReasonBadCredentials || r2.Reason != ReasonBadCredentials {
		t.Fatalf("两者都应是 bad_credentials，得到 %q / %q", r1.Reason, r2.Reason)
	}
}

// 换设备登录：凭证轮换 + 定点踢掉旧 gate 上的连接。
func TestLoginRotatesTokenAndKicksOldGate(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	first, err := env.store.IssueToken(ctx, "1")
	if err != nil {
		t.Fatalf("准备 token 失败: %v", err)
	}
	// 上次登录在 gate-A；这次从另一台设备进来，绑定会把登记改写成 gate-B。
	if err := env.online.Set(ctx, "1", "gate-A"); err != nil {
		t.Fatalf("准备 online 失败: %v", err)
	}
	env.app.sess = &fakeSession{onBind: func(uid string) {
		if err := env.online.Set(context.Background(), uid, "gate-B"); err != nil {
			t.Errorf("onBind 里写 online 失败: %v", err)
		}
	}}

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil || !r.Ok {
		t.Fatalf("登录应成功，得到 %+v err=%v", r, err)
	}
	if r.Token == first {
		t.Fatal("登录必须轮换 token")
	}
	if _, ok, _ := env.store.ResolveToken(ctx, first); ok {
		t.Fatal("旧 token 必须立即失效（顶号的权威手段）")
	}
	if len(env.app.calls) != 1 {
		t.Fatalf("应只在旧 gate 上踢一次，得到 %+v", env.app.calls)
	}
	if env.app.calls[0].serverID != "gate-A" || env.app.calls[0].route != kickRoute {
		t.Fatalf("应定点踢 gate-A，得到 %+v", env.app.calls[0])
	}
}

// 同一个 gate 上重复登录不该踢 —— Bind 已经同步把旧会话关掉了，再踢会踢到自己。
func TestLoginDoesNotKickWhenSameGate(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	if err := env.online.Set(ctx, "1", "gate-A"); err != nil {
		t.Fatalf("准备 online 失败: %v", err)
	}
	// 绑定后登记仍是 gate-A，说明旧会话就在本节点。
	env.app.sess = &fakeSession{onBind: func(uid string) {
		_ = env.online.Set(context.Background(), uid, "gate-A")
	}}

	r, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if !r.Ok {
		t.Fatalf("登录应成功，得到 %+v", r)
	}
	if len(env.app.calls) != 0 {
		t.Fatalf("同 gate 不该踢，得到 %+v", env.app.calls)
	}
}

func TestLoginRateLimited(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	for i := 0; i < RateLimit; i++ {
		env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "wrong!"})
	}
	r, _ := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "wrong!"})
	if r.Ok || r.Reason != ReasonRateLimited {
		t.Fatalf("超限应回 rate_limited，得到 %+v", r)
	}
}

func TestResumeHappyPathAndInvalid(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()

	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword 报错: %v", err)
	}
	if _, err := env.store.Create(ctx, "alice", hash); err != nil {
		t.Fatalf("准备账号失败: %v", err)
	}
	tok, err := env.store.IssueToken(ctx, "1")
	if err != nil {
		t.Fatalf("准备 token 失败: %v", err)
	}

	r, err := env.comp.Resume(ctx, &protos.ResumeMsg{Token: tok})
	if err != nil || !r.Ok {
		t.Fatalf("resume 应成功，得到 %+v err=%v", r, err)
	}
	if r.AccountId != "1" || r.Username != "alice" {
		t.Fatalf("应回账号信息，得到 %+v", r)
	}
	if env.sess.uid != "1" {
		t.Fatalf("会话应绑定到 1，得到 %q", env.sess.uid)
	}

	for _, bad := range []string{"garbage", ""} {
		r, _ := env.comp.Resume(ctx, &protos.ResumeMsg{Token: bad})
		if r.Ok || r.Reason != ReasonTokenInvalid {
			t.Fatalf("无效 token %q 应回 token_invalid，得到 %+v", bad, r)
		}
	}
}

// Bind 失败时不能把「登录成功」报给客户端：会话没绑上，之后的 match.join
// 会被当成未登录而忽略，玩家会卡在「正在匹配…」而无从排查。
func TestLoginReportsBindFailure(t *testing.T) {
	env := newTestComponent(t)
	ctx := context.Background()
	env.mustRegister(t, "alice", "hunter2")

	env.app.sess = &fakeSession{bindErr: errors.New("boom")}

	r, err := env.comp.Login(ctx, &protos.LoginMsg{Username: "alice", Password: "hunter2"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if r.Ok || r.Reason != ReasonInternal {
		t.Fatalf("Bind 失败应回 internal，得到 %+v", r)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./account
```

Expected: FAIL — `undefined: New`、`undefined: ReasonBadCredentials`。

- [ ] **Step 3: 写实现**

创建 `joltgo/account/component.go`：

```go
// component.go 是 account 服务的 pitaya handler：注册、登录、凭证恢复。
//
// 三个 handler 都返回 (*protos.LoginReply, error) —— 返回值决定消息类型：
// pitaya 的 suitableHandlerMethods 里「有返回值 = Request，无返回值 = Notify」，
// 所以它们是 Request，客户端按 mid 收 Response。这一点是必须的，不是风格选择：
// NATS 模式下 uid 未绑定就 push 不了（agent_remote.go 的 Push 直接返回
// ErrNoUIDBind），而登录失败时 uid 恰恰是空的，结果根本推不回去。
package account

import (
	"context"
	"log"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"joltgo/game/protos"
	"joltgo/online"
)

// 原因码。客户端按它显示文案，服务端不给自由文本。
const (
	ReasonBadCredentials = "bad_credentials"
	ReasonNameTaken      = "name_taken"
	ReasonBadUsername    = "bad_username"
	ReasonBadPassword    = "bad_password"
	ReasonRateLimited    = "rate_limited"
	ReasonTokenInvalid   = "token_invalid"
	ReasonInternal       = "internal"
)

// kickRoute 是 pitaya 内置的定点踢人 RPC（remote.Sys.Kick 注册在服务名 "sys" 下）。
const kickRoute = "gate.sys.kick"

// Component 是 account 服务的 pitaya 组件。
type Component struct {
	component.Base
	app   pitaya.Pitaya
	store *Store
	online *online.Store
}

// New 构造 account 组件（在 main.go 里 Register 到 app）。
func New(app pitaya.Pitaya, store *Store, onl *online.Store) *Component {
	return &Component{app: app, store: store, online: onl}
}

// Register 是 register handler（route "account.account.register"）。
func (c *Component) Register(ctx context.Context, msg *protos.RegisterMsg) (*protos.LoginReply, error) {
	if err := ValidateUsername(msg.Username); err != nil {
		return fail(ReasonBadUsername), nil
	}
	if err := ValidatePassword(msg.Password); err != nil {
		return fail(ReasonBadPassword), nil
	}

	// 限流按用户名（见 store.go 的 RateLimit 说明：account 拿不到客户端 IP）。
	allowed, err := c.store.Allow(ctx, msg.Username)
	if err != nil {
		log.Printf("account: rate limit check failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !allowed {
		return fail(ReasonRateLimited), nil
	}

	hash, err := HashPassword(msg.Password)
	if err != nil {
		return fail(ReasonBadPassword), nil
	}
	id, err := c.store.Create(ctx, msg.Username, hash)
	if err != nil {
		if err == ErrNameTaken {
			return fail(ReasonNameTaken), nil
		}
		log.Printf("account: create failed: %v", err)
		return fail(ReasonInternal), nil
	}

	return c.finishLogin(ctx, id, msg.Username)
}

// Login 是 login handler（route "account.account.login"）。
func (c *Component) Login(ctx context.Context, msg *protos.LoginMsg) (*protos.LoginReply, error) {
	allowed, err := c.store.Allow(ctx, msg.Username)
	if err != nil {
		log.Printf("account: rate limit check failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !allowed {
		return fail(ReasonRateLimited), nil
	}

	// 用户名不存在与密码错误返回同一个 reason：不泄露账号是否存在。
	id, ok, err := c.store.LookupByName(ctx, msg.Username)
	if err != nil {
		log.Printf("account: lookup failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !ok {
		return fail(ReasonBadCredentials), nil
	}
	acct, ok, err := c.store.GetAccount(ctx, id)
	if err != nil {
		log.Printf("account: get account failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !ok || !CheckPassword(acct.PassHash, msg.Password) {
		return fail(ReasonBadCredentials), nil
	}

	return c.finishLogin(ctx, id, acct.Username)
}

// Resume 是 resume handler（route "account.account.resume"）：拿凭证换回会话。
func (c *Component) Resume(ctx context.Context, msg *protos.ResumeMsg) (*protos.LoginReply, error) {
	if err := ValidateToken(msg.Token); err != nil {
		return fail(ReasonTokenInvalid), nil
	}
	id, ok, err := c.store.ResolveToken(ctx, msg.Token)
	if err != nil {
		log.Printf("account: resolve token failed: %v", err)
		return fail(ReasonInternal), nil
	}
	if !ok {
		return fail(ReasonTokenInvalid), nil
	}
	acct, ok, err := c.store.GetAccount(ctx, id)
	if err != nil || !ok {
		log.Printf("account: account %s for a valid token is missing (err=%v)", id, err)
		return fail(ReasonTokenInvalid), nil
	}
	return c.finishLogin(ctx, id, acct.Username)
}

// finishLogin 是三个 handler 共用的收尾：记下旧 gate → 轮换凭证 → 绑定会话 →
// 定点踢掉旧连接。
//
// 顺序是关键。第 3 步 Bind 会把**同一个 gate 上**的旧会话同步关掉（框架的
// sessionsByUID 逻辑），所以第 4 步只需要处理「旧会话在另一个 gate」的情况 ——
// 而那一脚绝不会打到自己，顶号竞态因此从设计上消失，不靠自愈。
func (c *Component) finishLogin(ctx context.Context, accountID, username string) (*protos.LoginReply, error) {
	// 1) 记下这次登录之前，该账号登记的 gate（可能是空 = 没在线）。
	oldGate, err := c.online.Gate(ctx, accountID)
	if err != nil {
		log.Printf("account: online lookup for %s failed: %v", accountID, err)
		// 读不到只影响「能不能踢掉旧连接」，不影响登录本身，继续。
		oldGate = ""
	}

	// 2) 轮换凭证。这一步是顶号的权威手段：旧客户端的 token 立即作废。
	token, err := c.store.IssueToken(ctx, accountID)
	if err != nil {
		log.Printf("account: issue token failed: %v", err)
		return fail(ReasonInternal), nil
	}

	// 3) 绑定会话。未绑定就没有身份，一切后续消息都会被当成未登录。
	s := c.app.GetSessionFromCtx(ctx)
	if err := s.Bind(ctx, accountID); err != nil {
		log.Printf("account: bind session to %s failed: %v", accountID, err)
		// Bind 失败必须如实回 internal：若报成功，客户端会去发 match.join，
		// 而它会因会话未绑定被静默忽略，玩家卡在「正在匹配…」无从排查。
		return fail(ReasonInternal), nil
	}

	// 4) 定点踢掉旧 gate 上的连接。Bind 返回时 gate 的 after-bind 钩子已经写完
	//    登记（跨节点 RPC，天然有序），所以这里读到的是本节点。
	me, err := c.online.Gate(ctx, accountID)
	if err != nil {
		log.Printf("account: online re-read for %s failed: %v", accountID, err)
	}
	if oldGate != "" && oldGate != me {
		if err := c.kickOn(ctx, oldGate, accountID); err != nil {
			// 踢不掉不影响正确性（旧 token 已作废，它 resume 不回来），
			// 只影响「旧连接早一点闭嘴」。
			log.Printf("account: kick %s on %s failed: %v", accountID, oldGate, err)
		}
	}

	return &protos.LoginReply{
		Ok:        true,
		Token:     token,
		Username:  username,
		AccountId: accountID,
	}, nil
}

// kickOn 请指定 gate 关掉该账号的会话。
func (c *Component) kickOn(ctx context.Context, gateID, accountID string) error {
	return c.app.RPCTo(ctx, gateID, kickRoute,
		&protos.KickAck{},
		&protos.KickRequest{UserId: accountID})
}

// fail 构造一个失败应答。
func fail(reason string) *protos.LoginReply {
	return &protos.LoginReply{Ok: false, Reason: reason}
}
```

⚠️ `kickOn` 里的 `protos.KickAck` / `protos.KickRequest` **不存在**，不要自己造。pitaya 的 `RPCTo` 要求 `proto.Message`，踢人用的是 pitaya 自带的 `protos.KickMsg`（`third_party/pitaya/pkg/protos`）与 `protos.KickAnswer`：

```go
import (
	pitayaprotos "github.com/topfreegames/pitaya/v3/pkg/protos"
)

// kickOn 请指定 gate 关掉该账号的会话。
func (c *Component) kickOn(ctx context.Context, gateID, accountID string) error {
	return c.app.RPCTo(ctx, gateID, kickRoute,
		&pitayaprotos.KickAnswer{},
		&pitayaprotos.KickMsg{UserId: accountID})
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./account
```

Expected: `ok  	joltgo/account`

- [ ] **Step 5: vet 与格式**

```bash
cd joltgo && PATH="$PWD:$PATH" go vet ./account && gofmt -l account
```

Expected: 无输出。

- [ ] **Step 6: 提交**

```bash
git add joltgo/account/component.go joltgo/account/component_test.go
git commit -m "feat(account): register/login/resume 三个 handler

三者的返回值都是 LoginReply，因此是 Request/Response（NATS 下 uid 未绑定时
Push 必然失败，登录结果推不回去）。finishLogin 统一收尾：记旧 gate → 轮换
凭证 → Bind → 定点踢旧 gate。同 gate 不踢 —— Bind 已同步关掉旧会话。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: main.go 第 4 角色 + gate 的 account 路由

**Files:**
- Modify: `joltgo/main.go`
- Modify: `joltgo/gate/gate.go`

**Interfaces:**
- Consumes: `kv.Open`、`account.NewStore`、`account.New`、`online.NewStore`（Task 3/4）
- Produces: 可运行的第 4 个角色；`gate.Configure` 增加 `account` 路由

- [ ] **Step 1: 给 gate 加 account 路由**

在 `joltgo/gate/gate.go` 的 `Configure` 里加一条路由，并加对应的路由函数：

```go
// Configure 在 gate 前端注册 account/match/game 三个路由函数。
func Configure(app pitaya.Pitaya) error {
	if err := app.AddRoute("account", routeAny); err != nil {
		return err
	}
	if err := app.AddRoute("match", routeAny); err != nil {
		return err
	}
	return app.AddRoute("game", routeGame(app))
}

// routeAny 轮询任一同类节点。account 与 match 的服务自身无状态（状态都在
// Redis），所以哪个节点处理都一样，不需要一致性哈希。
func routeAny(
	_ context.Context,
	_ *route.Route,
	_ []byte,
	servers map[string]*cluster.Server,
) (*cluster.Server, error) {
	for _, srv := range servers {
		return srv, nil
	}
	return nil, errors.New("no server available")
}
```

删掉原来的 `routeMatch`（被 `routeAny` 取代），并更新包注释里的路由清单：

```go
// Package gate 是 gate 服务（frontend）的路由配置：它持有客户端会话、把业务
// 消息路由到后端，并登记会话归属（见 session.go），自身不注册业务 handler。
//
//   - account.* → 轮询任一 account 节点（服务无状态，状态在 Redis）
//   - match.*   → 轮询任一 match 节点（同上）
//   - game.*    → 读会话数据里的 gameServerId，定点路由到托管该对局的 game 节点
//
// gameServerId 由匹配方（match）经定点 RPC 请本 gate 写入（见 session.go 的
// BindGame），因此 gate 的 game 路由函数能据此定位具体 game 节点。
```

- [ ] **Step 2: 改 main.go**

把 `joltgo/main.go` 整个文件替换成：

```go
package main

// 分布式服务端入口：单二进制按 -type 启动四种角色（gate / account / match / game），
// pitaya Cluster 模式（etcd 服务发现 + NATS RPC），共享状态放 Redis。
//
//   - gate（frontend）：与客户端直连（WS），把业务消息路由到后端，
//     并登记「账号在哪个 gate 在线」（见 gate/session.go）
//   - account（backend）：账号注册/登录/凭证恢复。唯一碰密码与 Redis 账号数据的角色
//   - match（backend）：对局匹配，队列在 Redis（多节点共享）
//   - game（backend）：游戏逻辑，每个对局一个 goroutine 顺序执行、无锁
//
// 启动顺序：先起 etcd + nats-server + redis-server（见 deploy/），再起四个进程：
//
//	joltgo.exe -type gate    -port 8080
//	joltgo.exe -type account
//	joltgo.exe -type match
//	joltgo.exe -type game
//
// 优雅退出由 pitaya 的 app.Start() 内部处理（SIGINT/SIGTERM → shutdownComponents）。

import (
	"context"
	"flag"
	"log"
	"strings"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/acceptor"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/config"
	"github.com/topfreegames/pitaya/v3/pkg/groups"
	"joltgo/account"
	"joltgo/game"
	"joltgo/gate"
	"joltgo/kv"
	"joltgo/match"
	"joltgo/online"
)

func main() {
	svType := flag.String("type", "gate", "server type: gate | account | match | game")
	redisAddr := flag.String("redis", kv.DefaultAddr, "redis address (host:port)")
	flag.Parse()

	cfg := config.NewDefaultPitayaConfig()
	// protobuf serializer（serializertype=2），消息类型见 game/protos/game.proto；
	// 关消息压缩：客户端用纯 GDScript 解码，不引入 gzip。
	cfg.SerializerType = 2
	cfg.Handler.Messages.Compression = false

	isFrontend := *svType == "gate"
	builder := pitaya.NewDefaultBuilder(
		isFrontend,
		*svType,
		pitaya.Cluster, // 集群模式：etcd 服务发现 + NATS RPC
		map[string]string{},
		*cfg,
	)
	builder.Groups = groups.NewMemoryGroupService(cfg.Groups.Memory)

	if isFrontend {
		builder.AddAcceptor(acceptor.NewWSAcceptor(":8080"))
	}

	if err := run(svType, builder, *redisAddr); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

// run 组装并启动指定角色的服务。抽成函数是为了让 flag 解析与 defer 清理分离 ——
// main 里 log.Fatal 会跳过 defer，Redis 连接必须在这里关。
//
// 注意别写成 log.Fatalf("启动失败: %v", run(...))：app.Start() 在 SIGINT/SIGTERM
// 时是**正常返回**，那样写会把每次优雅退出都报成「启动失败: <nil>」并以 1 退出，
// 让部署脚本和冒烟测试读到并不存在的失败。
func run(svType *string, builder *pitaya.Builder, redisAddr string) error {
	// Redis 是 gate/account/match 的共享依赖（gate 写会话归属、account 存取账号与
	// 凭证、match 存排队队列）。game 完全不碰 Redis —— 不给它建连接，免得 Redis
	// 抖动连带把对局节点也拖得起不来。
	// 三个需要 Redis 的角色统一「连不上就启动失败」——早失败比运行中途才暴露好排查。
	// game 不连（见上）。
	var rdb *redis.Client
	if *svType != "game" {
		var err error
		rdb, err = kv.Open(context.Background(), redisAddr)
		if err != nil {
			return err
		}
		defer rdb.Close()
	}

	app := builder.Build()

	switch *svType {
	case "gate":
		if err := gate.Configure(app); err != nil {
			return err
		}
		// 会话归属：绑定后写 online:{uid} → 本节点，断开时清除。
		gate.RegisterSessionHooks(builder.SessionPool, app.GetServerID(), online.NewStore(rdb))
		// 必须用 RegisterRemote，不能用 Register：match 是用 app.RPCTo 调过来的，
		// 而 RPCTo 走 RPCType_User → handleRPCUser → remotes 表，那张表**只由
		// RegisterRemote 填充**。注册成 handler 的话路由在 remotes 里找不到，
		// match 的 bindgame 会拿 ErrNotFoundCode（见 service/remote.go:246/254）。
		// 反过来这也正好关掉了攻击面：客户端发的 gate.gate.bindgame 会被路由到
		// handler 池、找不到而报错，压根到不了这里。
		app.RegisterRemote(gate.NewSessionComponent(app, builder.SessionPool),
			component.WithName("gate"),
			component.WithNameFunc(strings.ToLower),
		)

	case "account":
		app.Register(account.New(app, account.NewStore(rdb), online.NewStore(rdb)),
			component.WithName("account"),
			component.WithNameFunc(strings.ToLower),
		)

	case "match":
		app.Register(match.New(app, match.NewQueue(rdb), online.NewStore(rdb)),
			component.WithName("match"),
			component.WithNameFunc(strings.ToLower),
		)

	case "game":
		// 同一个组件实例同时注册为 handler（客户端经 gate 路由来的
		// game.cmd / game.resync，RPCType_Sys）与 remote（match 服务 RPCTo 来的
		// game.create / game.rejoin，RPCType_User）——两者的实例注册表必须共享。
		comp := game.New(app)
		app.Register(comp,
			component.WithName("game"),
			component.WithNameFunc(strings.ToLower),
		)
		app.RegisterRemote(comp,
			component.WithName("game"),
			component.WithNameFunc(strings.ToLower),
		)

	default:
		return fmt.Errorf("unknown server type %q (want gate|account|match|game)", *svType)
	}

	app.Start()
	return nil
}
```

⚠️ `run` 的第二个参数类型是 `*pitaya.Builder`（**不是** `*builder.Builder`——pitaya
没有 `pkg/builder` 子包，`pkg/builder.go` 的包名就是 `pitaya`）。`pitaya.NewDefaultBuilder`
的签名是：

```go
func NewDefaultBuilder(isFrontend bool, serverType string, serverMode ServerMode,
	serverMetadata map[string]string, pitayaConfig config.PitayaConfig) *Builder
```

同时需要 `import "fmt"`（用于 `fmt.Errorf`）。

- [ ] **Step 3: 编译（预期失败：gate.SessionComponent 还不存在）**

```bash
cd joltgo && PATH="$PWD:$PATH" go build ./... 2>&1 | head -20
```

Expected: `undefined: gate.NewSessionComponent`、`undefined: gate.RegisterSessionHooks`（Task 6 实现）。**这一步的失败是预期的**——继续 Task 6，回来再编译。

- [ ] **Step 4: 提交（与 Task 6 一起，见下）**

本任务不单独提交：它单独存在时编译不过，与 Task 6 是同一次可验证的变更。

---

## Task 6: gate 会话归属登记 + bindgame handler

**Files:**
- Create: `joltgo/gate/session.go`
- Test: `joltgo/gate/session_test.go`

**Interfaces:**
- Consumes: `online.Store`（Task 3）、`protos.BindGameMsg` / `protos.BindGameReply`（Task 1）
- Produces:
  - `gate.NewSessionComponent(app pitaya.Pitaya, pool session.SessionPool) *SessionComponent`
  - `(*SessionComponent).BindGame(ctx, msg *protos.BindGameMsg) (*protos.BindGameReply, error)`
  - `gate.RegisterSessionHooks(pool session.SessionPool, serverID string, onl *online.Store)`

- [ ] **Step 1: 写失败的测试**

创建 `joltgo/gate/session_test.go`：

```go
package gate

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	pitayaerrors "github.com/topfreegames/pitaya/v3/pkg/errors"
	"github.com/redis/go-redis/v9"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

// fakeSession 嵌入 session.Session，只覆盖组件用到的部分。
type fakeSession struct {
	session.Session
	uid        string
	isFrontend bool
	data       map[string]interface{}
}

func (s *fakeSession) UID() string         { return s.uid }
func (s *fakeSession) GetIsFrontend() bool { return s.isFrontend }
func (s *fakeSession) Set(k string, v interface{}) error {
	if s.data == nil {
		s.data = map[string]interface{}{}
	}
	s.data[k] = v
	return nil
}

// fakePool 只实现 GetSessionByUID。
type fakePool struct {
	session.SessionPool
	byUID map[string]session.Session
}

func (p *fakePool) GetSessionByUID(uid string) session.Session { return p.byUID[uid] }

// fakeApp 只实现 GetSessionFromCtx。
type fakeApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *fakeApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// 客户端直接发 gate.gate.bindgame 时必须被拒：否则任何人都能把自己的会话绑到
// 任意 game 节点，绕过匹配直接对着别人的对局发命令。
func TestBindGameRejectsClientSession(t *testing.T) {
	target := &fakeSession{uid: "1", data: map[string]interface{}{}}
	pool := &fakePool{byUID: map[string]session.Session{"1": target}}
	app := &fakeApp{sess: &fakeSession{uid: "1", isFrontend: true}} // 客户端会话
	c := NewSessionComponent(app, pool)

	reply, err := c.BindGame(context.Background(), &protos.BindGameMsg{Uid: "1", GameServerId: "g-1"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if reply.Found {
		t.Fatal("客户端会话发来的 bindgame 必须被拒绝")
	}
	if _, ok := target.data["gameServerId"]; ok {
		t.Fatal("被拒的请求不能改动会话数据")
	}
}

// 后端 RPC（会话是 Remote，IsFrontend=false）应正常写入。
func TestBindGameFromBackendWritesSessionData(t *testing.T) {
	target := &fakeSession{uid: "1"}
	pool := &fakePool{byUID: map[string]session.Session{"1": target}}
	app := &fakeApp{sess: &fakeSession{uid: "remote", isFrontend: false}}
	c := NewSessionComponent(app, pool)

	reply, err := c.BindGame(context.Background(), &protos.BindGameMsg{
		Uid: "1", GameServerId: "g-1", MatchId: "m-1", PlayerIdx: 1,
	})
	if err != nil || !reply.Found {
		t.Fatalf("后端请求应成功，得到 %+v err=%v", reply, err)
	}
	if got := target.data["gameServerId"]; got != "g-1" {
		t.Fatalf("会话数据应写入 g-1，得到 %v", got)
	}
}

// 玩家已不在本 gate 上（掉线）时 found=false，调用方据此剔除。
func TestBindGameNotFound(t *testing.T) {
	pool := &fakePool{byUID: map[string]session.Session{}}
	app := &fakeApp{sess: &fakeSession{isFrontend: false}}
	c := NewSessionComponent(app, pool)

	reply, err := c.BindGame(context.Background(), &protos.BindGameMsg{Uid: "gone", GameServerId: "g-1"})
	if err != nil {
		t.Fatalf("不该返回 Go error: %v", err)
	}
	if reply.Found {
		t.Fatal("本 gate 没有该会话时应回 found=false")
	}
}

// 回滚：game_server_id 为空表示清掉归属。
func TestBindGameRollbackClearsGameServer(t *testing.T) {
	target := &fakeSession{uid: "1", data: map[string]interface{}{"gameServerId": "g-1"}}
	pool := &fakePool{byUID: map[string]session.Session{"1": target}}
	app := &fakeApp{sess: &fakeSession{isFrontend: false}}
	c := NewSessionComponent(app, pool)

	if _, err := c.BindGame(context.Background(), &protos.BindGameMsg{Uid: "1"}); err != nil {
		t.Fatalf("回滚不该报错: %v", err)
	}
	if got := target.data["gameServerId"]; got != "" {
		t.Fatalf("回滚应清空，得到 %v", got)
	}
}

// RegisterSessionHooks：绑定写登记、断开清登记。
func TestRegisterSessionHooks(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	pool := session.NewSessionPool()

	RegisterSessionHooks(pool, "gate-A", onl)
	// 池上的钩子是内部状态，这里直接调用它们来验证行为。
	ctx := context.Background()
	s := &fakeSession{uid: "7"}

	pool.OnAfterSessionBind(nil) // 占位，见下
	_ = s
	_ = ctx
	_ = onl
	_ = pitayaerrors.ErrUnknownCode
}
```

⚠️ 最后那个用例行不通：`session.SessionPool` 没有「取出已注册钩子并调用」的公开方法，`OnAfterSessionBind(nil)` 会 panic（内部做 `reflect.ValueOf(f).Pointer()`）。

**改成结构更简单的做法**：把登记逻辑抽成一个纯函数，让测试直接调它，钩子只做转发。在 `session.go` 里写成：

```go
// markOnline 是绑定钩子的实体（抽成函数是为了能单测：sessionPool 的钩子
// 没有公开的 getter，注册进去就取不出来了）。
func markOnline(ctx context.Context, s session.Session, serverID string, onl *online.Store) {
	uid := s.UID()
	if uid == "" {
		return
	}
	if err := onl.Set(ctx, uid, serverID); err != nil {
		// best-effort：登记失败不阻塞连接建立，只降级（顶号少踢一次）。
		log.Printf("gate: set online for %s failed: %v", uid, err)
	}
}

// clearOnline 是断开钩子的实体。
func clearOnline(s session.Session, onl *online.Store) {
	uid := s.UID()
	if uid == "" {
		return
	}
	if err := onl.Clear(context.Background(), uid); err != nil {
		log.Printf("gate: clear online for %s failed: %v", uid, err)
	}
}
```

于是测试写成：

```go
func TestMarkOnline(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	ctx := context.Background()

	markOnline(ctx, &fakeSession{uid: "7"}, "gate-A", onl)
	if got, _ := onl.Gate(ctx, "7"); got != "gate-A" {
		t.Fatalf("应登记到 gate-A，得到 %q", got)
	}

	// 未绑定的会话（uid 为空）不该写登记。
	markOnline(ctx, &fakeSession{uid: ""}, "gate-A", onl)
	if got, _ := onl.Gate(ctx, ""); got != "" {
		t.Fatalf("空 uid 不该登记，得到 %q", got)
	}

	clearOnline(&fakeSession{uid: "7"}, onl)
	if got, _ := onl.Gate(ctx, "7"); got != "" {
		t.Fatalf("清除后应为空，得到 %q", got)
	}
}

// 登记写失败不能 panic、也不能阻塞（best-effort）。
func TestMarkOnlineSurvivesRedisFailure(t *testing.T) {
	rdb := newTestRedis(t)
	onl := online.NewStore(rdb)
	markOnline(context.Background(), &fakeSession{uid: "7"}, "gate-A", onl)
	// 关掉 redis 再写一次：只应记日志。
	_ = rdb.Close()
	markOnline(context.Background(), &fakeSession{uid: "8"}, "gate-A", onl)
}
```

删掉 `TestRegisterSessionHooks` 与 `pitayaerrors` import。

- [ ] **Step 2: 跑测试确认失败**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./gate
```

Expected: FAIL — `undefined: NewSessionComponent`、`undefined: markOnline`。

- [ ] **Step 3: 写实现**

创建 `joltgo/gate/session.go`：

```go
// session.go 是 gate 的会话归属逻辑：把「账号在哪个 gate 在线」登记进 Redis，
// 并接受后端请托写会话数据。
//
// 为什么 gate 要连 Redis：会话归属只有 gate 自己知道。后端（account / match）
// 手里只有 uid，没有会话对象 —— backend 的会话池里没有前端会话。这条登记是
// best-effort 的：写失败只降级（顶号少踢一次、探活误判），正确性由凭证轮换兜底
// （被顶掉的客户端拿着已作废的 token，重连也 resume 不回来）。
package gate

import (
	"context"
	"log"

	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

// SessionComponent 是 gate 上的会话组件。它不处理客户端消息：客户端不该发
// gate.*，真发了也会被 BindGame 的守卫挡住。
type SessionComponent struct {
	component.Base
	app  pitaya.Pitaya
	pool session.SessionPool
}

// NewSessionComponent 构造会话组件。
//
// 需要 pool 而不是只用 app：pitaya.Pitaya 接口上**没有** GetSessionByUID
// （只有 GetSessionFromCtx），要按 uid 找会话只能拿 SessionPool。
func NewSessionComponent(app pitaya.Pitaya, pool session.SessionPool) *SessionComponent {
	return &SessionComponent{app: app, pool: pool}
}

// RegisterSessionHooks 在 gate 的会话池上挂「绑定后 / 断开时」两个钩子，
// 维护 online 登记。必须在 app.Start() 之前调用（钩子只在启动时装配一次）。
func RegisterSessionHooks(pool session.SessionPool, serverID string, onl *online.Store) {
	// OnAfterSessionBind 在 Bind 内部、所有 sessionBindCallbacks 之后触发，
	// 此时 uid 已确定。
	pool.OnAfterSessionBind(func(ctx context.Context, s session.Session) error {
		markOnline(ctx, s, serverID, onl)
		return nil
	})
	pool.OnSessionClose(func(s session.Session) {
		clearOnline(s, onl)
	})
}

// markOnline 是绑定钩子的实体（抽成函数是为了能单测：sessionPool 的钩子
// 没有公开的 getter，注册进去就取不出来了）。
func markOnline(ctx context.Context, s session.Session, serverID string, onl *online.Store) {
	uid := s.UID()
	if uid == "" {
		return
	}
	if err := onl.Set(ctx, uid, serverID); err != nil {
		// best-effort：登记失败不阻塞连接建立，只降级。
		log.Printf("gate: set online for %s failed: %v", uid, err)
	}
}

// clearOnline 是断开钩子的实体。
func clearOnline(s session.Session, onl *online.Store) {
	uid := s.UID()
	if uid == "" {
		return
	}
	if err := onl.Clear(context.Background(), uid); err != nil {
		log.Printf("gate: clear online for %s failed: %v", uid, err)
	}
}

// BindGame 是远端 RPC handler（route "gate.gate.bindgame"）：后端（match）请本
// gate 把对局归属写进玩家自己的会话数据。
//
// 让 gate 改而不是 match 隔着 NATS 用 PushToFront 改：会话属于前端，请它自己改
// 比跨进程改别人的状态更正确；而且 match 手里只有 uid，根本拿不到会话对象。
//
// 安全守卫：客户端可以直接发 gate.gate.bindgame，把自己的会话绑到任意 game
// 节点，从而绕过匹配、对着别人的对局发命令。
//
// 挡它的**第一层是注册方式**：本组件只经 RegisterRemote 暴露（见 main.go），
// 客户端消息会被路由进 gate 的 handler 池、找不到这个 route 而直接报错。
// 下面这行是纵深防御 —— 万一将来有人把 app.Register 也加上，它就是唯一拦截点。
//
// 注意必须 nil 安全：**真实调用路径（后端 RPC）的 ctx 里根本没有会话**，
// GetSessionFromCtx 返回 nil（app.go 在缺 SessionCtxKey 时返回裸 nil 并打 Debug 日志）。
// 写成 `s.GetIsFrontend()` 会直接 panic —— 被 util.Pcall 兜成通用错误，
// 表现为 Found 永远为 false，且极难排查。
func (c *SessionComponent) BindGame(ctx context.Context, msg *protos.BindGameMsg) (*protos.BindGameReply, error) {
	if s := c.app.GetSessionFromCtx(ctx); s != nil && s.GetIsFrontend() {
		log.Printf("gate: reject bindgame from client session uid=%s", s.UID())
		return &protos.BindGameReply{Found: false}, nil
	}

	target := c.pool.GetSessionByUID(msg.Uid)
	if target == nil {
		// 玩家已不在本 gate 上（掉线或被顶号关掉了）。
		return &protos.BindGameReply{Found: false}, nil
	}

	// 空 game_server_id 表示回滚（建局失败时清掉归属），此时写入空串 ——
	// gate 的 routeGame 读不到非空 gsid 就会拒绝 game.*，正是想要的效果。
	if err := target.Set("gameServerId", msg.GameServerId); err != nil {
		log.Printf("gate: set gameServerId for %s failed: %v", msg.Uid, err)
		return &protos.BindGameReply{Found: false}, nil
	}
	return &protos.BindGameReply{Found: true}, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./gate
```

Expected: `ok  	joltgo/gate`

- [ ] **Step 5: 整体编译**

```bash
cd joltgo && PATH="$PWD:$PATH" go build ./... 2>&1 | head -20
```

Expected: 无输出（Task 5 的 `main.go` 现在能编过了）。

- [ ] **Step 6: 提交**

```bash
git add joltgo/main.go joltgo/gate
git commit -m "feat(gate): 会话归属登记与 bindgame handler；main 加 account 角色

gate 在会话绑定/断开时写 online:{uid}，并接受 match 的定点 RPC 写会话数据
gameServerId。BindGame 用 IsFrontend() 守卫把客户端挡在门外（否则客户端
可以自己把会话绑到任意 game 节点、绕过匹配）。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: match —— 队列搬 Redis + 开局链路按 uid 定址

**Files:**
- Create: `joltgo/match/queue.go`
- Test: `joltgo/match/queue_test.go`
- Modify: `joltgo/match/match.go`
- Modify: `joltgo/match/match_test.go`

**Interfaces:**
- Consumes: `online.Store`（Task 3）、`protos.BindGameMsg` / `BindGameReply`（Task 1）、`gate.gate.bindgame` route（Task 6）
- Produces:
  - `match.NewQueue(rdb *redis.Client) *Queue`
  - `(*Queue).Enqueue(ctx, uid string) error`
  - `(*Queue).PopPair(ctx) ([]string, error)`
  - `(*Queue).PopStale(ctx, timeout time.Duration) (string, error)`
  - `match.New(app pitaya.Pitaya, queue *Queue, onl *online.Store) *Component`

- [ ] **Step 1: 写失败的测试（队列）**

创建 `joltgo/match/queue_test.go`：

```go
package match

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestQueue(t *testing.T) (*Queue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewQueue(rdb), mr
}

func TestPopPairNeedsTwo(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// 一个人时不能弹：直接 ZPOPMIN 会把人白白弹出队列，
	// 弹出来又凑不齐只能丢回去，还会打乱等待顺序。
	if err := q.Enqueue(ctx, "a"); err != nil {
		t.Fatalf("Enqueue 报错: %v", err)
	}
	got, err := q.PopPair(ctx)
	if err != nil {
		t.Fatalf("PopPair 报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("不足 2 人时应返回空，得到 %v", got)
	}
	if n := q.queueSize(ctx, t); n != 1 {
		t.Fatalf("a 应还在队列里，得到 %d 人", n)
	}
}

func TestPopPairReturnsOldestFirst(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// 直接写死 score 而不是靠 Enqueue 的时钟：同一毫秒内入队的两条 score 相同，
	// ZSET 会退化成按 member 字典序排 —— 那样测出来的顺序是巧合不是语义。
	for _, m := range []struct {
		uid   string
		score float64
	}{{"c", 30}, {"a", 10}, {"b", 20}} {
		if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: m.score, Member: m.uid}).Err(); err != nil {
			t.Fatalf("ZAdd 报错: %v", err)
		}
	}

	got, err := q.PopPair(ctx)
	if err != nil {
		t.Fatalf("PopPair 报错: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("应弹出 score 最小的 a/b，得到 %v", got)
	}
	if q.queueSize(ctx, t) != 1 {
		t.Fatalf("应只剩 c")
	}
}

func TestEnqueueDedupsByUid(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	if err := q.Enqueue(ctx, "a"); err != nil {
		t.Fatalf("Enqueue 报错: %v", err)
	}
	if err := q.Enqueue(ctx, "a"); err != nil {
		t.Fatalf("重复 Enqueue 报错: %v", err)
	}
	if n := q.queueSize(ctx, t); n != 1 {
		t.Fatalf("同一个 uid 重复入队应只占一条（ZSET 去重），得到 %d", n)
	}

	// 再加一个人，应能立刻凑成一对 —— 若去重失效，这里会弹出 a/a。
	if err := q.Enqueue(ctx, "b"); err != nil {
		t.Fatalf("Enqueue(b) 报错: %v", err)
	}
	got, _ := q.PopPair(ctx)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("应弹出两个不同的人，得到 %v", got)
	}
}

func TestPopStale(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// 刚入队的人不该被单人兜底拿走。注意不能用 miniredis 的 FastForward 来
	// 「等 10 秒」—— 队列的时间戳取自 Redis 的 TIME 命令，而 miniredis 的 TIME
	// 返回真实墙钟，不受 FastForward 影响。所以这里直接写 score。
	now, err := q.nowMs(ctx, t)
	if err != nil {
		t.Fatalf("取 Redis 时间报错: %v", err)
	}
	if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: float64(now), Member: "fresh"}).Err(); err != nil {
		t.Fatalf("ZAdd 报错: %v", err)
	}

	got, err := q.PopStale(ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("PopStale 报错: %v", err)
	}
	if got != "" {
		t.Fatalf("未超时不该弹出，得到 %q", got)
	}

	// 早就入队的人（score 是 1 秒 = 1970 年）应被拿走。
	if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: 1000, Member: "stale"}).Err(); err != nil {
		t.Fatalf("ZAdd 报错: %v", err)
	}
	got, err = q.PopStale(ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("PopStale 报错: %v", err)
	}
	if got != "stale" {
		t.Fatalf("超时后应弹出 stale，得到 %q", got)
	}
	if q.queueSize(ctx, t) != 1 {
		t.Fatalf("应只剩 fresh")
	}
}

// 多个 match 节点并发抢同一个人时，只有一个能拿到（Lua 原子取）。
func TestPopStaleIsAtomic(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	if err := q.rdb.ZAdd(ctx, queueKey, redis.Z{Score: 1000, Member: "stale"}).Err(); err != nil {
		t.Fatalf("ZAdd 报错: %v", err)
	}

	const n = 8
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			got, _ := q.PopStale(context.Background(), 10*time.Second)
			results <- got
		}()
	}
	hit := 0
	for i := 0; i < n; i++ {
		if <-results != "" {
			hit++
		}
	}
	if hit != 1 {
		t.Fatalf("并发抢同一个人应恰好一个成功，得到 %d", hit)
	}
}
```

用例里用到两个测试辅助（`q.queueSize` 与 `q.nowMs`），写在**测试文件**里 —— 不要为了测试去改生产代码：

```go
// queueSize 返回队列当前人数。用 ZCARD 而不是把队列内容取出来（后者会干扰断言）。
func (q *Queue) queueSize(ctx context.Context, t *testing.T) int {
	t.Helper()
	n, err := q.rdb.ZCard(ctx, queueKey).Result()
	if err != nil {
		t.Fatalf("ZCARD 报错: %v", err)
	}
	return int(n)
}

// nowMs 取 Redis 服务端当前时间（毫秒），与队列脚本用的是同一个时钟。
func (q *Queue) nowMs(ctx context.Context, t *testing.T) (int64, error) {
	t.Helper()
	return redis.NewScript(`local t = redis.call('TIME')
return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)`).
		Run(ctx, q.rdb, []string{}).Int64()
}
```

（`Queue.rdb` 与 `queueKey` 都是包内可见，同包测试可以直接用。）

- [ ] **Step 2: 跑测试确认失败**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 -run 'TestPop|TestEnqueue' ./match
```

Expected: FAIL — `undefined: NewQueue`、`undefined: queueKey`。

- [ ] **Step 3: 写队列实现**

创建 `joltgo/match/queue.go`：

```go
// queue.go 是配对队列：ZSET(member=uid, score=入队毫秒时间戳)。
//
// 为什么在 Redis 而不是进程内切片：多个 match 节点各有一份队列的话，两个玩家
// 大概率落到不同节点，各自队列永远凑不满 2 人，双双等满兜底超时、各开一局。
// 放进 Redis 后所有 match 节点共享同一个队列，节点本身无状态、可水平扩容。
//
// 为什么用 ZSET 而不是 LIST：按 uid 去重（重连再 ZADD 就是更新 score，天然的
// 「挤掉旧的那条」）与按等待时长排序（score 就是入队时间）都是免费的。
//
// 时间戳一律取 **Redis 服务端时间**（脚本里的 redis.call('TIME')），不用各节点
// 的 time.Now()：否则「等了 10 秒」的定义会随节点时钟偏移而变 —— 一个快 15 秒
// 的节点会把刚入队的人直接拿去做单人开局。多节点共享同一个时钟是这里的关键。
package match

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// queueKey 是配对队列的键。
const queueKey = "match:queue"

// enqueueScript 入队并返回入队时刻（毫秒，取自 Redis 服务端时钟）。
// 同一个 uid 重复入队只更新 score，即**重新计时** —— 排队期间断线重连会被视为
// 重新排队，这是有意的（他确实刚刚才回来）。
var enqueueScript = redis.NewScript(`
local t = redis.call('TIME')
local ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZADD', KEYS[1], ms, ARGV[1])
return ms
`)

// pairScript 原子地取出最早的两个排队者。
//
// 必须先判人数再 ZPOPMIN：不足 2 人时直接 ZPOPMIN 会把人白白弹出队列，
// 弹出来又凑不齐只能丢回去 —— 丢回等于重置等待时间，反复发生会让排队者
// 永远等不到开局。
var pairScript = redis.NewScript(`
if redis.call('ZCARD', KEYS[1]) < 2 then return {} end
return redis.call('ZPOPMIN', KEYS[1], 2)
`)

// staleScript 原子地取出「最早且已等待超过 ARGV[1] 毫秒」的那一个（单人兜底）。
//
// 用 ZRANGEBYSCORE + ZREM 而不是 ZPOPMIN：要取的是**最早且已超时**的，
// 而不是单纯最早的 —— 后者会把一个刚入队的人拿去单人开局。
var staleScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local cutoff = now - tonumber(ARGV[1])
local r = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', cutoff, 'LIMIT', 0, 1)
if #r == 0 then return nil end
redis.call('ZREM', KEYS[1], r[1])
return r[1]
`)

// Queue 是 Redis 上的配对队列。
type Queue struct {
	rdb *redis.Client
}

// NewQueue 构造队列。
func NewQueue(rdb *redis.Client) *Queue { return &Queue{rdb: rdb} }

// Enqueue 入队（时间戳用 Redis 服务端时钟）。
func (q *Queue) Enqueue(ctx context.Context, uid string) error {
	return enqueueScript.Run(ctx, q.rdb, []string{queueKey}, uid).Err()
}

// PopPair 原子取出最早的两个排队者。返回空切片表示当前不足 2 人。
func (q *Queue) PopPair(ctx context.Context) ([]string, error) {
	res, err := pairScript.Run(ctx, q.rdb, []string{queueKey}).Result()
	if err != nil {
		return nil, err
	}
	items, ok := res.([]interface{})
	if !ok || len(items) == 0 {
		return nil, nil
	}
	// ZPOPMIN 在 Lua 里返回扁平的 [member, score, member, score, ...]，
	// 所以每隔一个取。
	uids := make([]string, 0, len(items)/2)
	for i := 0; i < len(items); i += 2 {
		if s, ok := items[i].(string); ok {
			uids = append(uids, s)
		}
	}
	return uids, nil
}

// PopStale 原子取出「最早且已等待超过 timeout」的那一个排队者（单人兜底）。
// 返回空串表示当前没有超时的排队者。
func (q *Queue) PopStale(ctx context.Context, timeout time.Duration) (string, error) {
	res, err := staleScript.Run(ctx, q.rdb, []string{queueKey}, timeout.Milliseconds()).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s, _ := res.(string)
	return s, nil
}
```

- [ ] **Step 4: 跑队列测试确认通过**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 -run 'TestPop|TestEnqueue' ./match
```

Expected: `ok  	joltgo/match`

- [ ] **Step 5: 改 match.go 的开局链路**

把 `joltgo/match/match.go` 整个文件替换成：

```go
// Package match 是 match 服务（backend）：对局匹配。凑齐 2 人（或超时兜底单人）
// 后挑一个 game 节点，请各玩家所属的 gate 写会话数据，并把结果推给双方客户端。
//
// 节点无状态：排队队列在 Redis（ZSET），会话归属也在 Redis（online:），
// 因此多个 match 节点可以同时跑、共享同一个队列。
package match

import (
	"context"
	"log"
	"time"

	"github.com/nats-io/nuid"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/component"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"joltgo/game/protos"
	"joltgo/online"
)

const (
	gameServerType  = "game"                // game 服务类型（AddRoute 与服务发现用）
	matchedRoute    = "onMatched"           // match → 客户端 push 的 route
	gameCreateRoute = "game.game.create"    // game 服务的创建对局 RPC route（三段式）
	gameRejoinRoute = "game.game.rejoin"    // 回局查询 RPC route（三段式）
	bindGameRoute   = "gate.gate.bindgame"  // 请玩家所属 gate 写会话数据（三段式）
	timeout         = 10 * time.Second      // 单人兜底开局的等待超时
	tickInterval    = time.Second           // 抢配对的轮询间隔
)

// RejoinResult 是一次回局查询的结果。GameServerID 是托管该实例的 game 节点。
type RejoinResult struct {
	Found        bool
	MatchID      string
	PlayerIdx    int
	GameServerID string
}

// firstFound 从各 game 节点的应答里挑出第一个命中的。replies 的键是 game 节点 id。
// 抽成纯函数是为了能脱离 pitaya 直接单测。
func firstFound(replies map[string]*RejoinResult) (*RejoinResult, bool) {
	for _, r := range replies {
		if r.Found {
			return r, true
		}
	}
	return nil, false
}

// Component 是 match 服务的 pitaya 组件。
type Component struct {
	component.Base
	app    pitaya.Pitaya
	queue  *Queue
	online *online.Store
}

// New 构造 match 组件。
func New(app pitaya.Pitaya, queue *Queue, onl *online.Store) *Component {
	return &Component{app: app, queue: queue, online: onl}
}

// Join 是远端 RPC handler（route "match.join"）：把已登录的会话加入匹配队列。
//
// 身份来自会话绑定 —— account 服务在登录成功时做过 s.Bind(ctx, accountID)，
// 这里只读会话 UID，不再看任何客户端传来的凭证。未绑定的会话说明客户端没登录
// （或登录失败后擅自发了 join），静默忽略并记日志。
func (c *Component) Join(ctx context.Context, msg *protos.JoinMsg) {
	s := c.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		log.Printf("match: join rejected: session not bound")
		return
	}

	// 回局优先：存量对局还在就直接回去，不入匹配队列。
	if c.tryRejoin(ctx, s, uid) {
		return
	}

	if err := c.queue.Enqueue(ctx, uid); err != nil {
		log.Printf("match: enqueue %s failed: %v", uid, err)
		return
	}
	c.tryMatch(ctx)
}

// tryRejoin 询问所有 game 节点是否托管着该 uid 的存量实例。命中则走与首次匹配
// 相同的收尾路径（写会话数据 + 推 onMatched），返回 true。
func (c *Component) tryRejoin(ctx context.Context, s session.Session, uid string) bool {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		return false
	}
	replies := map[string]*RejoinResult{}
	for id, srv := range servers {
		reply := &protos.RejoinReply{}
		if err := c.app.RPCTo(ctx, srv.ID, gameRejoinRoute, reply, &protos.RejoinMsg{Token: uid}); err != nil {
			continue // 该节点不可达，跳过
		}
		replies[id] = &RejoinResult{
			Found:        reply.Found,
			MatchID:      reply.MatchId,
			PlayerIdx:    int(reply.PlayerIdx),
			GameServerID: srv.ID,
		}
	}
	hit, ok := firstFound(replies)
	if !ok {
		return false
	}
	log.Printf("match: uid %s rejoined match %s on game %s as slot %d",
		uid, hit.MatchID, hit.GameServerID, hit.PlayerIdx)

	gateID, err := c.online.Gate(ctx, uid)
	if err != nil || gateID == "" {
		log.Printf("match: rejoin %s: no online gate (err=%v)", uid, err)
		return false
	}
	if err := c.bindGameOn(ctx, gateID, uid, hit.MatchID, hit.GameServerID, hit.PlayerIdx); err != nil {
		log.Printf("match: rejoin bind game for %s failed: %v", uid, err)
		return false
	}
	c.pushMatched(uid, hit.MatchID, hit.GameServerID, hit.PlayerIdx)
	return true
}

// AfterInit 启动兜底定时器：长时间等不到第二人的玩家单人开局。
func (c *Component) AfterInit() {
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for range ticker.C {
			c.tryMatch(context.Background())
			c.tryMatchTimeout(context.Background())
		}
	}()
}

// tryMatch 尝试配对：队列凑满 2 人即开局。多个 match 节点同时抢也只有一个
// 能弹出（Lua 原子），不需要选主。
func (c *Component) tryMatch(ctx context.Context) {
	uids, err := c.queue.PopPair(ctx)
	if err != nil {
		log.Printf("match: pop pair failed: %v", err)
		return
	}
	if len(uids) == 0 {
		return
	}
	c.startMatch(ctx, uids)
}

// tryMatchTimeout 兜底：单人等待超过 timeout 即单人开局。
func (c *Component) tryMatchTimeout(ctx context.Context) {
	uid, err := c.queue.PopStale(ctx, timeout)
	if err != nil {
		log.Printf("match: pop stale failed: %v", err)
		return
	}
	if uid == "" {
		return
	}
	c.startMatch(ctx, []string{uid})
}

// bindGameOn 请指定 gate 把对局归属写进玩家的会话数据。
// gameServerID 为空表示回滚（清掉归属）。
func (c *Component) bindGameOn(ctx context.Context, gateID, uid, matchID, gameServerID string, playerIdx int) error {
	return c.app.RPCTo(ctx, gateID, bindGameRoute,
		&protos.BindGameReply{},
		&protos.BindGameMsg{
			Uid:          uid,
			GameServerId: gameServerID,
			MatchId:      matchID,
			PlayerIdx:    int32(playerIdx),
		})
}

// pushMatched 把匹配结果推给客户端。
//
// 走全局 SendPushToUsers（发布到 pitaya/gate/user/{uid}/push）而不是定点 RPC：
// 推送与玩家连在哪个 gate 无关，NATS 会投给持有该会话的那个 gate。
func (c *Component) pushMatched(uid, matchID, gameServerID string, playerIdx int) {
	if _, err := c.app.SendPushToUsers(matchedRoute, &protos.MatchResult{
		MatchId:      matchID,
		GameServerId: gameServerID,
		PlayerIdx:    int32(playerIdx),
	}, []string{uid}, "gate"); err != nil {
		log.Printf("match: push onMatched to %s failed: %v", uid, err)
	}
}

// startMatch 挑一个 game 节点、请各玩家的 gate 写会话数据、创建对局，最后推结果。
//
// 顺序是先定地址再建局：排队者可能已经掉线（match 侧没有断线钩子），先读在线
// 登记再 RPC 请对方 gate 写会话数据，能把掉线的人在建局前剔掉 —— 不会留下
// 「占着槽位的幽灵玩家」，也不会出现客户端收到 onMatched 但 resync 路由不到的
// 死局。槽位（player_idx）在剔除过程中就定下来，因为 game.create 的 uids
// 下标就是 player_idx。
func (c *Component) startMatch(ctx context.Context, uids []string) {
	servers, err := c.app.GetServersByType(gameServerType)
	if err != nil || len(servers) == 0 {
		log.Printf("match: no game server available: %v", err)
		return
	}
	// 简单挑选：取第一个 game 节点（demo 规模足够；生产可做负载均衡）。
	var target *cluster.Server
	for _, srv := range servers {
		target = srv
		break
	}

	matchID := nuid.New().Next()

	// 1) 定地址 + 探活：读不到在线登记、或对方的 gate 写不进去，就当这个人没了。
	alive := make([]string, 0, len(uids))
	gates := make([]string, 0, len(uids))
	for _, uid := range uids {
		gateID, err := c.online.Gate(ctx, uid)
		if err != nil || gateID == "" {
			log.Printf("match: dropping %s from match %s: no online gate (err=%v)", uid, matchID, err)
			continue
		}
		slot := len(alive) // 槽位 = 在存活名单里的位置
		if err := c.bindGameOn(ctx, gateID, uid, matchID, target.ID, slot); err != nil {
			log.Printf("match: dropping %s from match %s: bind failed: %v", uid, matchID, err)
			continue
		}
		alive = append(alive, uid)
		gates = append(gates, gateID)
	}
	if len(alive) == 0 {
		return
	}

	// 2) 建局。失败则回滚会话数据，否则客户端会拿着一个不存在的 game 节点去发
	//    game.cmd，全部被静默丢弃（routeGame 找不到 gameServerId 对应节点）。
	reply := &protos.CreateGameReply{}
	if err := c.app.RPCTo(ctx, target.ID, gameCreateRoute, reply, &protos.CreateGameMsg{
		MatchId: matchID,
		Uids:    alive,
	}); err != nil {
		log.Printf("match: create game on %s failed: %v", target.ID, err)
		for i, uid := range alive {
			if err := c.bindGameOn(ctx, gates[i], uid, "", "", i); err != nil {
				log.Printf("match: rollback bind for %s failed: %v", uid, err)
			}
		}
		return
	}

	// 3) 推结果。
	for slot, uid := range alive {
		c.pushMatched(uid, matchID, target.ID, slot)
	}
	log.Printf("match: started match %s on game %s with %d players", matchID, target.ID, len(alive))
}
```

⚠️ `tryRejoin` 保留 `session.Session` 参数（它既要用 uid，也要在失败路径上不动会话），
所以 import 块里的 `session` 不能省。

- [ ] **Step 6: 改 match 的测试**

把 `joltgo/match/match_test.go` 整个文件替换成：

```go
package match

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/session"
	"github.com/redis/go-redis/v9"
	"joltgo/game/protos"
	"joltgo/online"
)

func TestFirstFound(t *testing.T) {
	if _, ok := firstFound(map[string]*RejoinResult{}); ok {
		t.Fatal("没有任何应答时不应命中")
	}
	if _, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
	}); ok {
		t.Fatal("全部未命中时不应命中")
	}
	got, ok := firstFound(map[string]*RejoinResult{
		"g1": {Found: false},
		"g2": {Found: true, MatchID: "m1", PlayerIdx: 1, GameServerID: "g2"},
	})
	if !ok {
		t.Fatal("有节点命中时应返回 ok")
	}
	if got.MatchID != "m1" || got.PlayerIdx != 1 || got.GameServerID != "g2" {
		t.Fatalf("应命中 g2 的实例，得到 %+v", got)
	}
}

// Component 只需要 pitaya.Pitaya 接口，所以「嵌入接口 + 覆盖用得到的方法」
// 就够驱动 Join 了 —— 不必引入 gomock。
type joinTestApp struct {
	pitaya.Pitaya
	sess session.Session
}

func (a *joinTestApp) GetSessionFromCtx(context.Context) session.Session { return a.sess }

func (a *joinTestApp) GetServersByType(string) (map[string]*cluster.Server, error) {
	return nil, errors.New("no game server") // 回局查询必然是 miss
}

// joinTestSession 只实现 Join 用到的 UID。
type joinTestSession struct {
	session.Session
	uid string
}

func (s *joinTestSession) UID() string { return s.uid }

func newTestComponent(t *testing.T, sess session.Session) (*Component, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return New(&joinTestApp{sess: sess}, NewQueue(rdb), online.NewStore(rdb)), mr
}

// 排队等待期间断线重连：同一个 uid 再 Join 一次，队列里必须还是只有一条
// （ZSET 按 member 去重）。不去重的话这里会是 2 条，进而可能自己跟自己配对、
// 或单人兜底时开两局。
func TestJoinDedupsQueuedUid(t *testing.T) {
	c, _ := newTestComponent(t, &joinTestSession{uid: "T"})
	ctx := context.Background()

	c.Join(ctx, &protos.JoinMsg{})
	if n, _ := c.queue.rdb.ZCard(ctx, queueKey).Result(); n != 1 {
		t.Fatalf("首次 Join 应入队一条，得到 %d", n)
	}
	c.Join(ctx, &protos.JoinMsg{})
	if n, _ := c.queue.rdb.ZCard(ctx, queueKey).Result(); n != 1 {
		t.Fatalf("同 uid 重连不应重复入队，得到 %d", n)
	}
	if got, _ := c.queue.rdb.ZRange(ctx, queueKey, 0, -1).Result(); len(got) != 1 || got[0] != "T" {
		t.Fatalf("队列里应是 T，得到 %v", got)
	}
}

// 未登录（会话未绑定）的 Join 必须被忽略：不入队。
func TestJoinRejectsUnboundSession(t *testing.T) {
	c, _ := newTestComponent(t, &joinTestSession{uid: ""})
	ctx := context.Background()

	c.Join(ctx, &protos.JoinMsg{})
	if n, _ := c.queue.rdb.ZCard(ctx, queueKey).Result(); n != 0 {
		t.Fatalf("未绑定会话不应入队，得到 %d 条", n)
	}
}
```

- [ ] **Step 7: 跑全部 match 测试**

```bash
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./match
```

Expected: `ok  	joltgo/match`

- [ ] **Step 8: 整体编译 + vet + 格式**

```bash
cd joltgo && PATH="$PWD:$PATH" go build ./... && PATH="$PWD:$PATH" go vet ./match ./gate ./account ./online ./kv && gofmt -l match gate account online kv
```

Expected: 只有 `match/match.go`、`gate/gate.go` 可能因存量 CRLF 被列出；其余无输出。

- [ ] **Step 9: 提交**

```bash
git add joltgo/match
git commit -m "feat(match): 队列搬 Redis；开局链路按 uid 定址

配对队列从进程内切片改为 Redis ZSET（+ 两段 Lua 保证原子取人），多个 match
节点共享同一队列 —— 原来两个玩家会落到不同节点、各自队列永远凑不满 2 人，
双双等满兜底超时、各开一局。开局改为先读在线登记再 RPC 请 gate 写会话数据，
掉线者建局前就被剔除；建局失败回滚会话数据。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: 客户端 Response 编解码

**Files:**
- Modify: `godot_client/scripts/fps_client.gd`
- Test: `godot_client/tests/login_reply_decode_test.gd`

**Interfaces:**
- Consumes: 服务端 `account.account.register/login/resume` 的 Response（Task 4/5）
- Produces:
  - `FpsClient._send_request(mid: int, route: String, payload: PackedByteArray) -> bool`
  - `FpsClient._decode_login_reply(buf: PackedByteArray) -> Dictionary`
  - `FpsClient._on_response(data: PackedByteArray, is_err: bool) -> void`
  - 信号 `login_result(result: Dictionary)`（`{ok, token, username, account_id, reason}`）

- [ ] **Step 1: 写失败的测试**

创建 `godot_client/tests/login_reply_decode_test.gd`：

```gdscript
extends SceneTree
## Response 帧与 LoginReply 的解码测试。
##
## 这是本项目第一次走 pitaya 的 Request/Response 路径：此前只发 Notify、只收
## Push。契约与 Push 不同 —— Response 帧是 flag + mid(LEB128) + payload，
## 没有 route 字段。这里用手工构造的字节串把两种帧都钉住。

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node
# GDScript 没有 try/catch：某个用例内部抛错会让函数中途返回、_failures 还是 0，
# 整个用例就"假绿"了。所以每个函数末尾打完成标记，_init 逐个核对。
var _done := {}

func _init() -> void:
	_c = FpsClient.new()
	_test_login_reply_ok()
	_test_login_reply_failure()
	_test_login_reply_empty()
	_test_response_frame_layout()
	_test_error_mask_flag()
	for name: String in ["reply_ok", "reply_fail", "reply_empty", "frame_layout", "error_mask"]:
		if not _done.has(name):
			_failures += 1
			printerr("FAIL: 用例 %s 没跑完（中途抛错了？）" % name)
	if _failures > 0:
		printerr("login_reply_decode_test: %d 项失败" % _failures)
		quit(1)
	else:
		print("login_reply_decode_test: OK")
		quit(0)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

# ---- protobuf 编码辅助（测试里手写，与服务端生成码对齐） ----

func _tag(field: int, wire: int) -> PackedByteArray:
	return _c._varint((field << 3) | wire)

func _f_varint(field: int, v: int) -> PackedByteArray:
	var out := _tag(field, _c.WIRE_VARINT)
	out.append_array(_c._varint(v))
	return out

func _f_str(field: int, s: String) -> PackedByteArray:
	var body := s.to_utf8_buffer()
	var out := _tag(field, _c.WIRE_LEN)
	out.append_array(_c._varint(body.size()))
	out.append_array(body)
	return out

# ---- 用例 ----

func _test_login_reply_ok() -> void:
	var buf := PackedByteArray()
	buf.append_array(_f_varint(1, 1))          # ok = true
	buf.append_array(_f_str(2, "tok123"))      # token
	buf.append_array(_f_str(3, "Alice"))       # username
	buf.append_array(_f_str(4, "7"))           # account_id
	var d: Dictionary = _c._decode_login_reply(buf)
	_check(bool(d["ok"]), "ok 应为 true")
	_check(d["token"] == "tok123", "token 应为 tok123，得到 %s" % d["token"])
	_check(d["username"] == "Alice", "username 应保留原始大小写，得到 %s" % d["username"])
	_check(d["account_id"] == "7", "account_id 应为 7，得到 %s" % d["account_id"])
	_check(d["reason"] == "", "成功时 reason 应为空，得到 %s" % d["reason"])
	_done["reply_ok"] = true

func _test_login_reply_failure() -> void:
	# ok 字段缺省 = false（proto3），失败应答只有 reason。
	var buf := _f_str(5, "bad_credentials")
	var d: Dictionary = _c._decode_login_reply(buf)
	_check(not bool(d["ok"]), "缺省 ok 应为 false")
	_check(d["reason"] == "bad_credentials", "reason 应解出，得到 %s" % d["reason"])
	_check(d["token"] == "", "失败时 token 应为空")
	_done["reply_fail"] = true

func _test_login_reply_empty() -> void:
	var d: Dictionary = _c._decode_login_reply(PackedByteArray())
	_check(not bool(d["ok"]), "空载荷应是失败态")
	_check(d["token"] == "" and d["username"] == "" and d["reason"] == "", "空载荷各字段应为空串")
	_done["reply_empty"] = true

## Response 帧的布局：flag(0x04) + mid(LEB128) + payload —— 没有 route。
func _test_response_frame_layout() -> void:
	var payload := _f_varint(1, 1)
	var frame := PackedByteArray()
	frame.append(_c.MSG_RESPONSE << 1)
	frame.append_array(_c._varint(300))   # 需要 2 字节 LEB128（300 > 127）
	frame.append_array(payload)

	# 用与服务端编码器同样的方式还原。
	var r: Array = _c._read_varint(frame, 1)
	_check(int(r[0]) == 300, "mid 应 LEB128 还原成 300，得到 %d" % int(r[0]))
	var rest: PackedByteArray = frame.slice(int(r[1]))
	_check(rest.size() == payload.size(), "mid 之后应恰好是 payload")
	_check((frame[0] >> 1) & 0x07 == _c.MSG_RESPONSE, "flag 低 3 位应是 Response")

	# 关键契约：Response 帧没有 route 字段。payload 的第一个字节是 protobuf tag
	# （字段 1 varint = 0x08），不是 route 长度 —— 若误按 Push 解析，会把它当成
	# 长度为 8 的 route 读走 8 个字节，静默解出垃圾。
	_check(rest[0] == 0x08, "payload 首字节应是 protobuf tag 0x08，得到 %d" % rest[0])
	_done["frame_layout"] = true

## errorMask(0x20)：pitaya 层错误会置位，此时 payload 是错误字符串而非 LoginReply。
func _test_error_mask_flag() -> void:
	var flag := (_c.MSG_RESPONSE << 1) | 0x20
	_check((flag & 0x20) != 0, "errorMask 应被识别")
	_check((flag >> 1) & 0x07 == _c.MSG_RESPONSE, "置了 errorMask 也仍是 Response 类型")
	_done["error_mask"] = true
```

- [ ] **Step 2: 跑测试确认失败**

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd
```

Expected: 报错 — `Invalid call. Nonexistent function '_decode_login_reply'`，且退出码非 0。

（Godot 可执行文件在 Downloads 下，不在 PATH；先 `cd` 到它的目录或写全路径。）

- [ ] **Step 3: 加编解码**

在 `godot_client/scripts/fps_client.gd` 顶部信号区加一个信号：

```gdscript
signal login_result(result: Dictionary)   # LoginReply：{ok, token, username, account_id, reason}
```

在常量区加：

```gdscript
const LOGIN_TIMEOUT := 5.0   # 登录类请求的响应超时（秒）
```

在 `# ---- 帧 / 消息编解码 ----` 区，紧跟 `_send_notify` 之后加：

```gdscript
## 发一条 Request 消息：flag=0x00(MSG_REQUEST<<1)，mid（LEB128 变长）
## + route 长度 + route + protobuf payload。
##
## 与 Notify 的唯一区别是多了 mid：服务端按 mid 回 Response 帧。
## 登录必须走 Request —— NATS 模式下会话未绑定时服务端 push 不了，
## 「登录失败」这个结果没有别的路能回来。
func _send_request(mid: int, route: String, payload: PackedByteArray) -> bool:
	if not (connected and _handshaken):
		return false
	var msg := PackedByteArray()
	msg.append(MSG_REQUEST << 1)
	msg.append_array(_varint(mid))
	var rb := route.to_utf8_buffer()
	msg.append(rb.size())
	msg.append_array(rb)
	msg.append_array(payload)
	_send_frame(TYPE_DATA, msg)
	return true
```

把 `_on_data` 替换成按类型分派的版本：

```gdscript
## 解析 Data 帧内的 message：flag 低 3 位得类型（Push / Response 两种），
## 高位 0x20 是 pitaya 的错误标记。
func _on_data(data: PackedByteArray) -> void:
	if data.size() < 2:
		return
	var flag := data[0]
	var mtype := (flag >> 1) & 0x07
	var is_err := (flag & 0x20) != 0
	match mtype:
		MSG_PUSH:
			_on_push(data)
		MSG_RESPONSE:
			_on_response(data, is_err)
		_:
			pass  # 本客户端只发 Notify/Request，其它类型忽略

## Push 帧：route 长度 + route + payload。
func _on_push(data: PackedByteArray) -> void:
	var rl := data[1]
	if data.size() < 2 + rl:
		return
	var route := data.slice(2, 2 + rl).get_string_from_utf8()
	var payload := data.slice(2 + rl)
	match route:
		"onMatched":
			_matched = true
			matched_received.emit(_decode_match_result(payload))
		"onFrame":
			frame_received.emit(_decode_frame(payload))

## Response 帧：mid（LEB128 变长）+ payload —— **没有 route 字段**。
## is_err 为真时 payload 是 pitaya 的错误字符串，不是 LoginReply。
func _on_response(data: PackedByteArray, is_err: bool) -> void:
	var r: Array = _read_varint(data, 1)
	var mid: int = int(r[0])
	var payload: PackedByteArray = data.slice(int(r[1]))
	_pending.erase(mid)
	if is_err:
		printerr("登录请求失败（服务端错误）: " + payload.get_string_from_utf8())
		login_result.emit({"ok": false, "reason": "internal"})
		return
	# 成功时由登录流程负责落盘凭证（见 Task 9 的 _save_token）。
	login_result.emit(_decode_login_reply(payload))

## LoginReply：ok=1(varint) token=2 username=3 account_id=4 reason=5（均为 string）。
func _decode_login_reply(buf: PackedByteArray) -> Dictionary:
	var d := {"ok": false, "token": "", "username": "", "account_id": "", "reason": ""}
	var i := 0
	while i < buf.size():
		var tag: Array = _read_varint(buf, i)
		i = int(tag[1])
		var field: int = int(tag[0]) >> 3
		var wire: int = int(tag[0]) & 0x07
		if wire == WIRE_VARINT:
			var r: Array = _read_varint(buf, i)
			i = int(r[1])
			if field == 1:
				d["ok"] = int(r[0]) != 0
		elif wire == WIRE_LEN:
			var rl: Array = _read_varint(buf, i)
			i = int(rl[1])
			var n: int = int(rl[0])
			var sub: PackedByteArray = buf.slice(i, i + n)
			i += n
			match field:
				2: d["token"] = sub.get_string_from_utf8()
				3: d["username"] = sub.get_string_from_utf8()
				4: d["account_id"] = sub.get_string_from_utf8()
				5: d["reason"] = sub.get_string_from_utf8()
		else:
			break
	return d
```

在变量声明区加 `_pending` 与 `_next_mid`：

```gdscript
var _pending := {}    # mid -> {"route": String, "at": float}，用于响应关联与超时
var _next_mid := 1
```

- [ ] **Step 4: 跑测试确认通过**

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd
```

Expected: `login_reply_decode_test: OK`，退出码 0。

- [ ] **Step 5: 确认没弄坏现有解码测试**

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/world_store_test.gd
```

Expected: 两个都 OK（`_on_data` 被拆成了 `_on_push`，若拆分时漏了什么，这两个会立刻发现）。

- [ ] **Step 6: 提交**

```bash
git add godot_client/scripts/fps_client.gd godot_client/tests/login_reply_decode_test.gd
git commit -m "feat(client): Response 帧编解码

本项目第一次走 Request/Response：Response 帧是 flag + mid(LEB128) + payload，
没有 route 字段（与 Push 不同）。加了 errorMask(0x20) 识别、LoginReply 解码
与 mid 关联表。_on_data 拆成 _on_push / _on_response 两路。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: 客户端登录流程与登录面板

**Files:**
- Modify: `godot_client/scripts/fps_client.gd`（登录状态机、token 持久化）
- Modify: `godot_client/scripts/main.gd`（登录面板、鼠标捕获守卫）

**Interfaces:**
- Consumes: `login_result` 信号、`_send_request`（Task 8）
- Produces:
  - `FpsClient.send_register(username, password) -> void`
  - `FpsClient.send_login(username, password) -> void`
  - `FpsClient.send_resume() -> void`
  - `FpsClient.last_username: String`

- [ ] **Step 1: 改 token 持久化与登录接口**

在 `godot_client/scripts/fps_client.gd` 里，把 `TOKEN_PATH` 常量换成：

```gdscript
const TOKEN_PATH := "user://auth_token.txt"      # 服务端签发的会话凭证
const USERNAME_PATH := "user://last_username.txt" # 上次登录的用户名，用于预填
```

把 `_load_or_create_token` 与 `_uuid4` 两个函数**整个删掉**，换成：

```gdscript
## _load_token 读本地持久化的凭证。空串表示没登录过（要显示登录面板）。
##
## 不复用旧的 client_id.txt：那里面是客户端自己生成的 UUID，服务端不认，
## 拿它去 resume 必然失败 —— 不如不认，直接走登录面板。
func _load_token() -> String:
	if not FileAccess.file_exists(TOKEN_PATH):
		return ""
	var f := FileAccess.open(TOKEN_PATH, FileAccess.READ)
	if f == null:
		return ""
	return f.get_as_text().strip_edges()

## _save_token 落盘凭证与用户名。写失败不致命（下次仍要重新登录）。
func _save_token(token: String, username: String) -> void:
	if token != "":
		client_token = token
		var f := FileAccess.open(TOKEN_PATH, FileAccess.WRITE)
		if f != null:
			f.store_string(token)
	if username != "":
		last_username = username
		var g := FileAccess.open(USERNAME_PATH, FileAccess.WRITE)
		if g != null:
			g.store_string(username)

## _load_username 读上次登录的用户名（预填输入框用）。
func _load_username() -> String:
	if not FileAccess.file_exists(USERNAME_PATH):
		return ""
	var f := FileAccess.open(USERNAME_PATH, FileAccess.READ)
	if f == null:
		return ""
	return f.get_as_text().strip_edges()
```

把变量声明 `var client_token := ""` 改成：

```gdscript
var client_token := ""
var last_username := ""
```

`_ready` 里把 `client_token = _load_or_create_token()` 改成：

```gdscript
	client_token = _load_token()
	last_username = _load_username()
```

最后，把 Task 8 里 `_on_response` 的收尾那一行：

```gdscript
	# 成功时由登录流程负责落盘凭证（见 Task 9 的 _save_token）。
	login_result.emit(_decode_login_reply(payload))
```

替换成：

```gdscript
	var reply := _decode_login_reply(payload)
	if bool(reply.get("ok", false)):
		# 登录/注册/resume 成功才落盘凭证：失败时服务端不发 token，
		# 写空串会把上一次的有效凭证也抹掉。
		_save_token(String(reply.get("token", "")), String(reply.get("username", "")))
	login_result.emit(reply)
```

- [ ] **Step 2: 改握手后的状态机**

把 `_on_handshake` 里最后那两行（`send_match_join()` 及其注释）替换成：

```gdscript
	# 有本地凭证就先试 resume（用户无感，重连回同一局的体验与之前一致）；
	# 没有就交给 UI 显示登录面板。
	#
	# 关键：没有凭证时**绝不**发 match.join —— 会话未绑定时服务端会忽略它，
	# 客户端会卡在「正在匹配…」而无从排查。
	if client_token != "":
		send_resume()
	else:
		login_result.emit({"ok": false, "reason": "no_token"})
```

- [ ] **Step 3: 加三个上行接口与超时**

在 `# ---- 上行：业务接口（main.gd 调用） ----` 区，紧跟 `send_match_join` 之后加：

```gdscript
## send_register 注册新账号；结果经 login_result 信号回来。
func send_register(username: String, password: String) -> void:
	_send_login_request("account.account.register", username, password)

## send_login 用已有账号登录。
func send_login(username: String, password: String) -> void:
	_send_login_request("account.account.login", username, password)

func _send_login_request(route: String, username: String, password: String) -> void:
	var payload := PackedByteArray()
	payload.append_array(_tag_len(1, username.to_utf8_buffer()))
	payload.append_array(_tag_len(2, password.to_utf8_buffer()))
	_send_tracked(route, payload)

## send_resume 用本地凭证换回会话。
func send_resume() -> void:
	_send_tracked("account.account.resume", _tag_len(1, client_token.to_utf8_buffer()))

## _send_tracked 发一条需要关联响应的请求（登记 mid 以便超时与配对）。
func _send_tracked(route: String, payload: PackedByteArray) -> void:
	var mid := _next_mid
	_next_mid += 1
	if _next_mid > 0xFFFFFF:
		_next_mid = 1
	if _send_request(mid, route, payload):
		_pending[mid] = {"route": route, "at": Time.get_ticks_msec() / 1000.0}
	else:
		# 连接还没就绪：立刻当作失败，让 UI 退回登录面板而不是干等超时。
		login_result.emit({"ok": false, "reason": "no_connection"})
```

在 `_process` 的 `STATE_OPEN` 分支里、心跳之后，加超时清理：

```gdscript
			# 登录类请求超时：响应丢了不能一直转圈，退回登录面板。
			for mid in _pending.keys():
				if now - float(_pending[mid]["at"]) > LOGIN_TIMEOUT:
					_pending.erase(mid)
					login_result.emit({"ok": false, "reason": "timeout"})
```

同时把 `_force_reconnect` 与断线分支里的 `_pending` 一并清掉（否则重连后旧的 mid 会误触发超时）：

```gdscript
	_pending = {}
```

加在 `_force_reconnect` 的 `_matched = false` 之后，以及 `STATE_CLOSED` 分支 `connection_changed.emit(false)` 之后。

- [ ] **Step 4: main.gd 加登录面板**

在 `godot_client/scripts/main.gd` 里加变量：

```gdscript
var _login_panel: Control
var _login_user: LineEdit
var _login_pass: LineEdit
var _login_error: Label
var _login_busy := false
```

在 `_ready` 的 `fps_client.connection_changed.connect(_on_connection)` 之后加：

```gdscript
	fps_client.login_result.connect(_on_login_result)
	_build_login_panel()
```

加构造函数（放在 `_build_hud` 之前）：

```gdscript
## _build_login_panel 搭登录/注册面板。用 Control + 手动定位，与 _build_hud 一致
## （本项目不依赖任何外部场景资源）。
func _build_login_panel() -> void:
	var layer := CanvasLayer.new()
	layer.name = "Login"
	add_child(layer)

	# 半透明遮罩：挡住 HUD，也吃掉点击（避免点到底下的东西）。
	var dim := ColorRect.new()
	dim.color = Color(0, 0, 0, 0.55)
	dim.set_anchors_preset(Control.PRESET_FULL_RECT)
	dim.mouse_filter = Control.MOUSE_FILTER_STOP
	layer.add_child(dim)

	var box := VBoxContainer.new()
	box.set_anchors_preset(Control.PRESET_CENTER)
	box.offset_left = -160.0
	box.offset_top = -110.0
	box.offset_right = 160.0
	box.offset_bottom = 110.0
	box.add_theme_constant_override("separation", 10)
	layer.add_child(box)

	var title := Label.new()
	title.text = "Jolt FPS"
	title.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	title.add_theme_font_size_override("font_size", 26)
	box.add_child(title)

	_login_user = LineEdit.new()
	_login_user.placeholder_text = "用户名（3-16 位字母/数字/下划线）"
	_login_user.text = fps_client.last_username
	_login_user.max_length = 16
	box.add_child(_login_user)

	_login_pass = LineEdit.new()
	_login_pass.placeholder_text = "密码（6-64 位）"
	_login_pass.secret = true
	_login_pass.max_length = 64
	box.add_child(_login_pass)

	var row := HBoxContainer.new()
	row.add_theme_constant_override("separation", 10)
	box.add_child(row)

	var btn_login := Button.new()
	btn_login.text = "登录"
	btn_login.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	btn_login.pressed.connect(_submit_login)
	row.add_child(btn_login)

	var btn_reg := Button.new()
	btn_reg.text = "注册"
	btn_reg.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	btn_reg.pressed.connect(_submit_register)
	row.add_child(btn_reg)

	_login_error = Label.new()
	_login_error.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	_login_error.add_theme_color_override("font_color", Color("e5484d"))
	_login_error.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
	box.add_child(_login_error)

	# 回车直接登录，省一次点击。
	_login_pass.text_submitted.connect(func(_t: String) -> void: _submit_login())

	_login_panel = layer
	_show_login_panel(true, "")

## _show_login_panel 显示/隐藏登录面板。
func _show_login_panel(show_it: bool, err: String) -> void:
	_login_panel.visible = show_it
	_login_error.text = err
	_login_busy = false
	if show_it:
		conn_label.visible = false
		Input.mouse_mode = Input.MOUSE_MODE_VISIBLE
		_login_user.grab_focus()

func _submit_login() -> void:
	_submit(false)

func _submit_register() -> void:
	_submit(true)

func _submit(register: bool) -> void:
	if _login_busy:
		return
	var user := _login_user.text.strip_edges()
	var pw := _login_pass.text
	if user == "" or pw == "":
		_login_error.text = "请填写用户名和密码"
		return
	_login_busy = true
	_login_error.text = "请稍候…"
	if register:
		fps_client.send_register(user, pw)
	else:
		fps_client.send_login(user, pw)

## _reason_text 把服务端的原因码翻成给玩家看的文案。
func _reason_text(reason: String) -> String:
	match reason:
		"bad_credentials": return "用户名或密码错误"
		"name_taken": return "该用户名已被注册"
		"bad_username": return "用户名需 3-16 位字母/数字/下划线"
		"bad_password": return "密码需 6-64 位"
		"rate_limited": return "操作过于频繁，请稍后再试"
		"token_invalid": return "登录已过期，请重新登录"
		"timeout": return "服务器无响应，请重试"
		"no_connection": return "未连接到服务器"
		"internal": return "服务暂时不可用"
		_: return ""

## _on_login_result 登录/注册/resume 的统一回调。
func _on_login_result(result: Dictionary) -> void:
	if bool(result.get("ok", false)):
		_show_login_panel(false, "")
		conn_label.text = "正在匹配…"
		conn_label.visible = true
		fps_client.send_match_join()
	else:
		var reason := String(result.get("reason", ""))
		if reason == "no_token":
			# 没登录过：安静地显示面板，不报错。
			_show_login_panel(true, "")
		else:
			_show_login_panel(true, _reason_text(reason))
```

- [ ] **Step 5: 鼠标捕获守卫（关键）**

`main.gd` 的 `_unhandled_input` 现在是「未捕获时任何点击都去捕获鼠标」—— 登录面板上的点击会被它吃掉，导致点按钮的同时锁鼠标。改成：

```gdscript
func _unhandled_input(event: InputEvent) -> void:
	if Input.mouse_mode == Input.MOUSE_MODE_CAPTURED:
		return
	if _login_panel != null and _login_panel.visible:
		return  # 登录面板上的点击归面板，不该当成「进入游戏」
	if event is InputEventMouseButton and event.pressed:
		Input.mouse_mode = Input.MOUSE_MODE_CAPTURED
```

同时改 `_on_connection` 的已连接分支 —— 登录成功之前不该写「正在匹配…」（那时还没发 join）：

```gdscript
	if connected:
		# 登录成功之前不能写「正在匹配…」——那时候还没有发 join。
		conn_label.text = "正在登录…"
		conn_label.visible = true
```

断线分支保持原样（不弹登录面板：会自动重连并 resume，弹了反而闪一下；只有 resume 明确失败才由 `_on_login_result` 弹）。

- [ ] **Step 6: 跑客户端测试**

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/game_frame_test.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd
```

Expected: 三个都 OK。`game_frame_test.gd` 与 `reconnect_cleanup_test.gd` 会实例化 `main.gd`，若 `_build_login_panel` 引用了未定义的变量，这两个会立刻报错。

- [ ] **Step 7: 提交**

```bash
git add godot_client/scripts/fps_client.gd godot_client/scripts/main.gd
git commit -m "feat(client): 登录流程与登录面板

有本地凭证先 resume（体验与之前一致），无凭证显示登录/注册面板。
凭证改存 user://auth_token.txt（不复用旧的 client_id.txt —— 那是自造 UUID，
服务端不认）。鼠标捕获加守卫：登录面板可见时不抢点击。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: 部署（Redis + 第 4 进程）

**Files:**
- Modify: `joltgo/deploy/start-infra.ps1`
- Modify: `joltgo/deploy/start-all.ps1`
- Modify: `joltgo/deploy/README.md`
- Modify: `joltgo/deploy/stop-infra.ps1`（若其中枚举了进程名）

**Interfaces:**
- Consumes: `-type account`（Task 5）
- Produces: 一键起全套（etcd + nats + redis + 四进程）

- [ ] **Step 1: 把 redis-server 放进 deploy/**

把本机的 Redis 二进制拷进 `deploy/`（与 etcd.exe / nats-server.exe 同规格）：

```bash
cp -r "/c/Users/zhubeijian/redis/Redis-8.10.1-Windows-x64-msys2-with-Service/redis-server.exe" /c/Users/zhubeijian/Desktop/Projects/fps/joltgo/deploy/
ls /c/Users/zhubeijian/Desktop/Projects/fps/joltgo/deploy/ | grep redis
```

Expected: 出现 `redis-server.exe`。

⚠️ 如果该 Redis 发行版还依赖同目录的 `msys-2.0.dll` 等运行库，把那些也一并拷进 `deploy/`（先只拷 exe 试跑，缺 DLL 会报「找不到 XXX.dll」）。

- [ ] **Step 2: 改 start-infra.ps1**

在 `joltgo/deploy/start-infra.ps1` 的 nats 判断之后追加 Redis 的启动块：

```powershell
# Redis 与 etcd 相反：**不清数据目录**。etcd 里只有服务发现这种瞬时状态，
# 清掉无妨；Redis 里是账号与凭证，清了就真没了。AOF 让账号在 Redis 重启后
# 仍然存在（不开的话一重启所有账号消失）。
$redisPort = 6379
$redisData = Join-Path $root 'redis-data'
if (Get-NetTCPConnection -LocalPort $redisPort -State Listen -ErrorAction SilentlyContinue) {
  Write-Host "redis-server already listening on $redisPort, kept as is"
} else {
  Start-Process -FilePath (Join-Path $root 'redis-server.exe') -ArgumentList @(
    '--port', "$redisPort",
    '--dir', $redisData,
    '--appendonly', 'yes'
  ) -WindowStyle Hidden
  Write-Host "started redis-server (localhost:$redisPort, aof on)"
}
```

⚠️ 该脚本头部有「无 BOM 的 UTF-8 被 PowerShell 5.1 按 GBK 误读」的坑：**中文只放注释且行尾留 ASCII**。上面对话框里的中文注释行末尾都是 ASCII 字符，保持这个习惯；改完必须用 PowerShell 5.1 实测解析（见 Step 5）。

- [ ] **Step 3: 改 start-all.ps1**

在 `start-all.ps1` 的三个 Start-Process 之后追加 account 进程：

```powershell
Start-Process -FilePath $exe -ArgumentList @('-type', 'account') -WorkingDirectory (Split-Path $exe) -RedirectStandardOutput (Join-Path $root 'account.out.log') -RedirectStandardError (Join-Path $root 'account.log') -WindowStyle Hidden
```

并把结尾的提示改成：

```powershell
Write-Host 'started gate (ws://localhost:8080) + account + match + game'
```

- [ ] **Step 4: 改 stop-infra.ps1**

查看 `joltgo/deploy/stop-infra.ps1`，把 `redis-server` 加进被停止的进程名清单（与 etcd / nats-server 并列）。

- [ ] **Step 5: 实测脚本（PowerShell 5.1 解析）**

```bash
cd /c/Users/zhubeijian/Desktop/Projects/fps/joltgo/deploy && powershell.exe -NoProfile -Command "[void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path '.\start-infra.ps1'), [ref]\$null, [ref]\$errs); if (\$errs) { \$errs | ForEach-Object { Write-Host \$_.Message } ; exit 1 } else { Write-Host 'parse OK' }"
```

Expected: `parse OK`。对 `start-all.ps1` 与 `stop-infra.ps1` 各跑一次。

- [ ] **Step 6: 起全套并验证四个角色都在**

```powershell
cd C:\Users\zhubeijian\Desktop\Projects\fps\joltgo
.\build.ps1
cd deploy
.\stop-infra.ps1
.\start-all.ps1
```

等 5 秒后检查四个服务都注册上了：

```bash
curl -s http://localhost:2379/v3/kv/range -X POST -d '{"key":"cGl0YXlhL3NlcnZlcnMv"}' -H 'Content-Type: application/json' | head -c 2000
```

（etcd 的键前缀 `pitaya/servers/` 的 base64 是 `cGl0YXlhL3NlcnZlcnMv`。）

Expected: 返回的 JSON 里 `key` 字段出现 `Z2F0ZQ`(gate)、`YWNjb3VudA`(account)、`bWF0Y2g`(match)、`Z2FtZQ`(game) 四种。若嫌麻烦，直接看日志也有「registered remote」： 

```bash
tail -20 /c/Users/zhubeijian/Desktop/Projects/fps/joltgo/deploy/account.log | grep -i "registered\|error"
```

- [ ] **Step 7: 提交**

```bash
git add joltgo/deploy
git commit -m "chore(deploy): 加 redis-server 与 account 进程

Redis 不清数据目录（与 etcd 相反：etcd 是瞬时状态，Redis 里是账号），
开 AOF 让账号在重启后仍在。start-all 起第 4 个进程。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: 端到端冒烟测试

**Files:**
- Create: `godot_client/tests/login_smoke.gd`

**Interfaces:**
- Consumes: 完整链路（Task 1–10）
- Produces: 唯一端到端验证「注册 → 拿 token → 断线 → resume → 进入对局」的测试

- [ ] **Step 1: 写测试**

创建 `godot_client/tests/login_smoke.gd`（风格对照 `tests/ws_smoke.gd`）：

```gdscript
extends SceneTree
## 端到端冒烟：注册 → 收到 LoginReply(token) → 断线 → resume → 收到 onMatched。
##
## 需要活集群（etcd + nats + redis + gate/account/match/game 四进程）。
## 这是唯一验证「登录 → 匹配 → 对局」整条链路的测试，改动 account 服务、
## gate 的会话归属、match 的开局链路后必跑。
##
## 断言在载荷解析失败时显式判失败（不会假通过）—— GDScript 没有 try/catch，
## 中途抛错会静默变成"通过"，所以每一步都打完成标记。

const FpsClient := preload("res://scripts/fps_client.gd")

var _failures := 0
var _c: Node
var _done := {}
var _username := ""
var _phase := 0   # 0=等首轮登录 1=等 resume 2=完成
var _elapsed := 0.0
var _token_seen := ""

const TIMEOUT := 20.0

func _init() -> void:
	_c = FpsClient.new()
	# 每次跑用一个新用户名，避免与上一轮的账号撞名（name_taken）。
	_username = "smoke_%d" % (Time.get_ticks_usec() % 100000000)
	_c.login_result.connect(_on_login)
	_c.matched_received.connect(_on_matched)
	root.add_child(_c)

func _check(cond: bool, msg: String) -> void:
	if not cond:
		_failures += 1
		printerr("FAIL: " + msg)

func _on_login(result: Dictionary) -> void:
	if not bool(result.get("ok", false)):
		if String(result.get("reason", "")) == "no_token" and _phase == 0:
			# 首次连接、本地无凭证：注册一个新账号。
			_c.send_register(_username, "smokepass")
			return
		_failures += 1
		printerr("FAIL: 登录失败 reason=%s（phase=%d）" % [result.get("reason", ""), _phase])
		return

	var token := String(result.get("token", ""))
	_check(token != "", "LoginReply 必须带 token")
	if _phase == 0:
		_check(_c.client_token == token, "客户端应把 token 存进 client_token")
		_token_seen = token
		_phase = 1
		# 模拟断线重连：丢掉本地连接、带着同一个 token 重新走 resume。
		# 用同一份凭证 resume 而不是重新 login —— 后者会轮换 token。
		_c._force_reconnect()
	else:
		_check(token == _token_seen, "resume 返回的 token 应与本地一致，得到 %s" % token)
		_phase = 2

func _on_matched(_result: Dictionary) -> void:
	_done["matched"] = true

func _process(delta: float) -> void:
	_elapsed += delta
	if _phase >= 2 and _done.has("matched"):
		_finish()
		return
	if _elapsed > TIMEOUT:
		_failures += 1
		printerr("FAIL: 超时（phase=%d, matched=%s）" % [_phase, str(_done.has("matched"))])
		_finish()

func _finish() -> void:
	_check(_done.has("matched"), "resume 之后应收到 onMatched（进入对局）")
	if _failures > 0:
		printerr("login_smoke: %d 项失败" % _failures)
		quit(1)
	else:
		print("login_smoke: OK (user=%s)" % _username)
		quit(0)
```

- [ ] **Step 2: 确认集群在跑（若没有则先起）**

```bash
cd /c/Users/zhubeijian/Desktop/Projects/fps/joltgo/deploy && tail -3 account.log 2>/dev/null || echo "not running"
```

Expected: 有日志（说明 account 已起）。若没有，先回到 Task 10 Step 6 起全套。

- [ ] **Step 3: 跑冒烟测试**

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/login_smoke.gd
```

Expected: `login_smoke: OK (user=smoke_XXXXXXXX)`，退出码 0，且无 `SCRIPT ERROR`。

- [ ] **Step 4: 跑回局回归（本次改了 bindPlayer 链路，必须重跑）**

```bash
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd
Godot_v4.7.2-stable_win64_console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
```

Expected: 两个都 OK。`ws_smoke` 的预期输出形如 `SMOKE unique_steps=81 span=80 elapsed_ms=4000`。

⚠️ `rejoin_smoke.gd` 是回局链路的**唯一**端到端验证，它现在也要先登录才能发 join。若它失败在「join 被忽略」上，说明该测试自身还没适配新的登录流程——**修该测试**（在 join 之前先注册/登录），不要改服务端去迁就它。

- [ ] **Step 5: 提交**

```bash
git add godot_client/tests/login_smoke.gd godot_client/tests/rejoin_smoke.gd
git commit -m "test(client): 登录链路端到端冒烟；rejoin_smoke 适配登录流程

login_smoke 注册 → 拿 token → 强制断线 → resume → 收到 onMatched。
rejoin_smoke 在 join 之前补登录步骤（JoinMsg 不再带凭证）。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: 文档同步

**Files:**
- Modify: `AGENTS.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/API.md`
- Modify: `joltgo/deploy/README.md`
- Modify: `godot_client/README.md`
- Modify: `docs/BUILD.md`（依赖与启动说明）

**Interfaces:**
- Consumes: Task 1–11 的最终形态
- Produces: 文档与代码一致

- [ ] **Step 1: 更新 AGENTS.md**

- §2 目录结构：加 `kv/`（Redis 连接）、`online/`（会话归属）、`account/`（账号服务），并在 `gate/` 后标注 `session.go`。
- §4 数据流：把「客户端握手后发 match.join（带持久化 token）」改成「客户端发 account.* 登录（Request/Response）→ 收到 LoginReply 后发 join」；补一句会话 UID = accountID。
- §5 约定与坑：**删掉** `JoinMsg.token` 的旧描述，替换为账号体系的说明；加「Redis 不清数据目录」「Response 帧无 route 字段」两条。
- 「多进程部署」条目：改成四个进程 + redis-server。

- [ ] **Step 2: 更新 docs/ARCHITECTURE.md**

- 数据流一节末尾追加「账号与会话」小节：登录时序（注册/登录/resume 三条路）、会话 UID = accountID、`game.rejoin` 为何零改动。
- 把「已知安全取舍」那段（`JoinMsg.token` 是持有即可冒用的一次性身份）**替换**为：凭证由服务端签发、可吊销、带 7 天过期；**保留**诚实说明——凭证是 bearer，拿到即用，生产应加 TLS 与更短的 TTL。
- 新增「多节点正确性」小节：四个角色的节点本地状态清单（见 spec §8 的表）、在线登记 + 定点踢、token 轮换是两个机制的权威、**推送双发窗口不修的理由**（加 NATS queue group 会让两个客户端各收一半帧流）。

- [ ] **Step 3: 更新 docs/API.md**

- 帧格式表下方加 Response 帧的说明：`flag + mid(LEB128) + payload`，**无 route**，`errorMask=0x20` 时 payload 是错误字符串。
- 新增 route 表：`account.account.register` / `.login` / `.resume`（Request/Response）、`gate.gate.bindgame`（RPC）。
- 「握手流程」一节：把第 4 步的 `match.join`（带 token）改成登录流程，并说明「未登录不得发 join」。
- `JoinMsg` 描述改为空消息。

- [ ] **Step 4: 更新 deploy/README.md**

加「Redis」一节：角色（账号/凭证/会话归属/配对队列）、监听 6379、**为什么不清数据目录**（对照 etcd 那节）、AOF 的作用、`redis-data/` 的说明。启动清单改成 etcd + nats + redis 三件套，服务清单改成四个进程。

- [ ] **Step 5: 更新 godot_client/README.md**

- 加登录界面说明（用户名/密码、登录/注册、回车提交）。
- 加 `user://auth_token.txt` 与 `last_username.txt`（并说明为什么不复用旧的 `client_id.txt`）。
- 自动化测试清单加 `login_reply_decode_test.gd`（无服务端）与 `login_smoke.gd`（需集群）。

- [ ] **Step 6: 更新 docs/BUILD.md**

依赖表加 Redis（本机二进制由 `deploy/` 提供）；「运行」一节的服务清单加 `-type account`；加 `-redis` flag 说明。

- [ ] **Step 7: 检查文档没有残留旧描述**

```bash
cd /c/Users/zhubeijian/Desktop/Projects/fps && grep -rn "client_id.txt\|JoinMsg.token\|三服务\|三个进程" AGENTS.md docs/*.md godot_client/README.md joltgo/deploy/README.md
```

Expected: 只在「为什么不复用 client_id.txt」这类**说明性**语境里出现；任何把它当作现行机制的描述都要改掉。

- [ ] **Step 8: 提交**

```bash
git add AGENTS.md docs godot_client/README.md joltgo/deploy/README.md
git commit -m "docs: 账号体系与多节点部署

同步 AGENTS/ARCHITECTURE/API/BUILD/deploy 与 client README：登录链路、
Response 帧格式、四个进程 + redis、多节点正确性（在线登记 + 定点踢、
凭证轮换是权威）。替换掉旧的「token 持有即可冒用」安全取舍说明。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## 完成标准

全部任务完成后，以下必须全绿：

```bash
# 服务端单测（无需集群）
cd joltgo && PATH="$PWD:$PATH" go build ./... && PATH="$PWD:$PATH" go test -count=1 ./account ./online ./match ./gate ./sim ./replication ./ecs

# 全部包（含 cgo；需已构建 libjolt_c.dll）
cd joltgo && PATH="$PWD:$PATH" go test -count=1 ./...

# 客户端无头回归（无需服务端）
Godot_..._console.exe --headless --path godot_client --script res://tests/login_reply_decode_test.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/frame_decode_test.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/world_store_test.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/game_frame_test.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/reconnect_cleanup_test.gd

# 端到端（需活集群）
Godot_..._console.exe --headless --path godot_client --script res://tests/login_smoke.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/ws_smoke.gd
Godot_..._console.exe --headless --path godot_client --script res://tests/rejoin_smoke.gd
```

外加人工验证：起全套后用 Godot 打开客户端，看到登录面板 → 注册 → 进入对局 → 打几枪 → 关掉客户端重开 → **免登录直接回到同一局**（token 免登录）；再开一个客户端用同一账号登录 → 第一个客户端断开且重连后停在登录面板（顶号生效）。
