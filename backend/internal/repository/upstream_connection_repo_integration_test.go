//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"
	"github.com/stretchr/testify/require"
)

func newTestUpstreamConnection(suffix string) *upstreamratesync.Connection {
	return &upstreamratesync.Connection{
		Name:                 "conn-" + suffix,
		BaseURL:              "https://upstream-" + suffix + ".example.com",
		AuthMode:             upstreamratesync.AuthModePassword,
		CredentialsEncrypted: "enc-creds-" + suffix,
		Enabled:              false,
		IntervalMinutes:      upstreamratesync.DefaultIntervalMinutes,
	}
}

func TestUpstreamConnectionRepository_CRUD(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := NewUpstreamConnectionRepository(tx.Client())
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	conn := newTestUpstreamConnection(suffix)
	require.NoError(t, repo.Create(ctx, conn))
	require.NotZero(t, conn.ID)
	require.False(t, conn.CreatedAt.IsZero())
	require.True(t, conn.HasCredentials())
	require.False(t, conn.HasAccessToken())

	loaded, err := repo.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.Equal(t, conn.Name, loaded.Name)
	require.Equal(t, conn.BaseURL, loaded.BaseURL)
	require.Equal(t, upstreamratesync.AuthModePassword, loaded.AuthMode)
	require.Equal(t, "enc-creds-"+suffix, loaded.CredentialsEncrypted)
	require.Equal(t, upstreamratesync.DefaultIntervalMinutes, loaded.IntervalMinutes)
	require.False(t, loaded.Enabled)
	require.Nil(t, loaded.LastSyncAt)
	require.Empty(t, loaded.LastStatus)

	loaded.Enabled = true
	loaded.IntervalMinutes = 60
	loaded.AccessTokenEncrypted = "enc-access"
	loaded.RefreshTokenEncrypted = "enc-refresh"
	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	loaded.TokenExpiresAt = &expiresAt
	require.NoError(t, repo.Update(ctx, loaded))

	reloaded, err := repo.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.True(t, reloaded.Enabled)
	require.Equal(t, 60, reloaded.IntervalMinutes)
	require.Equal(t, "enc-access", reloaded.AccessTokenEncrypted)
	require.Equal(t, "enc-refresh", reloaded.RefreshTokenEncrypted)
	require.NotNil(t, reloaded.TokenExpiresAt)
	require.True(t, expiresAt.Equal(*reloaded.TokenExpiresAt))

	// ListEnabled 只返回启用的连接
	enabled, err := repo.ListEnabled(ctx)
	require.NoError(t, err)
	found := false
	for _, c := range enabled {
		if c.ID == conn.ID {
			found = true
		}
	}
	require.True(t, found, "enabled connection should appear in ListEnabled")

	// 分页列表
	list, total, err := repo.List(ctx, upstreamratesync.ConnectionListParams{Page: 1, PageSize: 100})
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, int64(1))
	found = false
	for _, c := range list {
		if c.ID == conn.ID {
			found = true
		}
	}
	require.True(t, found)

	require.NoError(t, repo.Delete(ctx, conn.ID))
	_, err = repo.GetByID(ctx, conn.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, upstreamratesync.ErrConnectionNotFound))
}

func TestUpstreamConnectionRepository_BaseURLUniqueConflict(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := NewUpstreamConnectionRepository(tx.Client())
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	require.NoError(t, repo.Create(ctx, newTestUpstreamConnection(suffix)))

	dup := newTestUpstreamConnection(suffix)
	dup.Name = "dup-" + suffix
	err := repo.Create(ctx, dup)
	require.Error(t, err)
	require.True(t, errors.Is(err, upstreamratesync.ErrConnectionConflict),
		"duplicate base_url should map to ErrConnectionConflict, got: %v", err)
}

func TestUpstreamConnectionRepository_InvalidAuthModeRejected(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := NewUpstreamConnectionRepository(tx.Client())

	conn := newTestUpstreamConnection(fmt.Sprintf("%d", time.Now().UnixNano()))
	conn.AuthMode = "oauth"
	require.Error(t, repo.Create(ctx, conn), "invalid auth_mode should be rejected")
}

func TestUpstreamConnectionRepository_UpdateSyncResult(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := NewUpstreamConnectionRepository(tx.Client())

	conn := newTestUpstreamConnection(fmt.Sprintf("%d", time.Now().UnixNano()))
	require.NoError(t, repo.Create(ctx, conn))

	syncedAt := time.Now().Truncate(time.Second)
	require.NoError(t, repo.UpdateSyncResult(ctx, conn.ID, syncedAt, upstreamratesync.SyncStatusPartial, "2 accounts skipped"))

	loaded, err := repo.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.LastSyncAt)
	require.True(t, syncedAt.Equal(*loaded.LastSyncAt))
	require.Equal(t, upstreamratesync.SyncStatusPartial, loaded.LastStatus)
	require.Equal(t, "2 accounts skipped", loaded.LastError)

	// 清除错误摘要
	require.NoError(t, repo.UpdateSyncResult(ctx, conn.ID, syncedAt, upstreamratesync.SyncStatusSuccess, ""))
	loaded, err = repo.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.Equal(t, upstreamratesync.SyncStatusSuccess, loaded.LastStatus)
	require.Empty(t, loaded.LastError)
}

func TestUpstreamConnectionRepository_UpdateTokens(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := NewUpstreamConnectionRepository(tx.Client())

	conn := newTestUpstreamConnection(fmt.Sprintf("%d", time.Now().UnixNano()))
	require.NoError(t, repo.Create(ctx, conn))

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	refresh := "enc-refresh-v1"
	require.NoError(t, repo.UpdateTokens(ctx, conn.ID, "enc-access-v1", &refresh, expiresAt))

	loaded, err := repo.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.Equal(t, "enc-access-v1", loaded.AccessTokenEncrypted)
	require.Equal(t, "enc-refresh-v1", loaded.RefreshTokenEncrypted)
	require.NotNil(t, loaded.TokenExpiresAt)
	require.True(t, expiresAt.Equal(*loaded.TokenExpiresAt))

	// refresh 一次性轮转：新 refresh 覆盖旧的
	refreshV2 := "enc-refresh-v2"
	require.NoError(t, repo.UpdateTokens(ctx, conn.ID, "enc-access-v2", &refreshV2, expiresAt))
	loaded, err = repo.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.Equal(t, "enc-access-v2", loaded.AccessTokenEncrypted)
	require.Equal(t, "enc-refresh-v2", loaded.RefreshTokenEncrypted)

	// nil refresh 表示清除（token 模式无 refresh）
	require.NoError(t, repo.UpdateTokens(ctx, conn.ID, "enc-access-v3", nil, expiresAt))
	loaded, err = repo.GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.Equal(t, "enc-access-v3", loaded.AccessTokenEncrypted)
	require.Empty(t, loaded.RefreshTokenEncrypted)
}
