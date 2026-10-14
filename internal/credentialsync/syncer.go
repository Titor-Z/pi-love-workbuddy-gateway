package credentialsync

import (
	"context"
	"fmt"
	"time"
)

// Syncer 是客户端侧的凭证同步器。
//
// 它只做一件事：定时从网关拉一份凭证，落到本机加密文件里。
// **它不刷新、不回写、不推送**——那三条是地基，违反任何一条都会让
// 多设备共享退化成互相踢。
type Syncer struct {
	// Source 是从网关取凭证的通道（走 relay 客户端）。
	Source Source
	// Store 是本机凭证副本的路径。
	Store string
	// Seal 把明文凭证加密成可落盘的形式。由调用方注入本机 Cipher。
	Seal func(map[string]any) (map[string]any, error)
	// Report 汇报同步事件；为 nil 时静默。
	Report func(format string, args ...any)
	// Now 便于测试注入时间；nil 时用 time.Now。
	Now func() time.Time

	// Interval 是正常同步周期。
	Interval time.Duration

	// 以下是进程内状态，不落盘：重启就是全新开始，
	// 没必要记住「上次失败过」。
	failures    int
	lastAttempt time.Time
	lastPull    time.Time
	lastEpoch   int64
	// Conflict 非 nil 表示存在待用户决定的冲突。
	Conflict *ConflictInfo
}

// ConflictInfo 描述一次「本地比网关新」的冲突。
//
// 记录两边 epoch 而不是只记一个布尔：用户要做决定，
// 得先看到两边各是多少才有依据。
type ConflictInfo struct {
	LocalEpoch  int64     `json:"local_epoch"`
	RemoteEpoch int64     `json:"remote_epoch"`
	DetectedAt  time.Time `json:"detected_at"`
}

// Source 是取凭证的通道。
type Source interface {
	// Fetch 拉一份凭证。
	Fetch(ctx context.Context) (Envelope, error)
}

// State 是同步器的可观测状态，给面板用。
type State struct {
	LastSyncAt    time.Time     `json:"last_sync_at"`
	LastEpoch     int64         `json:"last_epoch"`
	LocalEpoch    int64         `json:"local_epoch"`
	Failures      int           `json:"failures"`
	NextAttemptIn string        `json:"next_attempt_in,omitempty"`
	Conflict      *ConflictInfo `json:"conflict,omitempty"`
}

// Snapshot 取当前状态。
func (s *Syncer) Snapshot() State {
	state := State{
		LastSyncAt:    s.lastPull,
		LastEpoch:     s.lastEpoch,
		Failures:      s.failures,
		NextAttemptIn: s.nextAttemptIn().String(),
	}
	if local, exists, err := Load(s.Store); err == nil && exists {
		state.LocalEpoch = local.Epoch
	}
	state.Conflict = s.Conflict
	return state
}

// nextAttemptIn 给出距离下次尝试还有多久。
func (s *Syncer) nextAttemptIn() time.Duration {
	if s.lastAttempt.IsZero() {
		return 0
	}
	elapsed := s.now().Sub(s.lastAttempt)
	wait := s.backoff()
	if elapsed >= wait {
		return 0
	}
	return wait - elapsed
}

// Backoff 给出下次同步前应等的时长。导出供主循环使用。
func (s *Syncer) Backoff() time.Duration { return s.backoff() }

// backoff 给出当前应等的时长。失败越多等越久，封顶正常周期。
func (s *Syncer) backoff() time.Duration {
	if s.failures == 0 {
		return s.Interval
	}
	minutes := BackoffMinutes(s.failures - 1)
	return time.Duration(minutes) * time.Minute
}

// ResolveConflict 由用户在面板上做出选择后调用。
//
// useRemote 为真表示「用网关的」——接受覆盖，冲突解除；
// 为假表示「保留本地的」——本次不同步，冲突保留并继续提示。
func (s *Syncer) ResolveConflict(useRemote bool) {
	if useRemote {
		s.Conflict = nil
		s.failures = 0
		return
	}
	// 保留冲突，让面板继续提示，直到用户处理。
}

// Sync 拉一次并落盘。这是唯一的同步入口，
// 定时循环与「立即同步」按钮都走它。
func (s *Syncer) Sync(ctx context.Context) error {
	s.lastAttempt = s.now()
	envelope, err := s.Source.Fetch(ctx)
	if err != nil {
		s.failures++
		return err
	}
	if err := envelope.Validate(); err != nil {
		s.failures++
		return fmt.Errorf("网关下发的凭证不可用：%w", err)
	}

	localEpoch := int64(-1)
	if local, exists, loadErr := Load(s.Store); loadErr != nil {
		s.failures++
		return loadErr
	} else if exists {
		localEpoch = local.Epoch
	}

	switch Decide(localEpoch, envelope.Epoch) {
	case Accept, AcceptIdempotent:
		// 正常路径：落盘。
	case Conflict:
		// 本地更新。不覆盖、不报错，把决定权交出去。
		if s.Conflict == nil || s.Conflict.LocalEpoch != localEpoch {
			s.Conflict = &ConflictInfo{
				LocalEpoch:  localEpoch,
				RemoteEpoch: envelope.Epoch,
				DetectedAt:  s.now(),
			}
		}
		s.failures = 0
		return fmt.Errorf("本机凭证比网关新（本机 epoch %d，网关 %d）："+
			"这台机器此前切到网关模式自己刷新过。请在面板上选择用哪一份",
			localEpoch, envelope.Epoch)
	default:
		s.failures++
		return fmt.Errorf("丢弃 epoch %d 的副本（本机为 %d）",
			envelope.Epoch, localEpoch)
	}

	if s.Seal == nil {
		s.failures++
		return fmt.Errorf("未配置加密器，拒绝把明文凭证写进磁盘")
	}
	sealed, err := s.Seal(envelope.Credential)
	if err != nil {
		s.failures++
		return fmt.Errorf("加密凭证失败：%w", err)
	}

	file := LocalFile{
		Epoch:      envelope.Epoch,
		SyncedAt:   s.now(),
		Active:     false, // 客户端模式下的副本默认非活跃
		Credential: sealed,
	}
	if err := Save(s.Store, file); err != nil {
		s.failures++
		return err
	}

	s.failures = 0
	s.lastPull = s.now()
	s.lastEpoch = envelope.Epoch
	s.reportf("凭证已同步，epoch %d", envelope.Epoch)
	return nil
}

// ShouldPullNow 判断是否到了该拉一次的时候。
//
// 计时基准是**上次尝试**而不是上次成功：一次失败也要开始退避，
// 否则网关离线时会变成拼命重试。成功一次失败计数清零，
// 于是失败后的第一次成功会立刻回到正常周期，不会卡在退避上。
func (s *Syncer) ShouldPullNow() bool {
	if s.lastAttempt.IsZero() {
		return true // 从未试过
	}
	return s.now().Sub(s.lastAttempt) >= s.backoff()
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) reportf(format string, args ...any) {
	if s.Report != nil {
		s.Report(format, args...)
	}
}
