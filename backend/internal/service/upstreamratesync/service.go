package upstreamratesync

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

// runner 周期与 leader lock 约束（仿 upstream_billing_probe 的后台任务范式）。
const (
	// rateSyncCycleInterval 每轮检查到期连接的间隔。
	rateSyncCycleInterval = time.Minute
	// RateSyncLeaderLockKey Redis/PG advisory 锁键，多实例单活。
	RateSyncLeaderLockKey = "upstream:rate_sync:leader"
	// rateSyncLeaderLockTTL 崩溃安全上限：必须大于单轮最坏耗时，
	// 锁随轮次结束立即释放，每轮重新竞选。
	rateSyncLeaderLockTTL = 2 * time.Minute
	// rateSyncLockOpTimeout 单次锁操作（获取/释放）的超时。
	rateSyncLockOpTimeout = 2 * time.Second
)

// LeaderLockCache 跨实例互斥端口（与 service.LeaderLockCache 同构，
// 本包不反向依赖 service 包，由 wire 层做结构化适配）。
type LeaderLockCache interface {
	TryAcquireLeaderLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)
	ReleaseLeaderLock(ctx context.Context, key, owner string) error
}

// AdvisoryLockFunc PG advisory lock 回退端口：Redis 不可用（调用期错误）时
// 由 service 包注入 tryAcquireDBAdvisoryLockWithError 的等价实现。
type AdvisoryLockFunc func(ctx context.Context, key string) (release func(), acquired bool, err error)

// UpstreamRateSyncService 上游倍率同步的后台 runner：每分钟一轮，
// 对 enabled 且到期（last_sync_at + interval_minutes <= now，从未同步立即到期）
// 的连接逐条调用 Syncer.SyncConnection；单连接失败不影响其他连接。
// 生命周期 Start/Stop/runLoop 仿 UpstreamBillingProbeService；构造函数不启动 goroutine。
type UpstreamRateSyncService struct {
	syncer   *Syncer
	connRepo ConnectionRepository
	runRepo  SyncRunRepository

	parentCtx    context.Context
	parentCancel context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	started      bool
	stopped      bool
	cycleMu      sync.Mutex

	lockCache    LeaderLockCache
	advisoryLock AdvisoryLockFunc
	instanceID   string
	now          func() time.Time
}

// NewUpstreamRateSyncService 创建 runner（不启动；Start 后才跑循环）。
func NewUpstreamRateSyncService(syncer *Syncer, connRepo ConnectionRepository, runRepo SyncRunRepository) *UpstreamRateSyncService {
	ctx, cancel := context.WithCancel(context.Background())
	return &UpstreamRateSyncService{
		syncer:       syncer,
		connRepo:     connRepo,
		runRepo:      runRepo,
		parentCtx:    ctx,
		parentCancel: cancel,
		instanceID:   uuid.NewString(),
		now:          time.Now,
	}
}

// SetLeaderLock 可选注入 leader lock 后端：Redis 锁优先、PG advisory lock 回退。
// 两者都缺失时 runner 不退化阻塞，按单实例无门控运行（与 probe 语义一致）。
func (s *UpstreamRateSyncService) SetLeaderLock(lockCache LeaderLockCache, advisoryLock AdvisoryLockFunc) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.advisoryLock = advisoryLock
}

// SetNow 注入时钟（测试用）。
func (s *UpstreamRateSyncService) SetNow(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *UpstreamRateSyncService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runLoop()
}

func (s *UpstreamRateSyncService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.parentCancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *UpstreamRateSyncService) runLoop() {
	defer s.wg.Done()
	_ = s.RunDue(s.parentCtx)
	ticker := time.NewTicker(rateSyncCycleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.parentCtx.Done():
			return
		case <-ticker.C:
			if err := s.RunDue(s.parentCtx); err != nil {
				logger.LegacyPrintf(syncLogPrefix, "run_due_failed: err=%v", err)
			}
		}
	}
}

// RunDue 执行一轮到期连接同步 + run 定期清理。
// leader lock 未抢到（他实例持有）时本轮静默跳过；锁后端故障记错误但不 panic。
func (s *UpstreamRateSyncService) RunDue(ctx context.Context) error {
	if s == nil || s.syncer == nil || s.connRepo == nil {
		return nil
	}
	s.cycleMu.Lock()
	defer s.cycleMu.Unlock()

	release, acquired, err := s.tryAcquireLeaderLock(ctx, RateSyncLeaderLockKey)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer release()

	connections, err := s.connRepo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, conn := range connections {
		if conn == nil || !conn.Enabled || !connectionDue(conn, now) {
			continue
		}
		if _, syncErr := s.syncer.SyncConnection(ctx, conn.ID); syncErr != nil {
			// 单连接失败不影响其他连接（错误已落入 run/last_error）。
			logger.LegacyPrintf(syncLogPrefix, "sync_connection_failed: connection_id=%d err=%v", conn.ID, syncErr)
		}
	}

	if s.runRepo != nil {
		if _, pruneErr := s.runRepo.Prune(ctx, RunRetentionDays, KeepRunsPerConnection); pruneErr != nil {
			logger.LegacyPrintf(syncLogPrefix, "prune_runs_failed: err=%v", pruneErr)
		}
	}
	return nil
}

// connectionDue 到期判定：从未同步（last_sync_at 为空）立即到期；
// 否则 last_sync_at + interval_minutes <= now。interval 非法时按默认值处理。
func connectionDue(conn *Connection, now time.Time) bool {
	if conn.LastSyncAt == nil {
		return true
	}
	interval := conn.IntervalMinutes
	if interval < MinIntervalMinutes || interval > MaxIntervalMinutes {
		interval = DefaultIntervalMinutes
	}
	return !conn.LastSyncAt.Add(time.Duration(interval) * time.Minute).After(now)
}

// tryAcquireLeaderLock Redis 锁优先；Redis 调用期错误时回退 PG advisory lock；
// 两者皆无（未注入）时无门控运行，保证 runner 永不因锁后端缺失而饿死。
func (s *UpstreamRateSyncService) tryAcquireLeaderLock(ctx context.Context, key string) (func(), bool, error) {
	lockCtx, cancel := context.WithTimeout(ctx, rateSyncLockOpTimeout)
	defer cancel()
	if s.lockCache != nil {
		acquired, err := s.lockCache.TryAcquireLeaderLock(lockCtx, key, s.instanceID, rateSyncLeaderLockTTL)
		if err == nil {
			if !acquired {
				return nil, false, nil
			}
			return func() {
				releaseCtx, releaseCancel := context.WithTimeout(context.Background(), rateSyncLockOpTimeout)
				defer releaseCancel()
				_ = s.lockCache.ReleaseLeaderLock(releaseCtx, key, s.instanceID)
			}, true, nil
		}
		// Redis 故障：回退 PG advisory lock，避免多实例并发执行。
		logger.LegacyPrintf(syncLogPrefix, "redis_leader_lock_failed_fallback_pg: err=%v", err)
	}
	if s.advisoryLock != nil {
		return s.advisoryLock(lockCtx, key)
	}
	return func() {}, true, nil
}
