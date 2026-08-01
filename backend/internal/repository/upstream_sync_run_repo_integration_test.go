//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"
	"github.com/stretchr/testify/require"
)

// mustCreateUpstreamConnection 在测试事务中创建一个连接并返回其 ID。
func mustCreateUpstreamConnection(t *testing.T, client *dbent.Client) int64 {
	t.Helper()
	repo := NewUpstreamConnectionRepository(client)
	conn := newTestUpstreamConnection(fmt.Sprintf("%d", time.Now().UnixNano()))
	require.NoError(t, repo.Create(context.Background(), conn))
	return conn.ID
}

func TestUpstreamSyncRunRepository_CreateFinishGetByID(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	connID := mustCreateUpstreamConnection(t, client)
	repo := NewUpstreamSyncRunRepository(client, integrationDB)

	run := &upstreamratesync.SyncRun{ConnectionID: connID}
	require.NoError(t, repo.Create(ctx, run))
	require.NotZero(t, run.ID)
	require.False(t, run.StartedAt.IsZero())
	require.Equal(t, upstreamratesync.SyncStatusSuccess, run.Status, "status 缺省应为 success")

	// 进行中：finished_at 为 NULL
	loaded, err := repo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Nil(t, loaded.FinishedAt)
	require.Empty(t, loaded.Details, "details 缺省应为空数组")

	// 结束 run：填齐五计数 + details JSONB 明细
	oldRate, newRate := 1.0, 1.5
	run.Status = upstreamratesync.SyncStatusPartial
	run.KeysFetched = 3
	run.AccountsMatched = 2
	run.AccountsUpdated = 1
	run.AccountsUnchanged = 1
	run.AccountsUnmatched = 1
	run.Details = []upstreamratesync.SyncRunDetail{
		{AccountID: 101, KeyPrefix: "sk-abcd123", GroupName: "vip", OldRate: &oldRate, NewRate: &newRate, Action: upstreamratesync.DetailActionUpdated},
		{AccountID: 102, KeyPrefix: "sk-efgh456", GroupName: "vip", OldRate: &oldRate, NewRate: &oldRate, Action: upstreamratesync.DetailActionUnchanged},
		{AccountID: 103, KeyPrefix: "sk-ijkl789", GroupName: "", OldRate: &oldRate, NewRate: nil, Action: upstreamratesync.DetailActionUnmatched},
	}
	run.Error = "1 account threshold skipped"
	require.NoError(t, repo.Finish(ctx, run))
	require.NotNil(t, run.FinishedAt)

	finished, err := repo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, upstreamratesync.SyncStatusPartial, finished.Status)
	require.NotNil(t, finished.FinishedAt)
	require.Equal(t, 3, finished.KeysFetched)
	require.Equal(t, 2, finished.AccountsMatched)
	require.Equal(t, 1, finished.AccountsUpdated)
	require.Equal(t, 1, finished.AccountsUnchanged)
	require.Equal(t, 1, finished.AccountsUnmatched)
	require.Equal(t, "1 account threshold skipped", finished.Error)

	// details JSONB 往返：字段与指针语义保持一致
	require.Len(t, finished.Details, 3)
	require.Equal(t, int64(101), finished.Details[0].AccountID)
	require.Equal(t, "sk-abcd123", finished.Details[0].KeyPrefix)
	require.Equal(t, "vip", finished.Details[0].GroupName)
	require.NotNil(t, finished.Details[0].OldRate)
	require.InDelta(t, 1.0, *finished.Details[0].OldRate, 1e-9)
	require.NotNil(t, finished.Details[0].NewRate)
	require.InDelta(t, 1.5, *finished.Details[0].NewRate, 1e-9)
	require.Equal(t, upstreamratesync.DetailActionUpdated, finished.Details[0].Action)
	require.Nil(t, finished.Details[2].NewRate, "unmatched 明细的 new_rate 应为 null")
	require.Equal(t, upstreamratesync.DetailActionUnmatched, finished.Details[2].Action)
}

func TestUpstreamSyncRunRepository_ListFiltersAndPagination(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	connID := mustCreateUpstreamConnection(t, client)
	otherConnID := mustCreateUpstreamConnection(t, client)
	repo := NewUpstreamSyncRunRepository(client, integrationDB)

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 5; i++ {
		status := upstreamratesync.SyncStatusSuccess
		if i%2 == 1 {
			status = upstreamratesync.SyncStatusFailed
		}
		run := &upstreamratesync.SyncRun{
			ConnectionID: connID,
			StartedAt:    base.Add(time.Duration(i) * time.Minute),
			Status:       status,
		}
		require.NoError(t, repo.Create(ctx, run))
	}
	// 另一个连接的一条 run，验证 connection_id 过滤
	require.NoError(t, repo.Create(ctx, &upstreamratesync.SyncRun{ConnectionID: otherConnID, Status: upstreamratesync.SyncStatusSuccess}))

	// 按 connection_id 过滤
	runs, total, err := repo.List(ctx, upstreamratesync.SyncRunListParams{ConnectionID: &connID, Page: 1, PageSize: 100})
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
	require.Len(t, runs, 5)

	// 按 status 过滤
	failed := 0
	for _, r := range runs {
		if r.Status == upstreamratesync.SyncStatusFailed {
			failed++
		}
	}
	require.Equal(t, 2, failed)
	failedRuns, failedTotal, err := repo.List(ctx, upstreamratesync.SyncRunListParams{
		ConnectionID: &connID, Status: upstreamratesync.SyncStatusFailed, Page: 1, PageSize: 100,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), failedTotal)
	require.Len(t, failedRuns, 2)

	// 按时间范围过滤
	from := base.Add(2 * time.Minute)
	to := base.Add(3 * time.Minute)
	ranged, rangedTotal, err := repo.List(ctx, upstreamratesync.SyncRunListParams{
		ConnectionID: &connID, StartedFrom: &from, StartedTo: &to, Page: 1, PageSize: 100,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), rangedTotal)
	require.Len(t, ranged, 2)

	// 分页：按 started_at 倒序，第一页取最新 2 条
	page1, totalAll, err := repo.List(ctx, upstreamratesync.SyncRunListParams{ConnectionID: &connID, Page: 1, PageSize: 2})
	require.NoError(t, err)
	require.Equal(t, int64(5), totalAll)
	require.Len(t, page1, 2)
	require.True(t, !page1[0].StartedAt.Before(page1[1].StartedAt), "应按 started_at 倒序")
	page2, _, err := repo.List(ctx, upstreamratesync.SyncRunListParams{ConnectionID: &connID, Page: 2, PageSize: 2})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	require.NotEqual(t, page1[0].ID, page2[0].ID)
}

func TestUpstreamSyncRunRepository_PruneRetentionBoundaries(t *testing.T) {
	ctx := context.Background()
	// Prune 走原生 SQL（integrationDB），不能使用 testEntTx 的事务内数据；
	// 使用真实 client 写入并在测试结束时清理。
	client := integrationEntClient
	connID := mustCreateUpstreamConnection(t, client)
	repo := NewUpstreamSyncRunRepository(client, integrationDB)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM upstream_connections WHERE id = $1", connID)
		require.NoError(t, err)
	})

	now := time.Now()
	// 老 run：超出 30 天保留期，无论数量都应删除
	oldRun := &upstreamratesync.SyncRun{
		ConnectionID: connID,
		StartedAt:    now.Add(-31 * 24 * time.Hour),
		Status:       upstreamratesync.SyncStatusSuccess,
	}
	require.NoError(t, repo.Create(ctx, oldRun))
	// 新 run：在保留期内且未超每连接条数上限，应保留
	newRun := &upstreamratesync.SyncRun{
		ConnectionID: connID,
		StartedAt:    now.Add(-time.Hour),
		Status:       upstreamratesync.SyncStatusSuccess,
	}
	require.NoError(t, repo.Create(ctx, newRun))

	deleted, err := repo.Prune(ctx, upstreamratesync.RunRetentionDays, upstreamratesync.KeepRunsPerConnection)
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(1))

	_, err = repo.GetByID(ctx, oldRun.ID)
	require.True(t, errors.Is(err, upstreamratesync.ErrSyncRunNotFound), "超 30 天的 run 应被清理")
	_, err = repo.GetByID(ctx, newRun.ID)
	require.NoError(t, err, "保留期内的 run 应保留")

	// 每连接条数上限：keep=1 时只剩最新一条
	deleted, err = repo.Prune(ctx, upstreamratesync.RunRetentionDays, 1)
	require.NoError(t, err)
	require.Equal(t, int64(0), deleted, "只有一条 run 时不应再删除")

	extra := &upstreamratesync.SyncRun{
		ConnectionID: connID,
		StartedAt:    now.Add(-time.Minute),
		Status:       upstreamratesync.SyncStatusSuccess,
	}
	require.NoError(t, repo.Create(ctx, extra))
	deleted, err = repo.Prune(ctx, upstreamratesync.RunRetentionDays, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted, "keep=1 时应删除较旧的一条")
	_, err = repo.GetByID(ctx, extra.ID)
	require.NoError(t, err, "最新的 run 应保留")
	_, err = repo.GetByID(ctx, newRun.ID)
	require.True(t, errors.Is(err, upstreamratesync.ErrSyncRunNotFound), "较旧的 run 应被清理")
}

func TestUpstreamSyncRunRepository_CascadeDeleteWithConnection(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	connID := mustCreateUpstreamConnection(t, client)
	runRepo := NewUpstreamSyncRunRepository(client, integrationDB)
	connRepo := NewUpstreamConnectionRepository(client)

	run := &upstreamratesync.SyncRun{ConnectionID: connID}
	require.NoError(t, runRepo.Create(ctx, run))

	require.NoError(t, connRepo.Delete(ctx, connID))
	_, err := runRepo.GetByID(ctx, run.ID)
	require.True(t, errors.Is(err, upstreamratesync.ErrSyncRunNotFound),
		"删除连接应级联删除其 run")
}
