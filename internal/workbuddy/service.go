package workbuddy

import (
	"context"
	"strconv"
	"time"
)

// Service 把「查状态 / 签到 / 记一笔」串成一个动作。
//
// 它仍然**不含调度**：调用方决定什么时候调 RunOnce。网关自己的定时器是
// 一个调用方，控制台按钮与外部看板是另外两个，它们互不感知。
type Service struct {
	client *Client
	store  *Store
}

// NewService 组装服务。
func NewService(client *Client, store *Store) *Service {
	return &Service{client: client, store: store}
}

// Store 返回底层的记录器，供面板读历史。
func (s *Service) Store() *Store { return s.store }

// Status 查一次签到状态（只读）。
func (s *Service) Status(ctx context.Context) (Status, error) {
	return s.client.Status(ctx)
}

// RunOnce 执行一次签到并记录结果。
//
// 会先看本地记录里今天是否已经成功签过——成功过就不再打后端，
// 这样定时器与手动点击重合时不会重复请求。**但本地记录只是省流量的缓存**：
// 它没有记录（例如首次运行、或换了机器）时会老老实实打一次后端，
// 由后端返回的 10001 告诉我们是「已经签过」。
func (s *Service) RunOnce(ctx context.Context) Result {
	if today, ok := s.store.Today(); ok && today.Done {
		return Result{
			Done:       true,
			Already:    true,
			Credit:     today.Credit,
			StreakDays: today.StreakDays,
			Message:    "今天已经签过了（本地记录）",
		}
	}

	result, err := s.client.Do(ctx)
	now := time.Now()
	if err != nil {
		s.store.Put(Record{
			Date:    DayKey(now),
			At:      now,
			Message: err.Error(),
			Error:   err.Error(),
		})
		result.Message = err.Error()
		return result
	}

	// 后端在「今天已签过」（code 10001）的响应里不回连续天数与积分，
	// 直接用会把「连续几天」显示成 0。既然这时候没花任何代价（本来就不会再签），
	// 就顺手查一次状态把真实数字补上——比留一个看着像「数据丢了」的 0 诚实。
	if result.Already {
		if status, err := s.client.Status(ctx); err == nil {
			result.StreakDays = status.StreakDays
			if result.Credit == 0 {
				result.Credit = status.DailyCredit
			}
		}
	}

	s.store.Put(Record{
		Date:       DayKey(now),
		Done:       result.Done,
		Credit:     result.Credit,
		StreakDays: result.StreakDays,
		At:         now,
		Message:    result.Message,
	})
	return result
}

// CheckinStatus 是 CheckinSource 要求的形状，转发给 Status。
// 名字不同是为了让面板的接口读起来就是「签到状态」，
// 而不是一个笼统的 Status（这个包以后还会有别的状态）。
func (s *Service) CheckinStatus(ctx context.Context) (Status, error) {
	return s.Status(ctx)
}

// CheckinNow 是 CheckinSource 要求的形状，转发给 RunOnce。
func (s *Service) CheckinNow(ctx context.Context) (Result, error) {
	return s.RunOnce(ctx), nil
}

// CheckinRecent 是 CheckinSource 要求的形状，转发给记录的 Recent。
func (s *Service) CheckinRecent(limit int) []Record { return s.store.Recent(limit) }

// NextRun 返回在 now 之后、按每天 clock（"HH:MM"）触发的下一个时刻。
//
// 抽成纯函数是为了能测：「过了今天的时间点就推到明天」这类判断
// 用真时钟测会很不稳。clock 非法时退回第二天同一时刻，
// 而不是返回零值把定时器弄停。
func NextRun(now time.Time, clock string) time.Time {
	hour, minute := parseClock(clock)
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// parseClock 解析 "HH:MM"；非法时给默认时刻。
// 调用前 settings.Validate 已经挡过非法值，这里是兜底。
func parseClock(clock string) (int, int) {
	if len(clock) != 5 || clock[2] != ':' {
		return 9, 0
	}
	hour, err := strconv.Atoi(clock[:2])
	if err != nil || hour < 0 || hour > 23 {
		return 9, 0
	}
	minute, err := strconv.Atoi(clock[3:])
	if err != nil || minute < 0 || minute > 59 {
		return 9, 0
	}
	return hour, minute
}
