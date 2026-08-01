package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/upstreamsyncrun"
	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"
)

// upstreamSyncRunRepository 实现 upstreamratesync.SyncRunRepository。
//
// 选型说明：
//   - 创建/结束/查询走 ent，复用项目的事务上下文支持
//   - 超保留期清理走原生 SQL 分批删除（与 channel_monitor 明细清理同一模式），
//     避免单事务删除过多引起锁/WAL 压力
type upstreamSyncRunRepository struct {
	client *dbent.Client
	db     *sql.DB
}

// NewUpstreamSyncRunRepository 创建仓储实例。
func NewUpstreamSyncRunRepository(client *dbent.Client, db *sql.DB) upstreamratesync.SyncRunRepository {
	return &upstreamSyncRunRepository{client: client, db: db}
}

func (r *upstreamSyncRunRepository) Create(ctx context.Context, run *upstreamratesync.SyncRun) error {
	client := clientFromContext(ctx, r.client)
	builder := client.UpstreamSyncRun.Create().
		SetConnectionID(run.ConnectionID).
		SetKeysFetched(run.KeysFetched).
		SetAccountsMatched(run.AccountsMatched).
		SetAccountsUpdated(run.AccountsUpdated).
		SetAccountsUnchanged(run.AccountsUnchanged).
		SetAccountsUnmatched(run.AccountsUnmatched).
		SetDetails(syncRunDetailsForPersistence(run.Details))
	if !run.StartedAt.IsZero() {
		builder = builder.SetStartedAt(run.StartedAt)
	}
	if run.Status != "" {
		builder = builder.SetStatus(upstreamsyncrun.Status(run.Status))
	}
	if run.FinishedAt != nil {
		builder = builder.SetFinishedAt(*run.FinishedAt)
	}
	if run.Error != "" {
		builder = builder.SetError(run.Error)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return fmt.Errorf("create upstream sync run: %w", err)
	}
	run.ID = created.ID
	run.StartedAt = created.StartedAt
	if run.Status == "" {
		run.Status = string(created.Status)
	}
	return nil
}

// Finish 结束 run：按 run.ID 更新 status/finished_at/五个计数/details/error。
// finished_at 未设置时取当前时间。
func (r *upstreamSyncRunRepository) Finish(ctx context.Context, run *upstreamratesync.SyncRun) error {
	client := clientFromContext(ctx, r.client)
	finishedAt := time.Now()
	if run.FinishedAt != nil {
		finishedAt = *run.FinishedAt
	}
	updater := client.UpstreamSyncRun.UpdateOneID(run.ID).
		SetFinishedAt(finishedAt).
		SetStatus(upstreamsyncrun.Status(run.Status)).
		SetKeysFetched(run.KeysFetched).
		SetAccountsMatched(run.AccountsMatched).
		SetAccountsUpdated(run.AccountsUpdated).
		SetAccountsUnchanged(run.AccountsUnchanged).
		SetAccountsUnmatched(run.AccountsUnmatched).
		SetDetails(syncRunDetailsForPersistence(run.Details)).
		SetError(run.Error)

	updated, err := updater.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, upstreamratesync.ErrSyncRunNotFound, nil)
	}
	run.FinishedAt = updated.FinishedAt
	return nil
}

func (r *upstreamSyncRunRepository) GetByID(ctx context.Context, id int64) (*upstreamratesync.SyncRun, error) {
	row, err := r.client.UpstreamSyncRun.Query().
		Where(upstreamsyncrun.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, upstreamratesync.ErrSyncRunNotFound, nil)
	}
	return entToUpstreamSyncRun(row), nil
}

// List 按连接/状态/时间范围筛选，按 started_at、id 倒序分页。
func (r *upstreamSyncRunRepository) List(ctx context.Context, params upstreamratesync.SyncRunListParams) ([]*upstreamratesync.SyncRun, int64, error) {
	q := r.client.UpstreamSyncRun.Query()
	if params.ConnectionID != nil {
		q = q.Where(upstreamsyncrun.ConnectionIDEQ(*params.ConnectionID))
	}
	if params.Status != "" {
		q = q.Where(upstreamsyncrun.StatusEQ(upstreamsyncrun.Status(params.Status)))
	}
	if params.StartedFrom != nil {
		q = q.Where(upstreamsyncrun.StartedAtGTE(*params.StartedFrom))
	}
	if params.StartedTo != nil {
		q = q.Where(upstreamsyncrun.StartedAtLTE(*params.StartedTo))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count upstream sync runs: %w", err)
	}

	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	page := params.Page
	if page <= 0 {
		page = 1
	}

	rows, err := q.
		Order(dbent.Desc(upstreamsyncrun.FieldStartedAt), dbent.Desc(upstreamsyncrun.FieldID)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list upstream sync runs: %w", err)
	}

	out := make([]*upstreamratesync.SyncRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, entToUpstreamSyncRun(row))
	}
	return out, int64(total), nil
}

// Prune 清理超保留期的 run：每连接仅保留最近 keepPerConnection 条（按 id 排序），
// 且删除 started_at 早于 retentionDays cutoff 的记录。分批物理删除，返回累计删除行数。
func (r *upstreamSyncRunRepository) Prune(ctx context.Context, retentionDays int, keepPerConnection int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = upstreamratesync.RunRetentionDays
	}
	if keepPerConnection <= 0 {
		keepPerConnection = upstreamratesync.KeepRunsPerConnection
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)

	var total int64
	for {
		res, err := r.db.ExecContext(ctx, upstreamSyncRunPruneSQL, keepPerConnection, cutoff, upstreamSyncRunPruneBatchSize)
		if err != nil {
			return total, fmt.Errorf("upstream sync run prune batch: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("upstream sync run prune rows affected: %w", err)
		}
		total += affected
		if affected == 0 {
			break
		}
	}
	return total, nil
}

// upstreamSyncRunPruneBatchSize 单批删除上限，与 channel_monitor / ops_cleanup
// 保持一致的 5000：按 id 小批删可以避免长事务和 WAL 堆积。
const upstreamSyncRunPruneBatchSize = 5000

// upstreamSyncRunPruneSQL 分批物理删超保留期的 run：
//   - 每连接 id 排名超过 $1（keepPerConnection）的记录
//   - 或 started_at 早于 $2（retention cutoff）的记录
// 借助 (connection_id, started_at DESC) 索引与 id 主键定位小批后按 id 删。
const upstreamSyncRunPruneSQL = `
WITH ranked AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY id DESC) AS rn
    FROM upstream_sync_runs
),
doomed AS (
    SELECT id FROM ranked WHERE rn > $1
    UNION
    SELECT id FROM upstream_sync_runs WHERE started_at < $2
),
batch AS (
    SELECT id FROM doomed ORDER BY id LIMIT $3
)
DELETE FROM upstream_sync_runs
WHERE id IN (SELECT id FROM batch)
`

func entToUpstreamSyncRun(row *dbent.UpstreamSyncRun) *upstreamratesync.SyncRun {
	if row == nil {
		return nil
	}
	details := row.Details
	if details == nil {
		details = []upstreamratesync.SyncRunDetail{}
	}
	return &upstreamratesync.SyncRun{
		ID:                row.ID,
		ConnectionID:      row.ConnectionID,
		StartedAt:         row.StartedAt,
		FinishedAt:        row.FinishedAt,
		Status:            string(row.Status),
		KeysFetched:       row.KeysFetched,
		AccountsMatched:   row.AccountsMatched,
		AccountsUpdated:   row.AccountsUpdated,
		AccountsUnchanged: row.AccountsUnchanged,
		AccountsUnmatched: row.AccountsUnmatched,
		Details:           details,
		Error:             row.Error,
	}
}

// syncRunDetailsForPersistence nil 归一为空数组，保证 details 列始终为 JSON 数组。
func syncRunDetailsForPersistence(details []upstreamratesync.SyncRunDetail) []upstreamratesync.SyncRunDetail {
	if details == nil {
		return []upstreamratesync.SyncRunDetail{}
	}
	return details
}
