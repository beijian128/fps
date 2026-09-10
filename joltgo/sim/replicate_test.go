package sim

import (
	"reflect"
	"testing"

	"joltgo/ecs"
	"joltgo/replication"
)

// snapshotWorld 的临时副本（Task 6 移入 sim_test.go 并删除此处）。
func snapshotWorld(s *Simulation) State { return s.snapshot() }

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
				t.Fatalf("实体 %d 属性 %s 不一致：世界 %v vs store %v", id, attr, wv.Floats(), gv.Floats())
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

	// 跑一段：物理步进、弹丸命中、接触伤害、拾取、刷怪都要覆盖到。
	for i := 0; i < 40; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}

	// 射击 -> 命中靶球（摧毁实体）
	target := targetsOf(snapshotWorld(s))[0]
	proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
	p.queueContact(proj, target)
	s.Step()
	assertStoreMatchesWorld(t, s)

	// 玩家拾取金币。初始金币是随机撒的，理论上可能一枚都没撒上（既有随机性，
	// 见 TestInitialSnapshot 的同源 flake），所以先判空再取下标。
	if res := snapshotWorld(s).Resources; len(res) > 0 {
		p.queueCharacterContact(uint32(res[0].ID))
		s.ApplyInput(0, [2]float32{0, 0}, 0, false)
		s.Step()
		assertStoreMatchesWorld(t, s)
	}

	// 敌人贴身伤害 + 复活
	enemy := enemiesOf(snapshotWorld(s))[0]
	p.queueCharacterContact(enemy)
	for i := 0; i < 300; i++ {
		s.Step()
	}
	assertStoreMatchesWorld(t, s)

	// 波次推进：击杀全场敌人，等 2 秒（waveDelayTicks=40）应刷出新的一波
	// —— 这会新建实体，必须同样被同步到。
	for round := 0; round < 2; round++ {
		for _, e := range enemiesOf(snapshotWorld(s)) {
			proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
			p.queueContact(proj, e)
			s.Step()
			assertStoreMatchesWorld(t, s)
		}
		for i := 0; i < 50; i++ {
			s.Step()
			assertStoreMatchesWorld(t, s)
		}
	}
	if len(enemiesOf(snapshotWorld(s))) == 0 {
		t.Fatal("清波 2 秒后应刷出下一波敌人（否则这个用例没覆盖到新建实体）")
	}
}

// TestStoreMatchesWorldAcrossWaveAdvance 补上上一个用例没真正覆盖的一段：
// 每只敌人有 3 点血，而上面「清波」循环每轮每只只打 1 发，敌人根本不会死，
// 所以 waveSystem 的 wave++、运行时 spawnEnemy、击杀掉落 spawnResource 都没被
// 跑到（那个 len==0 的守卫因此永远为真，是空转的）。这里真的清场来覆盖它们。
func TestStoreMatchesWorldAcrossWaveAdvance(t *testing.T) {
	s, p := newTestSim(t)
	assertStoreMatchesWorld(t, s)

	// 一次 tick 内击杀全部敌人（每只打满 3 发），顺带走击杀掉落。
	for _, enemy := range enemiesOf(snapshotWorld(s)) {
		for i := 0; i < 3; i++ {
			proj := s.Shoot(shoot0(), [3]float32{1, 0, 0})
			p.queueContact(proj, enemy)
		}
	}
	s.Step()
	assertStoreMatchesWorld(t, s) // 记分与掉落都必须已同步

	if got := len(enemiesOf(snapshotWorld(s))); got != 0 {
		t.Fatalf("一次 tick 内每只打满 3 发应清空全场，还剩 %d", got)
	}

	// 清波 2 秒后刷出新一波：wave++ 与运行时新建的敌人都必须被同步，
	// 否则客户端会永远停在旧波次、且看不到新怪。
	for i := 0; i < waveDelayTicks+5; i++ {
		s.Step()
		assertStoreMatchesWorld(t, s)
	}
	if s.wave != 2 {
		t.Fatalf("清波 2 秒后波次应推进到 2，得到 %d", s.wave)
	}
	if got := len(enemiesOf(snapshotWorld(s))); got != initialEnemies+1 {
		t.Fatalf("第 2 波应有 %d 只敌人，得到 %d", initialEnemies+1, got)
	}
}

// 需求 3：把增量流喂给一个「客户端 store」，重建结果必须等于直接取全量。
func TestDeltaStreamRebuildsFullState(t *testing.T) {
	s, _ := newTestSim(t)

	// 客户端从服务端 schema 建立同一套属性表。注意增量帧不带 schema
	// （只有 full 帧带），所以名字表要在循环外建好。
	schema := s.FullFrame().Schema
	name := map[uint32]string{}
	client := replication.New()
	for _, a := range schema.Fields {
		name[a.ID] = a.Name
		client.Declare(a.Name, a.Kind)
	}

	applyFrame := func(f replication.Frame) {
		for _, ed := range f.Entities {
			if ed.Destroy {
				client.Destroy(ed.ID)
				continue
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
