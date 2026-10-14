package devices

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func openRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := Open(t.TempDir() + "/devices.json")
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	return registry
}

// TestObserveRegistersOnceThenUpdates 首次到访登记一次，
// 之后只更新最后在线时间——不该把 FirstSeen 改掉，也不该重复登记。
func TestObserveRegistersOnceThenUpdates(t *testing.T) {
	registry := openRegistry(t)
	registry.Observe("a", "笔记本")
	registry.Observe("a", "笔记本")
	registry.Observe("b", "台式机")

	entries := registry.List()
	if len(entries) != 2 {
		t.Fatalf("登记了 %d 台设备，期望 2 台", len(entries))
	}
	for _, entry := range entries {
		if entry.ID != "a" && entry.ID != "b" {
			t.Errorf("出现了未知设备 %q", entry.ID)
		}
		if entry.Revoked {
			t.Errorf("%q 不该默认就是吊销状态", entry.ID)
		}
	}
}

// TestObserveKeepsNameWhenClientOmitsIt 客户端没报名字时留住原来的名字，
// 而不是把列表里的名字抹成空白。
func TestObserveKeepsNameWhenClientOmitsIt(t *testing.T) {
	registry := openRegistry(t)
	registry.Observe("a", "笔记本")
	registry.Observe("a", "")
	if got := registry.NameOf("a"); got != "笔记本" {
		t.Errorf("NameOf = %q，期望保留「笔记本」", got)
	}
}

// TestObserveIgnoresEmptyID 没有设备 ID 的请求（本机直连）不该登记成一台设备。
func TestObserveIgnoresEmptyID(t *testing.T) {
	registry := openRegistry(t)
	registry.Observe("", "某人")
	if entries := registry.List(); len(entries) != 0 {
		t.Errorf("空 ID 也被登记了：%+v", entries)
	}
}

// TestRevokedBlocksOnlyRevokedDevice 吊销只影响那一个 ID；
// 未知 ID 不算吊销（它只是还没见过）。
func TestRevokedBlocksOnlyRevokedDevice(t *testing.T) {
	registry := openRegistry(t)
	registry.Observe("a", "笔记本")
	registry.Observe("b", "台式机")

	if !registry.Revoke("a") {
		t.Fatal("吊销已知设备应当返回 true")
	}
	if !registry.IsRevoked("a") {
		t.Error("a 应当被吊销")
	}
	if registry.IsRevoked("b") {
		t.Error("b 不该被牵连")
	}
	if registry.IsRevoked("从未见过") {
		t.Error("未知设备不该被当作吊销：那会把新设备全挡在外面")
	}
}

// TestRevokeUnknownReturnsFalse 吊销一个不存在的 ID 要返回 false，
// 好让接口层能告诉用户「没有这台设备」，而不是假装成功。
func TestRevokeUnknownReturnsFalse(t *testing.T) {
	registry := openRegistry(t)
	if registry.Revoke("不存在") {
		t.Error("吊销未知设备应当返回 false")
	}
	if registry.Unrevoke("不存在") {
		t.Error("撤销未知设备的吊销应当返回 false")
	}
}

// TestUnrevokeRestoresAccess 误操作要能回头，否则用户只能去删文件。
func TestUnrevokeRestoresAccess(t *testing.T) {
	registry := openRegistry(t)
	registry.Observe("a", "笔记本")
	registry.Revoke("a")
	if !registry.Unrevoke("a") {
		t.Fatal("撤销吊销应当返回 true")
	}
	if registry.IsRevoked("a") {
		t.Error("a 应当恢复")
	}
}

// TestPersistsAcrossReopen 登记表必须跨重启存在，
// 否则每次重启所有客户端都得重新接受一遍管理。
func TestPersistsAcrossReopen(t *testing.T) {
	path := t.TempDir() + "/devices.json"
	first, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	first.Observe("a", "笔记本")
	first.Revoke("a")

	second, err := Open(path)
	if err != nil {
		t.Fatalf("再次打开失败：%v", err)
	}
	if !second.IsRevoked("a") {
		t.Error("重启后吊销状态丢了")
	}
	if second.NameOf("a") != "笔记本" {
		t.Errorf("重启后名字 = %q", second.NameOf("a"))
	}
}

// TestOpenRejectsEmptyPath 路径为空要报错，否则会往当前目录写文件。
func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("空路径应当报错")
	}
}

// TestOpenToleratesMissingAndCorruptFile 文件不存在、或坏掉，
// 都不该让网关起不来——注册表不是凭据，没有凭据之外的用途。
func TestOpenToleratesMissingAndCorruptFile(t *testing.T) {
	if _, err := Open(t.TempDir() + "/missing.json"); err != nil {
		t.Errorf("文件不存在不该报错：%v", err)
	}

	corrupt := t.TempDir() + "/devices.json"
	if err := os.WriteFile(corrupt, []byte("这不是 JSON"), 0o600); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	registry, err := Open(corrupt)
	if err != nil {
		t.Fatalf("坏文件不该让注册失败：%v", err)
	}
	if len(registry.List()) != 0 {
		t.Error("坏文件应当当成空表")
	}
}

// TestListIsSortedByLastSeen 顺序必须确定：调用方不该各排各的，
// 而 map 的遍历顺序从来不是排序。
func TestListIsSortedByLastSeen(t *testing.T) {
	registry := openRegistry(t)
	registry.Observe("old", "老机器")
	// 时间往前拨，制造出真实的先后差别。
	registry.mu.Lock()
	if entry := registry.entries["old"]; entry != nil {
		entry.LastSeen = time.Now().Add(-time.Hour)
	}
	registry.mu.Unlock()
	registry.Observe("new", "新机器")

	entries := registry.List()
	if len(entries) != 2 {
		t.Fatalf("列表长度 = %d，期望 2", len(entries))
	}
	if entries[0].ID != "new" {
		t.Errorf("第一台 = %q，期望最近活跃的 new", entries[0].ID)
	}
}

// TestSavedFileIsHumanReadable 这个文件会被人直接打开看；
// 主机名里的 < > & 不能被转义成 \u003c，否则认不出是哪台机器。
func TestSavedFileIsHumanReadable(t *testing.T) {
	path := t.TempDir() + "/devices.json"
	registry, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	registry.Observe("a", "Mac<Pro>&Air")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if strings.Contains(string(raw), "\\u003c") {
		t.Errorf("HTML 转义没有被关掉：%s", raw)
	}
	if !strings.Contains(string(raw), "\n  {") {
		t.Errorf("没有缩进，不便阅读：%s", raw)
	}
	var decoded []Entry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Errorf("写出的文件不是合法 JSON：%v", err)
	}
}

// TestConcurrentObserve 多设备并发到访不该把内存写坏；
// 这条会因 -race 失败，用来兜住锁的粒度。
func TestConcurrentObserve(t *testing.T) {
	registry := openRegistry(t)
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func(index int) {
			defer func() { done <- struct{}{} }()
			registry.Observe("d"+string(rune('a'+index)), "机器")
			registry.IsRevoked("d" + string(rune('a'+index)))
			registry.List()
		}(i)
	}
	for i := 0; i < 16; i++ {
		<-done
	}
	if got := len(registry.List()); got != 16 {
		t.Errorf("登记了 %d 台，期望 16", got)
	}
}
