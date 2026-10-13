package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
)

// 测试用的固定密钥。不是真实密钥，只用来造一份能解开的登录态文件。
const testSecret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// newTestClient 造一个指向假后端的账单客户端。
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(newTestCredential(t), server.URL)
}

// newTestCredential 造一份指向临时目录的登录态，返回可用的凭据管理器。
func newTestCredential(t *testing.T) *credential.Manager {
	t.Helper()
	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	sealed, err := cipher.Encrypt("test-access-token")
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken": sealed,
			"domain":      "www.codebuddy.cn",
			"expiresAt":   float64(time.Now().Add(24 * time.Hour).UnixMilli()),
		},
		"account": map[string]any{"uid": "test-uid", "enterpriseId": "test-enterprise"},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	path := filepath.Join(t.TempDir(), "workbuddy-desktop.info")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入登录态失败：%v", err)
	}
	return credential.NewManager(path, cipher, nil, "")
}

// 下面这份响应体照抄真实接口的形状，含那个最容易踩的坑：
// 整数 CapacityRemain 是包的总量、纹丝不动，真正会动的是精确字段。
const realShapeResponse = `{
  "code": 0,
  "msg": "OK",
  "data": {
    "Response": {
      "Data": {
        "TotalCount": 2,
        "TotalDosage": 2400,
        "Accounts": [
          {
            "PackageName": "CodeBuddy个人体验版",
            "CapacityRemain": 500,
            "CapacityUsed": 0,
            "CycleCapacityRemainPrecise": "497.46000001",
            "CycleCapacityUsedPrecise": "2.53999999",
            "CycleCapacitySizePrecise": "500",
            "CapacityRemainPrecise": "500",
            "CycleEndTime": "2026-10-31 23:59:59",
            "DeductionEndTime": 2052324559000
          },
          {
            "PackageName": "CodeBuddy个人版拉新权益包",
            "CapacityRemain": 1500,
            "CapacityUsed": 0,
            "CycleCapacityRemainPrecise": "1500",
            "CycleCapacityUsedPrecise": "0",
            "CycleEndTime": "2026-11-14 02:09:20",
            "DeductionEndTime": 1794593360000
          }
        ]
      }
    }
  }
}`

// TestFetchParsesRealShape 用真实响应形状做回归：
// 必须优先取精确字段，而不是那个永远不变的整数。
func TestFetchParsesRealShape(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法 = %s，期望 POST", r.Method)
		}
		if r.URL.Path != "/v2/billing/meter/get-user-resource" {
			t.Errorf("路径 = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-User-Id"); got != "test-uid" {
			t.Errorf("X-User-Id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(realShapeResponse))
	}))

	snapshot, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if len(snapshot.Packages) != 2 {
		t.Fatalf("包数 = %d，期望 2", len(snapshot.Packages))
	}

	first := snapshot.Packages[0]
	if first.Name != "CodeBuddy个人体验版" {
		t.Errorf("包名 = %q", first.Name)
	}
	// 关键断言：读的是精确字段，不是那个恒为 500 的整数。
	if first.Remain != 497.46000001 {
		t.Errorf("剩余 = %v，期望 497.46000001（不能取整数字段）", first.Remain)
	}
	if first.Use != 2.53999999 {
		t.Errorf("已用 = %v，期望 2.53999999", first.Use)
	}
	if first.Size != 500 {
		t.Errorf("总量 = %v，期望 500", first.Size)
	}
	if first.TotalRemain != 500 {
		t.Errorf("包剩余总量 = %v，期望 500", first.TotalRemain)
	}

	wantCycleEnd := time.Date(2026, 10, 31, 23, 59, 59, 0, time.Local)
	if !first.CycleEndTime.Equal(wantCycleEnd) {
		t.Errorf("周期结束 = %v，期望 %v", first.CycleEndTime, wantCycleEnd)
	}
	if first.ExpireTime.IsZero() {
		t.Error("失效时间没有被解析出来")
	}
}

// TestSnapshotTotalRemain 合计所有资源包的剩余量。
func TestSnapshotTotalRemain(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realShapeResponse))
	}))
	snapshot, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if got, want := snapshot.TotalRemain(), 497.46000001+1500.0; got != want {
		t.Errorf("合计剩余 = %v，期望 %v", got, want)
	}
	if snapshot.FetchedAt.IsZero() {
		t.Error("应记录读取时刻，面板要用来提示数据可能滞后")
	}
}

// TestFetchFallsBackToIntegerFields 精确字段缺失时退回整数口径，
// 而不是丢掉这一项、显示成零。
func TestFetchFallsBackToIntegerFields(t *testing.T) {
	response := `{"code":0,"data":{"Response":{"Data":{"Accounts":[{
		"PackageName":"旧格式包","CapacityRemain":300,"CapacityUsed":200
	}]}}}}`
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(response))
	}))

	snapshot, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	item := snapshot.Packages[0]
	if item.Remain != 300 || item.Use != 200 {
		t.Errorf("整数兜底不对：%+v", item)
	}
	if !item.CycleEndTime.IsZero() {
		t.Errorf("没有周期时间时应为零值：%v", item.CycleEndTime)
	}
}

// TestFetchRejectsEmptyAccounts 拿不到资源包时要报错。
// 返回一个「余额 0」的假象会让用户以为额度被清零了，比报错更糟。
func TestFetchRejectsEmptyAccounts(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[]}}}}`))
	}))
	if _, err := client.Fetch(context.Background()); err == nil {
		t.Fatal("空的资源包列表应当报错")
	}
}

// TestFetchSurfacesUpstreamErrors 上游错误码要带上文案报出来。
func TestFetchSurfacesUpstreamErrors(t *testing.T) {
	cases := map[string]string{
		"业务错误码":  `{"code":10001,"msg":"uid or enterpriseID is empty"}`,
		"坏 JSON": `{not json`,
	}
	for name, body := range cases {
		client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := client.Fetch(context.Background())
		if err == nil {
			t.Errorf("%s：应当报错", name)
			continue
		}
		if name == "业务错误码" && !strings.Contains(err.Error(), "enterpriseID") {
			t.Errorf("%s：应保留上游文案，实际：%v", name, err)
		}
	}
}

// TestFetchRejectsHTTPError 非 200 要报出来，不能把错误页当成数据解析。
func TestFetchRejectsHTTPError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html>503</html>"))
	}))
	if _, err := client.Fetch(context.Background()); err == nil {
		t.Fatal("HTTP 503 应当报错")
	}
}

// TestNewClientDefaultsToOfficialHost 不传地址时指向官方账单域名。
func TestNewClientDefaultsToOfficialHost(t *testing.T) {
	client := NewClient(newTestCredential(t), "")
	if client.baseURL != "https://www.codebuddy.cn" {
		t.Errorf("baseURL = %q", client.baseURL)
	}
}
