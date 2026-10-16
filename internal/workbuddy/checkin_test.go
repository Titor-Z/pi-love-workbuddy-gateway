package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeTokens 是最简单的 TokenSource：固定给一枚令牌。
// 签到包里不需要真的凭证管理器——客户端只要求「能给出请求头」。
type fakeTokens struct{}

func (fakeTokens) Headers() (map[string]string, error) {
	return map[string]string{"authorization": "Bearer test-token"}, nil
}

// newTestClient 造一个指向假后端的客户端，并把请求记下来供断言。
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *[]string) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return NewClient(fakeTokens{}, server.URL), &paths
}

// 状态响应照抄真实接口的形状。**必须带 /v2 前缀**——
// 走错路径时后端会回一个「active:false、streak_days:0」的空壳，
// 那正是最容易让人以为「没有签到活动」的坑。
func TestStatusParsesActivityStatus(t *testing.T) {
	client, paths := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{
			"active":true,"today_checked_in":true,"streak_days":4,
			"daily_credit":100,"total_credits":400,
			"checkin_dates":["2026-10-17","2026-10-16"],
			"week_progress":[false,false,true,true,true,true,false],
			"theme_name":"Buddy加油站","activity_name":"Buddy应用",
			"start_time":"2026-09-30 00:00:00","end_time":"2026-10-31 23:59:59"}}`))
	})

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("查状态失败：%v", err)
	}
	if got := (*paths)[0]; got != "/v2/billing/meter/checkin-activity-status" {
		t.Fatalf("查状态走了错误的路径：%s（漏了 /v2 就会拿到空壳响应）", got)
	}
	if !status.Active || !status.CheckedInToday {
		t.Fatalf("状态解析不对：%+v", status)
	}
	if status.StreakDays != 4 || status.DailyCredit != 100 || status.TotalCredit != 400 {
		t.Fatalf("数字解析不对：%+v", status)
	}
	if len(status.CheckinDates) != 2 || len(status.WeekProgress) != 7 {
		t.Fatalf("数组解析不对：%+v", status)
	}
}

// 签到成功：拿到积分与连续天数。
func TestCheckinSuccess(t *testing.T) {
	client, paths := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"credit":100,"streak_days":4}}`))
	})

	result, err := client.Do(context.Background())
	if err != nil {
		t.Fatalf("签到失败：%v", err)
	}
	if got := (*paths)[0]; got != "/v2/billing/meter/daily-checkin" {
		t.Fatalf("签到走了错误的路径：%s", got)
	}
	if !result.Done || result.Already || result.Credit != 100 || result.StreakDays != 4 {
		t.Fatalf("结果不对：%+v", result)
	}
}

// 「今天已签到」是成功，不是失败。
//
// 后端对重复签到返回 HTTP 400 + code 10001。把 400 当失败是最容易写错的一处，
// 而且症状很隐蔽：用户看到「签到失败」，其实今天已经签好了。
func TestCheckinAlreadyCheckedInIsSuccess(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":10001,"msg":"今日已签到"}`))
	})

	result, err := client.Do(context.Background())
	if err != nil {
		t.Fatalf("「今天已签到」不该报错：%v", err)
	}
	if !result.Done || !result.Already {
		t.Fatalf("应当归一到 Done+Already：%+v", result)
	}
	if result.Code != 10001 {
		t.Fatalf("应当保留原始码：%+v", result)
	}
}

// 真正的失败要返回错误，且带后端原话。
func TestCheckinRejectionReturnsError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":403,"msg":"活动已结束"}`))
	})

	result, err := client.Do(context.Background())
	if err == nil {
		t.Fatalf("非 0 且非 10001 的码应当报错，得到：%+v", result)
	}
	if result.Done {
		t.Fatalf("失败时 Done 不该为真：%+v", result)
	}
}

// 请求必须带上令牌头。少了它后端会 401，而症状是「签到失败」，
// 看起来像账号问题。
func TestRequestCarriesTokenHeader(t *testing.T) {
	var auth string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("authorization")
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	})
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("查状态失败：%v", err)
	}
	if auth != "Bearer test-token" {
		t.Fatalf("没有带上令牌头，拿到：%q", auth)
	}
}

// 一次 RunOnce：成功后要落一条记录，且同一天再调不再打后端。
func TestRunOnceRecordsAndSkipsRepeat(t *testing.T) {
	calls := 0
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":0,"data":{"credit":100,"streak_days":4}}`))
	})
	store, err := OpenStore(filepath.Join(t.TempDir(), "checkin.json"))
	if err != nil {
		t.Fatalf("打开记录失败：%v", err)
	}
	service := NewService(client, store)

	first := service.RunOnce(context.Background())
	if !first.Done {
		t.Fatalf("第一次应当签到成功：%+v", first)
	}
	second := service.RunOnce(context.Background())
	if !second.Done || !second.Already {
		t.Fatalf("第二次应当认出「今天已签」：%+v", second)
	}
	if calls != 1 {
		t.Fatalf("今天已签后不该再打后端，实际打了 %d 次", calls)
	}
	if today, ok := store.Today(); !ok || !today.Done || today.StreakDays != 4 {
		t.Fatalf("记录没落对：%+v（ok=%v）", today, ok)
	}
}

// 失败也要落记录，但 Done 为假——否则一次网络抖动会被记成「今天已完成」，
// 把后面的重试挡住。
func TestRunOnceRecordsFailureWithoutBlocking(t *testing.T) {
	calls := 0
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"msg":"服务异常"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"credit":100,"streak_days":4}}`))
	})
	store, err := OpenStore(filepath.Join(t.TempDir(), "checkin.json"))
	if err != nil {
		t.Fatalf("打开记录失败：%v", err)
	}
	service := NewService(client, store)

	failed := service.RunOnce(context.Background())
	if failed.Done {
		t.Fatalf("失败不该算签到成功：%+v", failed)
	}
	if today, ok := store.Today(); !ok || today.Done {
		t.Fatalf("失败记录应当落盘且 Done=false：%+v（ok=%v）", today, ok)
	}
	// 关键：失败没有把重试挡住。
	retried := service.RunOnce(context.Background())
	if !retried.Done {
		t.Fatalf("失败之后必须还能重试成功：%+v", retried)
	}
}

// 「已经签过」时后端不回连续天数与积分，要顺手查状态补齐，
// 而不是把「连续几天」显示成 0。
func TestRunOnceFillsMissingNumbersWhenAlready(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/billing/meter/daily-checkin" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":10001,"msg":"今日已签到"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"streak_days":7,"daily_credit":100}}`))
	})
	store, err := OpenStore(filepath.Join(t.TempDir(), "checkin.json"))
	if err != nil {
		t.Fatalf("打开记录失败：%v", err)
	}

	result := NewService(client, store).RunOnce(context.Background())
	if !result.Done || !result.Already {
		t.Fatalf("应当认出已签：%+v", result)
	}
	if result.StreakDays != 7 {
		t.Fatalf("连续天数应当从状态接口补回来，得到 %d", result.StreakDays)
	}
}

// NextRun 的边界：过了今天的点就推到明天；没过就用今天。
func TestNextRun(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 17, 8, 30, 0, 0, location)

	if got := NextRun(now, "09:00"); !got.Equal(time.Date(2026, 10, 17, 9, 0, 0, 0, location)) {
		t.Fatalf("未到点应当用今天：%v", got)
	}
	if got := NextRun(now, "08:00"); !got.Equal(time.Date(2026, 10, 18, 8, 0, 0, 0, location)) {
		t.Fatalf("已过点应当推到明天：%v", got)
	}
	// 正好等于当前时刻也算「已过」，否则会立刻触发一次。
	if got := NextRun(now, "08:30"); !got.Equal(time.Date(2026, 10, 18, 8, 30, 0, 0, location)) {
		t.Fatalf("恰好等于当前时刻应当推到明天：%v", got)
	}
	// 非法输入退回默认时刻，而不是返回零值把定时器弄停。
	if got := NextRun(now, "乱写"); got.Hour() != 9 || got.Minute() != 0 {
		t.Fatalf("非法时刻应当退回 09:00：%v", got)
	}
}

// 记录文件损坏时不该让工具起不来，从空记录继续。
func TestOpenStoreToleratesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkin.json")
	if err := os.WriteFile(path, []byte("{ 这不是 JSON"), 0o600); err != nil {
		t.Fatalf("准备坏文件失败：%v", err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("坏文件不该让打开失败：%v", err)
	}
	if len(store.Recent(10)) != 0 {
		t.Fatalf("坏文件应当从空记录开始")
	}
}

// 记录条数要有上限，否则跑几年会一直长。
func TestStoreCapsRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkin.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("打开记录失败：%v", err)
	}
	for i := 0; i < maxRecords+50; i++ {
		store.Put(Record{Date: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")})
	}
	if got := len(store.Recent(0)); got != maxRecords {
		t.Fatalf("记录应当封顶在 %d 条，实际 %d", maxRecords, got)
	}
	// 落盘的那份也要是同一条数。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回记录失败：%v", err)
	}
	var parsed []Record
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("落盘内容不是合法 JSON：%v", err)
	}
	if len(parsed) != maxRecords {
		t.Fatalf("落盘的记录数不对：%d", len(parsed))
	}
}
