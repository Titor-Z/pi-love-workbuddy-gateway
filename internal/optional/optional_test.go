package optional

import "testing"

// fakeReader 用来验「类型化的 nil」——本包存在的唯一理由。
type fakeReader struct{ name string }

func (f *fakeReader) Name() string { return f.name }

// TestCastTypedNilBecomesInterfaceNil 赋进接口的那一刻就要变成真 nil。
// 这正是本包存在的理由：留到调用时判，调用点往往在别处，来不及。
func TestCastTypedNilBecomesInterfaceNil(t *testing.T) {
	var typedNil *fakeReader
	if got := Cast[interface{ Name() string }](typedNil); got != nil {
		t.Errorf("类型化的 nil 应当变成接口 nil，得到 %#v", got)
	}
	real := &fakeReader{name: "有"}
	if got := Cast[interface{ Name() string }](real); got == nil || got.Name() != "有" {
		t.Errorf("真实值应当原样装进去，得到 %#v", got)
	}
	// 装进去之后应当连着「缺席」这条判据也成立。
	if !Missing(Cast[interface{ Name() string }](typedNil)) {
		t.Error("Cast 之后仍然应当算缺席")
	}
}

func TestMissing(t *testing.T) {
	var typedNil *fakeReader
	var real = &fakeReader{name: "有"}

	cases := []struct {
		name       string
		dependency any
		want       bool
	}{
		{"真 nil", nil, true},
		{"类型化的 nil 指针", typedNil, true},
		{"装进接口的类型化 nil", interface{ Name() string }(typedNil), true},
		{"真实值", real, false},
		{"装进接口的真实值", interface{ Name() string }(real), false},
		{"零值 map", map[string]int(nil), true},
		{"零值切片", []int(nil), true},
		{"非可空类型", 42, false},
		{"空接口里的 nil", any(nil), true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := Missing(item.dependency); got != item.want {
				t.Errorf("Missing(%v) = %v，想要 %v", item.dependency, got, item.want)
			}
		})
	}
}
