package replication

// Kind 是属性值的类型标签。值本身不带维度信息，维度由 Kind 决定。
type Kind uint8

const (
	KindF32  Kind = iota // f[0]
	KindI32              // i
	KindBool             // i != 0
	KindStr              // s
	KindVec2             // f[0..1]
	KindVec3             // f[0..2]
	KindVec4             // f[0..3]
)

// Dim 返回该类型占用的浮点维度；非浮点族返回 0。
func (k Kind) Dim() int {
	switch k {
	case KindF32:
		return 1
	case KindVec2:
		return 2
	case KindVec3:
		return 3
	case KindVec4:
		return 4
	}
	return 0
}

// Value 是属性值：紧凑联合体，按 Kind 取用其中一个字段。
// 不用 any 是因为同步路径每 tick 会有上千次 Set，接口装箱会造成持续堆分配。
type Value struct {
	kind Kind
	f    [4]float32
	i    int32
	s    string
}

func F32(v float32) Value { return Value{kind: KindF32, f: [4]float32{v}} }

func I32(v int32) Value { return Value{kind: KindI32, i: v} }

func Bool(v bool) Value {
	var i int32
	if v {
		i = 1
	}
	return Value{kind: KindBool, i: i}
}

func Str(v string) Value { return Value{kind: KindStr, s: v} }

func Vec2(x, y float32) Value { return Value{kind: KindVec2, f: [4]float32{x, y}} }

func Vec3(x, y, z float32) Value { return Value{kind: KindVec3, f: [4]float32{x, y, z}} }

func Vec4(x, y, z, w float32) Value { return Value{kind: KindVec4, f: [4]float32{x, y, z, w}} }

// Kind 返回值的类型标签。
func (v Value) Kind() Kind { return v.kind }

// Floats 返回浮点族的值，长度等于 Kind().Dim()；标量族返回 nil。
func (v Value) Floats() []float32 {
	if d := v.kind.Dim(); d > 0 {
		return v.f[:d]
	}
	return nil
}

// Int 返回整数族的值（KindI32）；KindBool 用 Boolean 读。
func (v Value) Int() int32 { return v.i }

// Boolean 返回布尔族的值。
func (v Value) Boolean() bool { return v.i != 0 }

// Text 返回字符串族的值。
func (v Value) Text() string { return v.s }

// equal 报告两个值是否相等：类型不同一律不等，否则按各自的族比较。
func (v Value) equal(o Value) bool {
	if v.kind != o.kind {
		return false
	}
	if v.kind == KindStr {
		return v.s == o.s
	}
	if v.kind.Dim() > 0 {
		return v.f == o.f
	}
	return v.i == o.i
}
