package sim

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"joltgo/ecs"
	"joltgo/replication"
)

// expectedAttrs 是「世界应有属性集」的独立实现 —— 它是 oracle：
// 不看 replicate.go 怎么写，只从 ECS 世界直接读出应有的 (实体, 属性, 终值)。
// 任何漏写的 rep.Set 都会在 assertStoreMatchesWorld 里失败。
func expectedAttrs(s *Simulation) map[uint32]map[string]replication.Value {
	out := map[uint32]map[string]replication.Value{}
	put := func(id uint32, attr string, v replication.Value) {
		m := out[id]
		if m == nil {
			m = map[string]replication.Value{}
			out[id] = m
		}
		m[attr] = v
	}

	// 全局状态单例实体
	if gs, ok := ecs.Get[GameState](s.world, s.game); ok {
		id := uint32(s.game)
		put(id, attrGameScore, replication.I32(gs.Score))
		put(id, attrGameWave, replication.I32(gs.Wave))
		put(id, attrGameGold, replication.I32(gs.Gold))
	}

	// 玩家
	for i := 0; i < MaxPlayers; i++ {
		e := s.players[i]
		if e == ecs.InvalidEntity {
			continue
		}
		id := uint32(e)
		p, _ := ecs.Get[Position](s.world, e)
		h, _ := ecs.Get[Health](s.world, e)
		f, _ := ecs.Get[Facing](s.world, e)
		pl, _ := ecs.Get[Player](s.world, e)
		put(id, attrPos, replication.Vec3(p[0], p[1], p[2]))
		put(id, attrHealth, replication.F32(float32(*h)))
		put(id, attrFacing, replication.F32(f.Yaw))
		put(id, attrPlayerIdx, replication.I32(int32(pl.Idx)))
	}

	// 刚体
	ecs.Each(s.world, func(e ecs.Entity, b *Body) {
		id := uint32(e)
		p, _ := ecs.Get[Position](s.world, e)
		rot, _ := ecs.Get[Rotation](s.world, e)
		put(id, attrBodyKind, replication.I32(int32(b.Kind)))
		put(id, attrBodySize, replication.Vec3(b.Size[0], b.Size[1], b.Size[2]))
		put(id, attrBodyStatic, replication.Bool(b.Static))
		put(id, attrBodyActive, replication.Bool(b.Active))
		put(id, attrBodyMat, replication.I32(int32(b.Mat)))
		put(id, attrPos, replication.Vec3(p[0], p[1], p[2]))
		put(id, attrRot, replication.Vec4(rot[0], rot[1], rot[2], rot[3]))
		if h, ok := ecs.Get[Health](s.world, e); ok {
			put(id, attrHealth, replication.F32(float32(*h)))
		}
		if ecs.Has[Enemy](s.world, e) {
			put(id, attrEnemy, replication.Bool(true))
		}
		if ecs.Has[Target](s.world, e) {
			put(id, attrTarget, replication.Bool(true))
		}
		if ecs.Has[Projectile](s.world, e) {
			put(id, attrProjectile, replication.Bool(true))
		}
		if r, ok := ecs.Get[Resource](s.world, e); ok {
			put(id, attrResourceKind, replication.I32(int32(r.Kind)))
		}
	})
	return out
}

// storeAttrs 把 store 的全量帧摊平成与 expectedAttrs 同构的映射。
func storeAttrs(s *Simulation) map[uint32]map[string]replication.Value {
	f := s.FullFrame()
	name := map[uint32]string{}
	for _, a := range f.Schema.Fields {
		name[a.ID] = a.Name
	}
	out := map[uint32]map[string]replication.Value{}
	for _, ed := range f.Entities {
		m := map[string]replication.Value{}
		for _, av := range ed.Set {
			m[name[av.Attr]] = av.Value
		}
		out[ed.ID] = m
	}
	return out
}

func assertStoreMatchesWorld(t *testing.T, s *Simulation) {
	t.Helper()
	want := expectedAttrs(s)
	got := storeAttrs(s)

	for id, wm := range want {
		gm, ok := got[id]
		if !ok {
			t.Fatalf("实体 %d 在世界里存在，但同步 store 里没有", id)
		}
		for attr, wv := range wm {
			gv, ok := gm[attr]
			if !ok {
				t.Fatalf("实体 %d 缺少属性 %s（漏写 rep.Set？）", id, attr)
			}
			if !valueEqual(wv, gv) {
				t.Fatalf("实体 %d 属性 %s 不一致：世界 %s vs store %s", id, attr, describe(wv), describe(gv))
			}
		}
	}
	for id, gm := range got {
		if _, ok := want[id]; !ok {
			t.Fatalf("同步 store 里有世界里不存在的实体 %d", id)
		}
		for attr := range gm {
			if _, ok := want[id][attr]; !ok {
				t.Fatalf("实体 %d 有世界里不存在的属性 %s", id, attr)
			}
		}
	}
}

// describe 把属性值渲染成可读文本。不能直接用 Floats()：它对整数/布尔返回 nil，
// 会让这两类属性的失败信息变成两个空切片，看不到到底是哪个值不对。
func describe(v replication.Value) string {
	switch {
	case v.Kind().Dim() > 0:
		return fmt.Sprintf("%v", v.Floats())
	case v.Kind() == replication.KindStr:
		return strconv.Quote(v.Text())
	case v.Kind() == replication.KindBool:
		return strconv.FormatBool(v.Boolean())
	default:
		return strconv.FormatInt(int64(v.Int()), 10)
	}
}

// valueEqual 用一个短小的往返把两边都归一成可比形式（同 Kind 才算相等）。
func valueEqual(a, b replication.Value) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	if a.Kind() == replication.KindStr {
		return a.Text() == b.Text()
	}
	if a.Kind().Dim() > 0 {
		return reflect.DeepEqual(a.Floats(), b.Floats())
	}
	if a.Kind() == replication.KindBool {
		return a.Boolean() == b.Boolean()
	}
	return a.Int() == b.Int()
}

func TestStoreMatchesWorldThroughoutMatch(t *testing.T) {
	s, p := newTestSim(t)
	assertStoreMatchesWorld(t, s)

	// 跑一段：物理步进、刷怪、接触伤害都要覆盖到。
	for i := 0; i < 40; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}

	// 弹丸存活期间必须被同步（Projectile 属性只在创建时 Set 一次，而别的用例里
	// 弹丸都在创建的同一 tick 就被销毁 —— 不单独跑这一条，漏写这个 Set 不会被发现）。
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 弹丸命中靶球（摧毁实体）
	target := targetsOf(snapshotWorld(s))[0]
	p.queueContact(proj, target)
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 玩家朝向：yaw 必须真的变过才验证得到 Facing 的 Set（出生朝向是 0）。
	s.ApplyInput(0, [2]float32{0, 0}, 0.7, false)
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 刚体的变换必须跟着物理走。fake 平时既不移动也不旋转刚体，所以这里手动推一下；
	// 必须挑动态刚体（静态船体的 active 恒为 false，翻转不出变化）。
	var dyn uint32
	for _, b := range snapshotWorld(s).Bodies {
		if !b.Static {
			dyn = b.ID
			break
		}
	}
	if dyn == 0 {
		t.Fatal("场景里应有动态刚体（木箱）")
	}

	p.moveBody(dyn, [3]float32{1.5, 2.5, 3.5})
	p.setBodyQuat(dyn, [4]float32{0, 0.70710678, 0, 0.70710678})
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 休眠状态翻转：Body.Active 只在值真的变了才 Set，必须真的翻过才验证得到。
	p.setBodyActive(dyn, false)
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 玩家拾取金币。初始金币是随机撒的，理论上可能一枚都没撒上（既有随机性，
	// 见 TestInitialSnapshot 的同源 flake），所以先判空再取下标。
	if res := snapshotWorld(s).Resources; len(res) > 0 {
		p.queueCharacterContact(uint32(res[0].ID))
		s.Step()
		assertStoreMatchesWorld(t, s)
	}

	// 敌人贴身伤害。**每 tick 都断言**：玩家复活那一帧会同时改 Health 与 Position，
	// 只在循环外断言的话，下一 tick 的同步会把两边都修好、漏写的 Set 就抓不住了。
	enemy := enemiesOf(snapshotWorld(s))[0]
	p.queueCharacterContact(enemy)
	for i := 0; i < 300; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}
}

// 波次推进：击杀全场敌人 -> 清波 waveDelayTicks 后刷出新的一波（会新建实体）。
// 单独成测是因为「真的把敌人打死」需要 enemyHealth 次命中；waveSystem 的
// wave++ 与运行期的 spawnEnemy 只有走到这里才会被覆盖到。
func TestStoreMatchesWorldAcrossWaveAdvance(t *testing.T) {
	s, p := newTestSim(t)
	before := s.wave

	for _, e := range enemiesOf(snapshotWorld(s)) {
		for hit := 0; hit < enemyHealth; hit++ {
			proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
			p.queueContact(proj, e)
			s.Step()
			assertStoreMatchesWorld(t, s)
		}
	}

	for i := 0; i < waveDelayTicks+10; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}
	if s.wave <= before {
		t.Fatalf("清波后应刷出新的一波（否则运行期 spawnEnemy 与 wave++ 都没被覆盖），wave 仍是 %d", s.wave)
	}
}

// 需求 3：把增量流喂给一个「客户端 store」，重建结果必须等于直接取全量。
func TestDeltaStreamRebuildsFullState(t *testing.T) {
	s, p := newTestSim(t)

	// 客户端从服务端 schema 建立同一套属性表。注意增量帧不带 schema
	// （只有 full 帧带），所以名字表要在循环外建好。
	schema := s.FullFrame().Schema
	name := map[uint32]string{}
	client := replication.New()
	for _, a := range schema.Fields {
		name[a.ID] = a.Name
		client.Declare(a.Name, a.Kind)
	}

	// 注意：destroy 之后**不能** continue —— 同帧销毁+重建时，同一个 EntityDelta
	// 里既有 destroy 也有新实体的 set，丢掉 set 客户端就再也收不到重建的实体。
	applyFrame := func(f replication.Frame) {
		for _, ed := range f.Entities {
			if ed.Destroy {
				client.Destroy(ed.ID)
			}
			for _, cid := range ed.Removed {
				client.Remove(ed.ID, name[cid])
			}
			for _, av := range ed.Set {
				client.Set(ed.ID, name[av.Attr], av.Value)
			}
		}
	}

	for i := 0; i < 60; i++ {
		s.Step()
		applyFrame(s.DrainFrame())
	}

	// 「只销毁、不重建」的帧——生产里最常见的路径（弹丸过期、金币被拾取、
	// 敌人被击杀），而下面 Reset 那条路径只会产生 destroy+set。
	// 不单独走一遍的话，客户端就算完全忽略 destroy 也测不出来。
	doomed := enemiesOf(snapshotWorld(s))[0]
	if _, seen := storeAttrsOf(client)[doomed]; !seen {
		t.Fatalf("客户端本应已收到敌人 %d，否则这个用例覆盖不到销毁", doomed)
	}
	for _, e := range enemiesOf(snapshotWorld(s)) {
		for hit := 0; hit < enemyHealth; hit++ {
			proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
			p.queueContact(proj, e)
			s.Step()
			applyFrame(s.DrainFrame())
		}
	}
	if attrs, still := storeAttrsOf(client)[doomed]; still {
		t.Fatalf("敌人 %d 已被击杀，客户端 store 里不应还有它：%+v", doomed, attrs)
	}

	// 场景重建走的是「所有旧实体各发一条 destroy、随后整体重建」的路径，
	// 而且刚体 id 会从头复用 —— 正好覆盖上面 destroy+set 同帧那个分支。
	s.Reset()
	for i := 0; i < 5; i++ {
		s.Step()
		applyFrame(s.DrainFrame())
	}

	// 客户端 store 从零开始，只吃增量流；跑完应与服务端的全量逐项相等。
	got := storeAttrsOf(client)
	want := expectedAttrs(s)
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Fatal("增量重建的客户端状态与全量不一致")
	}
}

// storeAttrsOf 与 storeAttrs 相同，但作用于任意 Store。
func storeAttrsOf(st *replication.Store) map[uint32]map[string]replication.Value {
	f := st.Full()
	name := map[uint32]string{}
	for _, a := range f.Schema.Fields {
		name[a.ID] = a.Name
	}
	out := map[uint32]map[string]replication.Value{}
	for _, ed := range f.Entities {
		m := map[string]replication.Value{}
		for _, av := range ed.Set {
			m[name[av.Attr]] = av.Value
		}
		out[ed.ID] = m
	}
	return out
}

// normalize 把 map 转成可 DeepEqual 的稳定形式（Value 有未导出字段，
// reflect.DeepEqual 能比，但 map 键序不影响 —— 这里只做一层拷贝以确保比较的是内容）。
func normalize(m map[uint32]map[string]replication.Value) map[uint32]map[string]replication.Value {
	out := map[uint32]map[string]replication.Value{}
	for id, am := range m {
		c := map[string]replication.Value{}
		for k, v := range am {
			c[k] = v
		}
		out[id] = c
	}
	return out
}
