package sim

import "testing"

// 结算快照必须「取走即清」，否则 game 侧会重复上报同一场。
func TestDrainOutcomeIsTakeOnce(t *testing.T) {
	s := New(newFakePhysics())
	s.Init()
	defer s.Shutdown()

	if _, ok := s.DrainOutcome(); ok {
		t.Fatal("还没分出胜负时不应有结算快照")
	}

	// 直接写分数（同包测试可以直接访问 score），省掉打 10 次真实命中。
	s.score[0] = PlayerScore{Kills: killTarget, Deaths: 4}
	s.score[1] = PlayerScore{Kills: 3, Deaths: killTarget}
	for i := 0; i < 3; i++ {
		s.Step()
	}

	out, ok := s.DrainOutcome()
	if !ok {
		t.Fatal("判出胜负后应有结算快照")
	}
	if out.WinnerSlot != 0 {
		t.Fatalf("WinnerSlot = %d，期望 0", out.WinnerSlot)
	}
	if out.Kills[0] != killTarget || out.Kills[1] != 3 || out.Deaths[0] != 4 || out.Deaths[1] != killTarget {
		t.Fatalf("结算快照的 k/d 不对: %+v", out)
	}
	if out.DurationSeconds != int32(s.step/20) {
		t.Fatalf("DurationSeconds = %d，期望 %d", out.DurationSeconds, s.step/20)
	}
	if _, ok := s.DrainOutcome(); ok {
		t.Fatal("第二次 DrainOutcome 必须返回 ok=false（取走即清）")
	}
}

// 判出胜负后不得再重开一局：分数与 winner 都要停在那里等 game 侧终结实例。
func TestWinnerStopsTheRound(t *testing.T) {
	s := New(newFakePhysics())
	s.Init()
	defer s.Shutdown()

	s.score[1] = PlayerScore{Kills: killTarget}
	for i := 0; i < 3; i++ {
		s.Step()
	}
	winnerStep := s.step
	winner := s.winner
	if winner != 1 {
		t.Fatalf("winner = %d，期望 1", winner)
	}

	// 旧实现会在 matchOverTicks（5 秒 = 100 tick）后自动 Reset；这里推 200 tick。
	for i := 0; i < 200; i++ {
		s.Step()
	}
	if s.winner != winner {
		t.Fatalf("胜负判定后不应被重开，winner 变成了 %d", s.winner)
	}
	if s.score[1].Kills != killTarget {
		t.Fatalf("胜负判定后分数不应被清零，得到 %d", s.score[1].Kills)
	}
	if s.step <= winnerStep {
		t.Fatalf("tick 应继续推进（实例由 game 侧停止），step=%d", s.step)
	}
}
