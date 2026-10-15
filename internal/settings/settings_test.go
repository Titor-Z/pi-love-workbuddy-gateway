package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenMissingFileUsesDefaults 首次运行时没有文件，用默认值。
func TestOpenMissingFileUsesDefaults(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	got := store.Get()
	if got.LogPageSize != DefaultPageSize || got.LogRetentionDays != DefaultRetentionDays {
		t.Errorf("默认值不对：%+v", got)
	}
}

// TestOpenPartialFileKeepsDefaults 旧版本的文件只有一部分字段，
// 不能因为少了新加的那一项就把整份配置当损坏。
func TestOpenPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"log_page_size":25}`), 0o600); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	got := store.Get()
	if got.LogPageSize != 25 {
		t.Errorf("已存在的字段应当保留，得到 %d", got.LogPageSize)
	}
	if got.LogRetentionDays != DefaultRetentionDays {
		t.Errorf("缺失的字段应当补默认值，得到 %d", got.LogRetentionDays)
	}
}

// TestOpenCorruptFileUsesDefaults 文件坏了不该让网关起不来。
func TestOpenCorruptFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("坏文件不该报错：%v", err)
	}
	if store.Get() != Default() {
		t.Errorf("坏文件应当退回默认值，得到 %+v", store.Get())
	}
}

// TestOpenRejectsEmptyPath 空路径是编程错误，要明确报出来。
func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("空路径应当报错")
	}
}

// TestOpenNormalizesOutOfRange 文件里的越界值收敛到默认值而不是拒绝启动。
func TestOpenNormalizesOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"log_page_size":0,"log_retention_days":9999}`), 0o600); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	if store.Get() != Default() {
		t.Errorf("越界值应当收敛成默认值，得到 %+v", store.Get())
	}
}

// TestUpdatePersistsAndTakesEffect 面板改完要立即生效，并且落盘。
func TestUpdatePersistsAndTakesEffect(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "settings.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}

	next := Settings{LogPageSize: 20, LogRetentionDays: 14}
	if err := store.Update(next); err != nil {
		t.Fatalf("Update 失败：%v", err)
	}
	if store.Get() != next {
		t.Errorf("改完没有立即生效：%+v", store.Get())
	}

	// 父目录要自动建出来，否则面板在首次运行时改不了设置。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("文件没落盘：%v", err)
	}
	var parsed Settings
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("落盘的不是合法 JSON：%v", err)
	}
	if parsed != next {
		t.Errorf("落盘内容不对：%+v", parsed)
	}

	// 重开一份要读到刚写的值。
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	if reopened.Get() != next {
		t.Errorf("重开后读到的不是保存的值：%+v", reopened.Get())
	}

	// 临时文件不能留在目录里。
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("临时文件没有被清理")
	}
}

// TestUpdateRejectsOutOfRange 面板显式提交越界值时要报错，
// 让用户知道填错了，而不是悄悄改成别的数。
func TestUpdateRejectsOutOfRange(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	cases := []Settings{
		{LogPageSize: MinPageSize - 1, LogRetentionDays: DefaultRetentionDays},
		{LogPageSize: MaxPageSize + 1, LogRetentionDays: DefaultRetentionDays},
		{LogPageSize: DefaultPageSize, LogRetentionDays: MinRetentionDays - 1},
		{LogPageSize: DefaultPageSize, LogRetentionDays: MaxRetentionDays + 1},
	}
	for _, value := range cases {
		if err := store.Update(value); err == nil {
			t.Errorf("%+v 应当被拒绝", value)
		}
	}
	// 被拒绝的提交不能改动已经在用的值。
	if store.Get() != Default() {
		t.Errorf("拒绝后当前值被改了：%+v", store.Get())
	}
}
