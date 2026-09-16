package protos

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// messageRe 抓顶层 `message <Name>` 声明。
var messageRe = regexp.MustCompile(`(?m)^\s*message\s+([A-Za-z0-9_]+)`)

// protoMessages 读一个 .proto 并返回它定义的 message 名（排序去重）。
//
// 这里用极简正则而不是 protoc 的解析器：它要守住的是一条**归属规则**，
// 规则本身出错的风险远大于解析出错的风险。
func protoMessages(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, m := range messageRe.FindAllStringSubmatch(string(raw), -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// repoRoot 返回仓库的 joltgo 目录（本测试在 game/protos 里跑）。
func repoRoot() string { return filepath.Join("..", "..") }

func TestProtoLayoutSplitsMessagesByOwner(t *testing.T) {
	root := repoRoot()
	// 变量名刻意避开 gate / match / game：它们是同层的目录与包名，遮蔽会让读者犯嘀咕。
	gateMsgs := strings.Join(protoMessages(t, filepath.Join(root, "gate", "protos", "gate.proto")), ",")
	matchMsgs := strings.Join(protoMessages(t, filepath.Join(root, "match", "protos", "match.proto")), ",")
	gameMsgs := strings.Join(protoMessages(t, filepath.Join(root, "game", "protos", "game.proto")), ",")

	// 客户端主动请求 + 响应：必须在 gate.proto。
	for _, want := range []string{
		"RegisterMsg", "LoginMsg", "ResumeMsg", "LoginReply",
		"LogicStateMsg", "PurchaseMsg", "EquipMsg", "PlayerProfileMsg",
		"LogicStateReply", "PlayerProfileReply", "LogicShopItem", "MatchRecord",
		"JoinMsg", "PendingMatchMsg", "PendingMatchReply",
		"AbandonMatchMsg", "AbandonMatchReply", "MatchCancelMsg", "MatchCancelReply",
		"CommandMsg",
	} {
		if !strings.Contains(gateMsgs, want) {
			t.Errorf("客户端契约消息 %s 应该在 gate.proto 里", want)
		}
	}

	// match 的推送：必须在 match.proto。
	for _, want := range []string{"MatchResult", "MatchStatus"} {
		if !strings.Contains(matchMsgs, want) {
			t.Errorf("match 推送消息 %s 应该在 match.proto 里", want)
		}
	}

	// game 的推送 + 服务内部：必须在 game.proto。
	for _, want := range []string{
		"Frame", "EntityDelta", "AttrValue", "Schema", "SchemaField",
		"MatchEnded", "SlotResult",
		"CreateGameMsg", "RejoinMsg", "LeaveMsg", "BindGameMsg",
		"RecordMatchMsg", "UserOnlineMsg", "AddBotsMsg", "GrantCoinsMsg",
	} {
		if !strings.Contains(gameMsgs, want) {
			t.Errorf("game 推送或内部消息 %s 应该在 game.proto 里", want)
		}
	}
}

// TestProtoLayoutKeepsPushesOutOfGateProto 守住「gate.proto 只装客户端上行契约」。
// 推送一旦混进 gate.proto，白名单的来源就不再是「客户端能发什么」了。
func TestProtoLayoutKeepsPushesOutOfGateProto(t *testing.T) {
	gateMsgs := strings.Join(protoMessages(t, filepath.Join(repoRoot(), "gate", "protos", "gate.proto")), ",")
	for _, pushOnly := range []string{"MatchResult", "MatchStatus", "MatchEnded", "Frame"} {
		if strings.Contains(gateMsgs, pushOnly) {
			t.Errorf("%s 是服务端推送，不该定义在 gate.proto（推送按发送方归文件）", pushOnly)
		}
	}
}

// TestProtoLayoutKeepsSlotResultWithItsUsers 钉住 SlotResult 的位置：
// 它被下发的 MatchEnded 与上报的 RecordMatchMsg 同时引用，两者都在 game.proto，
// 所以它必须留在 game.proto —— 否则要引入跨 proto 的 import。
func TestProtoLayoutKeepsSlotResultWithItsUsers(t *testing.T) {
	gameMsgs := strings.Join(protoMessages(t, filepath.Join(repoRoot(), "game", "protos", "game.proto")), ",")
	for _, want := range []string{"SlotResult", "MatchEnded", "RecordMatchMsg"} {
		if !strings.Contains(gameMsgs, want) {
			t.Errorf("%s 应留在 game.proto", want)
		}
	}
}
