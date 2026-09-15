package logic

// 等级与经验的数值全部集中在这里：等级**不落库**，由 XP 现算，所以曲线随时可调，
// 既不需要迁移存量数据，也不可能出现「等级字段与经验不同步」的脏数据。
const (
	XPPerWin  int64 = 50
	XPPerLoss int64 = 10
	XPPerKill int64 = 5

	// XPPerLevel 是升一级所需经验。当前曲线是线性的；换成非线性曲线时只要改这三个
	// 函数，客户端不用动（它只消费下发的 level / xp_into_level / xp_for_next_level）。
	XPPerLevel int64 = 200
)

// Level 从累计经验派生等级：0 XP = 1 级，200 XP = 2 级。
func Level(xp int64) int32 {
	if xp < 0 {
		xp = 0
	}
	return int32(1 + xp/XPPerLevel)
}

// XPIntoLevel 是当前等级内已积累的经验。
func XPIntoLevel(xp int64) int64 {
	if xp < 0 {
		xp = 0
	}
	return xp - int64(Level(xp)-1)*XPPerLevel
}

// XPForNextLevel 是升到下一级需要的经验总量（线性曲线下恒等于 XPPerLevel）。
func XPForNextLevel() int64 { return XPPerLevel }

// MatchXP 是一局结束时获得的经验：胜/负基础值 + 各自的击杀数。
func MatchXP(won bool, kills int32) int64 {
	base := XPPerLoss
	if won {
		base = XPPerWin
	}
	return base + int64(kills)*XPPerKill
}
