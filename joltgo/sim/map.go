package sim

import "math"

// map.go —— 「运输船」场景（参考穿越火线 运输船）。
//
// 布局：一艘长甲板货船。艏（-z）/ 艉（+z）两个出生区，中部是可跳跃攀爬的
// 集装箱堆，两舷各有一条高架走道（走道梯上、可俯瞰整条船），甲板上散布
// 集装箱与木箱掩体，侧舷立着烟囱、桅杆与系缆桩，艏艉各有掩体墙。
//
// 公平性：整张图在绕 Y 轴旋转 180°（(x,y,z) → (-x,y,-z)）下自映射，两个
// 出生点与掩体完全等价。带 mirror 的部件会自动补上旋转后的孪生体——改图
// 时只写半边即可，对称性由代码保证，不靠人工核对。
//
// 坐标：x 为船宽（±13），z 为船长（±22），甲板面 y = 0（甲板钢板顶面）。
// 玩家跳跃初速 8.5、重力 20 → 跳跃高度约 1.8 m：≤1.4 m 的掩体可直接跳上，
// 2.4 m 的高架走道只能走楼梯。

// Material 是刚体的视觉材质，随快照下发（proto 字段 mat），客户端据此配色。
// 编号是 wire 契约的一部分，只能追加、不能改号。
type Material int

const (
	MatDefault    Material = 0  // 未指定（客户端按默认刚体配色）
	MatDeck       Material = 1  // 甲板钢板
	MatHull       Material = 2  // 船体外板 / 舱壁
	MatContainerA Material = 3  // 集装箱 · 蓝
	MatContainerB Material = 4  // 集装箱 · 红
	MatContainerC Material = 5  // 集装箱 · 绿
	MatContainerD Material = 6  // 集装箱 · 橙
	MatCatwalk    Material = 7  // 高架走道（格栅钢）
	MatRailing    Material = 8  // 栏杆
	MatCrate      Material = 9  // 木箱（动态）
	MatSteel      Material = 10 // 桅杆 / 烟囱 / 系缆桩
	MatStairs     Material = 11 // 舷梯踏步
)

// 场景尺寸常量（供测试与角色出生点引用，避免各处写死坐标）。
const (
	deckHalfX  = 13.0 // 甲板半宽（x ∈ [-13, 13]）
	deckHalfZ  = 22.0 // 甲板半长（z ∈ [-22, 22]）
	spawnZ     = 18.0 // 两个出生点到中心的纵向距离
	catwalkTop = 2.4  // 高架走道面高度
)

// scenePos 是场景数据表里的一个坐标（x, y, z）。
type scenePos [3]float32

// shipBox 是轴对齐静态方块（地板/舱壁/集装箱/走道/栏杆/台阶）。
type shipBox struct {
	hx, hy, hz float32
	x, y, z    float32
	mat        Material
	mirror     bool // 同时生成绕 Y 轴旋转 180° 的孪生体
}

// shipCapsule 是静态胶囊（桅杆/烟囱/系缆桩）。Jolt 胶囊沿 Y 轴，不能倾斜，
// 因此只能当立柱用。
type shipCapsule struct {
	radius, half float32
	x, y, z      float32
	mat          Material
	mirror       bool
}

// shipStairs 是一段舷梯：steps 级实心踏步，每级抬高 rise、进深 dz，从最低
// 一级（中心 z 为 z）沿 dir 方向逐级升高。实心踏步（每级都从甲板堆到自身
// 顶面）而不是悬空薄板——既省得算出悬空几何，也保证角色控制器能走上去。
type shipStairs struct {
	x, z, hx, dz, rise float32
	steps              int
	dir                float32 // +1 = 沿 +z 升高；-1 = 沿 -z 升高
	mat                Material
	mirror             bool
}

// ---- 船体：甲板 + 四周围板（自身对称，不需要镜像） ----

var hullBoxes = []shipBox{
	// 甲板钢板（顶面 y = 0）。
	{hx: 13, hy: 1, hz: 22, x: 0, y: -1, z: 0, mat: MatDeck},
	// 左右舷外板（与甲板边缘重叠 0.5，避免出现可掉下去的缝）。
	{hx: 1, hy: 3.5, hz: 22.5, x: -13.5, y: 2.5, z: 0, mat: MatHull},
	{hx: 1, hy: 3.5, hz: 22.5, x: 13.5, y: 2.5, z: 0, mat: MatHull},
	// 艏艉横舱壁。
	{hx: 14.5, hy: 3.5, hz: 1, x: 0, y: 2.5, z: -22.5, mat: MatHull},
	{hx: 14.5, hy: 3.5, hz: 1, x: 0, y: 2.5, z: 22.5, mat: MatHull},
}

// ---- 中部集装箱堆：三层，可跳上去（甲板 0 → 1.4 → 2.4）。
// 两侧的矮箱用同一种颜色：整组在 180° 旋转下自映射，两名玩家的掩体完全一致。

var midBoxes = []shipBox{
	{hx: 2.2, hy: 0.7, hz: 2.2, x: -2.6, y: 0.7, z: 0, mat: MatContainerB},
	{hx: 2.2, hy: 1.2, hz: 2.2, x: 0, y: 1.2, z: 0, mat: MatContainerA},
	{hx: 2.2, hy: 0.7, hz: 2.2, x: 2.6, y: 0.7, z: 0, mat: MatContainerB},
}

// ---- 甲板掩体与舷侧结构（写半边，mirror 补孪生体） ----

var deckBoxes = []shipBox{
	// 两舷走道（面高 2.4，板厚 0.3）：贴舷侧的通长高台，下方留出可通行的净空。
	{hx: 1.1, hy: 0.15, hz: 13, x: 11.2, y: 2.25, z: 0, mat: MatCatwalk, mirror: true},
	// 走道外侧栏杆（只挡向海一侧，内侧敞开便于跳下甲板）。
	{hx: 0.1, hy: 0.5, hz: 13, x: 12.15, y: 2.9, z: 0, mat: MatRailing, mirror: true},

	// 舷侧长集装箱：贴着走道下方的甲板，顶面 1.4（可跳上）。
	{hx: 1.5, hy: 0.7, hz: 3.2, x: 8.2, y: 0.7, z: 7.5, mat: MatContainerD, mirror: true},
	{hx: 1.4, hy: 1.2, hz: 2.0, x: 8.2, y: 1.2, z: -3.8, mat: MatContainerA, mirror: true},
	{hx: 1.5, hy: 0.7, hz: 3.2, x: 8.2, y: 0.7, z: -9.0, mat: MatContainerC, mirror: true},

	// 出生区掩体墙：中间留出中央通道，两侧各留一条舷侧绕后路线。
	{hx: 2.2, hy: 1.2, hz: 0.7, x: 5.5, y: 1.2, z: -16.5, mat: MatContainerD, mirror: true},
	{hx: 2.2, hy: 1.2, hz: 0.7, x: -5.5, y: 1.2, z: -16.5, mat: MatContainerC, mirror: true},
}

// ---- 舷梯：走道在右舷艉端、左舷艏端各一段（镜像后两舷各有且仅有一段） ----

var deckStairs = []shipStairs{
	// 右舷艉端：8 级 × 0.3 = 2.4，最高一级正好贴住走道（走道 z 端 13.0）。
	// 进深 0.56 > 角色半径 0.4：踏步比角色还窄时，Jolt 的 WalkStairs 抬腿后
	// 会被再下一级绊住（实测 0.34 进深走不上去，≥0.5 可以），必须留够进深。
	{x: 11.2, z: 17.2, hx: 1.1, dz: 0.56, rise: 0.3, steps: 8, dir: -1, mat: MatStairs, mirror: true},
}

// ---- 舷侧立柱：烟囱、桅杆、系缆桩 ----

var deckCapsules = []shipCapsule{
	// 烟囱：两舷中部的显著地标（也是把中央战场与大走道隔开的掩体）。
	{radius: 1.0, half: 2.4, x: 9.0, y: 2.4, z: 0, mat: MatSteel, mirror: true},
	// 桅杆：艏艉中线上各一根。
	{radius: 0.35, half: 5.0, x: 0, y: 5.0, z: -20.5, mat: MatSteel, mirror: true},
	// 系缆桩：舷边小柱（避开舷梯所在的 z 段）。
	{radius: 0.22, half: 0.45, x: 12.0, y: 0.45, z: -19.5, mat: MatSteel, mirror: true},
	{radius: 0.22, half: 0.45, x: 12.0, y: 0.45, z: 19.5, mat: MatSteel, mirror: true},
}

// ---- 木箱（动态刚体，可被弹丸挡下、可被玩家推开） ----

var deckCratePositions = []scenePos{
	{0.0, 0.5, 6.0}, {-1.7, 0.5, 6.0}, {1.7, 0.5, 6.0}, {0.0, 1.5, 6.0},
	{0.0, 0.5, -6.0}, {-1.7, 0.5, -6.0}, {1.7, 0.5, -6.0}, {0.0, 1.5, -6.0},
	{6.4, 0.5, 2.0}, {6.4, 0.5, 0.4}, {-6.4, 0.5, -2.0}, {-6.4, 0.5, -0.4},
	{-6.4, 0.5, 2.0}, {6.4, 0.5, -2.0},
	{0.0, 0.5, 14.0}, {0.0, 0.5, -14.0},
	{9.4, 0.5, 12.0}, {-9.4, 0.5, -12.0},
}

// shipBoxParts / shipCapsuleParts 是展开镜像与台阶后的最终部件表，
// 也是场景刚体数量的唯一来源（测试直接引用，不再手写期望值）。
var (
	shipBoxParts     = expandShipBoxes()
	shipCapsuleParts = expandShipCapsules()
)

func mirrorBox(b shipBox) shipBox { b.x, b.z, b.mirror = -b.x, -b.z, false; return b }

func expandShipBoxes() []shipBox {
	out := make([]shipBox, 0, 64)
	add := func(b shipBox) {
		out = append(out, b)
		if b.mirror {
			out = append(out, mirrorBox(b))
		}
	}
	for _, b := range hullBoxes {
		add(b)
	}
	for _, b := range midBoxes {
		add(b)
	}
	for _, b := range deckBoxes {
		add(b)
	}
	for _, st := range deckStairs {
		for _, step := range stairSteps(st) {
			step.mirror = st.mirror // 踏步自己不带 mirror，继承所属舷梯的
			add(step)
		}
	}
	return out
}

// stairSteps 把一段舷梯展开成 steps 级实心踏步（每级从甲板堆到自身顶面）。
func stairSteps(st shipStairs) []shipBox {
	out := make([]shipBox, 0, st.steps)
	for i := 0; i < st.steps; i++ {
		top := st.rise * float32(i+1)
		out = append(out, shipBox{
			hx: st.hx, hy: top / 2, hz: st.dz / 2,
			x: st.x, y: top / 2, z: st.z + st.dir*st.dz*float32(i),
			mat: st.mat,
		})
	}
	return out
}

func expandShipCapsules() []shipCapsule {
	out := make([]shipCapsule, 0, 8)
	for _, c := range deckCapsules {
		out = append(out, c)
		if c.mirror {
			out = append(out, shipCapsule{radius: c.radius, half: c.half, x: -c.x, y: c.y, z: -c.z, mat: c.mat})
		}
	}
	return out
}

// mapBlocks 报告以 (x,y,z) 为中心、半径 margin 的球体是否与场景静态几何相交。
// 场景里没有运行期随机撒点（出生/复活点都是固定的、由 map_test.go 断言其畅通），
// 所以这里是**地图自检**用的几何查询：改部件表后用 test 直接验「这个位置塞不塞得下
// 一个半径 margin 的球」，而不必起物理引擎。
func mapBlocks(x, y, z, margin float32) bool {
	for _, b := range shipBoxParts {
		if x > b.x-b.hx-margin && x < b.x+b.hx+margin &&
			y > b.y-b.hy-margin && y < b.y+b.hy+margin &&
			z > b.z-b.hz-margin && z < b.z+b.hz+margin {
			return true
		}
	}
	for _, c := range shipCapsuleParts {
		dx, dz := x-c.x, z-c.z
		r := c.radius + margin
		if dx*dx+dz*dz < r*r && y > c.y-c.half-margin && y < c.y+c.half+margin {
			return true
		}
	}
	return false
}

// playerSpawnXZ 返回玩家槽位 i 的出生点水平坐标：艉 / 艏两个对称出生区，
// 两人隔着整条甲板对望（客户端按槽位设置初始朝向）。
func playerSpawnXZ(i int) (float32, float32) {
	if i == 1 {
		return 0, -spawnZ
	}
	return 0, spawnZ
}

// playerSpawnYaw 返回玩家槽位 i 出生时的水平朝向（弧度，绕 Y 轴）：面朝船中。
func playerSpawnYaw(i int) float32 {
	if i == 1 {
		return math.Pi
	}
	return 0
}
