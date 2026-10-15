// Package optional 处理「可选的接口依赖到底在不在」。
//
// 存在的原因是一个 Go 的经典陷阱，本项目已经踩过两次：
//
//	var real *T = nil
//	var dep I = real   // 接口本身**非空**，里面装着一个类型已知、值为 nil 的指针
//	if dep == nil { }  // false —— 判不出来
//	dep.Method()       // 解引用 nil，panic
//
// 第一次是 relay 的 CredentialSource（panic 会让整条 TLS 连接断掉），
// 第二次是面板的 OverridesSyncer（网关模式下它本来就是 nil，点开模型页
// 就让 HTTP 处理协程 panic）。两次都是「这个依赖是可选的」这个前提在
// 赋值那一刻被抹掉了。
//
// 所以把判据收在这里一处：**凡是「可能不传」的接口字段，一律用 Missing 判，
// 不要直接判等。**
package optional

import "reflect"

// Cast 把可选的实现装进接口字段：缺席时得到接口的 nil，而不是「类型化的 nil」。
//
// 赋值那一刻就要决定，不要留到调用时再判——`dep.Method()` 的调用点往往
// 在另一个包、另一条路径上，那时再想起来判已经晚了。
func Cast[I any](dependency any) I {
	if Missing(dependency) {
		var zero I
		return zero
	}
	return dependency.(I)
}

// Missing 判断一个可选依赖是不是真的缺席。
//
// 直接判等之外还认得「类型化的 nil」：若值是 nil 指针/接口/map/切片/函数/通道，
// 也算缺席。非可空的类型（结构体、数字等）永远算存在。
func Missing(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	default:
		return false
	}
}
