package logic

import "testing"

func TestLevelDerivation(t *testing.T) {
	cases := []struct {
		xp    int64
		level int32
		into  int64
	}{
		{xp: 0, level: 1, into: 0},
		{xp: 199, level: 1, into: 199},
		{xp: 200, level: 2, into: 0},
		{xp: 399, level: 2, into: 199},
		{xp: 400, level: 3, into: 0},
		{xp: 1000, level: 6, into: 0},
	}
	for _, tc := range cases {
		if got := Level(tc.xp); got != tc.level {
			t.Fatalf("Level(%d) = %d，期望 %d", tc.xp, got, tc.level)
		}
		if got := XPIntoLevel(tc.xp); got != tc.into {
			t.Fatalf("XPIntoLevel(%d) = %d，期望 %d", tc.xp, got, tc.into)
		}
	}
	// 负经验按 0 处理，不能出现负数等级。
	if got := Level(-50); got != 1 {
		t.Fatalf("Level(-50) = %d，期望 1", got)
	}
	if got := XPIntoLevel(-50); got != 0 {
		t.Fatalf("XPIntoLevel(-50) = %d，期望 0", got)
	}
	if got := XPForNextLevel(); got != XPPerLevel {
		t.Fatalf("XPForNextLevel() = %d，期望 %d", got, XPPerLevel)
	}
}

func TestMatchXP(t *testing.T) {
	if got := MatchXP(true, 10); got != 100 {
		t.Fatalf("胜利 + 10 杀 = %d，期望 100", got)
	}
	if got := MatchXP(false, 3); got != 25 {
		t.Fatalf("失败 + 3 杀 = %d，期望 25", got)
	}
	if got := MatchXP(false, 0); got != XPPerLoss {
		t.Fatalf("失败 0 杀 = %d，期望 %d", got, XPPerLoss)
	}
}
