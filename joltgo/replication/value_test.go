package replication

import "testing"

func TestFloatsByKind(t *testing.T) {
	cases := []struct {
		name string
		v    Value
		dim  int
		want []float32
	}{
		{"F32", F32(1.5), 1, []float32{1.5}},
		{"Vec2", Vec2(1, 2), 2, []float32{1, 2}},
		{"Vec3", Vec3(1, 2, 3), 3, []float32{1, 2, 3}},
		{"Vec4", Vec4(1, 2, 3, 4), 4, []float32{1, 2, 3, 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.v.Kind().Dim(); got != c.dim {
				t.Fatalf("Dim() = %d，期望 %d", got, c.dim)
			}
			got := c.v.Floats()
			if len(got) != len(c.want) {
				t.Fatalf("Floats() 长度 = %d，期望 %d", len(got), len(c.want))
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("Floats()[%d] = %v，期望 %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestScalarKindsHaveNoFloats(t *testing.T) {
	for _, v := range []Value{I32(3), Bool(true), Str("x")} {
		if v.Floats() != nil {
			t.Fatalf("标量类型的 Floats() 应为 nil，得到 %v", v.Floats())
		}
	}
}

func TestScalarAccessors(t *testing.T) {
	if I32(-7).Int() != -7 {
		t.Fatal("I32().Int() 往返失败")
	}
	if !Bool(true).Boolean() || Bool(false).Boolean() {
		t.Fatal("Bool().Boolean() 往返失败")
	}
	if Str("abc").Text() != "abc" {
		t.Fatal("Str().Text() 往返失败")
	}
}

func TestEqual(t *testing.T) {
	if !Vec3(1, 2, 3).equal(Vec3(1, 2, 3)) {
		t.Fatal("同类型同值应相等")
	}
	if Vec3(1, 2, 3).equal(Vec3(1, 2, 3.5)) {
		t.Fatal("值不同不应相等")
	}
	if F32(1).equal(I32(1)) {
		t.Fatal("类型不同不应相等")
	}
	if !Str("a").equal(Str("a")) {
		t.Fatal("字符串同值应相等")
	}
	if Bool(true).equal(Bool(false)) {
		t.Fatal("bool 不同值不应相等")
	}
}
