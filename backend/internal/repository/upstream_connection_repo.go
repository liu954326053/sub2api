package repository

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/upstreamconnection"
	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"
)

// upstreamConnectionRepository 实现 upstreamratesync.ConnectionRepository。
//
// 凭证字段（credentials/access_token/refresh_token）只保存密文；
// base_url 由调用方归一化后传入，唯一约束冲突翻译为 ErrConnectionConflict。
type upstreamConnectionRepository struct {
	client *dbent.Client
}

// NewUpstreamConnectionRepository 创建仓储实例。
func NewUpstreamConnectionRepository(client *dbent.Client) upstreamratesync.ConnectionRepository {
	return &upstreamConnectionRepository{client: client}
}

func (r *upstreamConnectionRepository) Create(ctx context.Context, conn *upstreamratesync.Connection) error {
	client := clientFromContext(ctx, r.client)
	builder := client.UpstreamConnection.Create().
		SetName(conn.Name).
		SetBaseURL(conn.BaseURL).
		SetAuthMode(upstreamconnection.AuthMode(conn.AuthMode)).
		SetCredentialsEncrypted(conn.CredentialsEncrypted).
		SetAccessTokenEncrypted(conn.AccessTokenEncrypted).
		SetRefreshTokenEncrypted(conn.RefreshTokenEncrypted).
		SetEnabled(conn.Enabled).
		SetIntervalMinutes(conn.IntervalMinutes)
	if conn.TokenExpiresAt != nil {
		builder = builder.SetTokenExpiresAt(*conn.TokenExpiresAt)
	}
	if conn.LastSyncAt != nil {
		builder = builder.SetLastSyncAt(*conn.LastSyncAt)
	}
	if conn.LastStatus != "" {
		builder = builder.SetLastStatus(conn.LastStatus)
	}
	if conn.LastError != "" {
		builder = builder.SetLastError(conn.LastError)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, upstreamratesync.ErrConnectionConflict)
	}
	conn.ID = created.ID
	conn.CreatedAt = created.CreatedAt
	conn.UpdatedAt = created.UpdatedAt
	return nil
}

func (r *upstreamConnectionRepository) GetByID(ctx context.Context, id int64) (*upstreamratesync.Connection, error) {
	row, err := r.client.UpstreamConnection.Query().
		Where(upstreamconnection.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, upstreamratesync.ErrConnectionNotFound, nil)
	}
	return entToUpstreamConnection(row), nil
}

func (r *upstreamConnectionRepository) Update(ctx context.Context, conn *upstreamratesync.Connection) error {
	client := clientFromContext(ctx, r.client)
	updater := client.UpstreamConnection.UpdateOneID(conn.ID).
		SetName(conn.Name).
		SetBaseURL(conn.BaseURL).
		SetAuthMode(upstreamconnection.AuthMode(conn.AuthMode)).
		SetCredentialsEncrypted(conn.CredentialsEncrypted).
		SetAccessTokenEncrypted(conn.AccessTokenEncrypted).
		SetRefreshTokenEncrypted(conn.RefreshTokenEncrypted).
		SetEnabled(conn.Enabled).
		SetIntervalMinutes(conn.IntervalMinutes).
		SetLastStatus(conn.LastStatus).
		SetLastError(conn.LastError)
	if conn.TokenExpiresAt != nil {
		updater = updater.SetTokenExpiresAt(*conn.TokenExpiresAt)
	} else {
		updater = updater.ClearTokenExpiresAt()
	}
	if conn.LastSyncAt != nil {
		updater = updater.SetLastSyncAt(*conn.LastSyncAt)
	} else {
		updater = updater.ClearLastSyncAt()
	}

	updated, err := updater.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, upstreamratesync.ErrConnectionNotFound, upstreamratesync.ErrConnectionConflict)
	}
	conn.UpdatedAt = updated.UpdatedAt
	return nil
}

func (r *upstreamConnectionRepository) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	if err := client.UpstreamConnection.DeleteOneID(id).Exec(ctx); err != nil {
		return translatePersistenceError(err, upstreamratesync.ErrConnectionNotFound, nil)
	}
	return nil
}

func (r *upstreamConnectionRepository) List(ctx context.Context, params upstreamratesync.ConnectionListParams) ([]*upstreamratesync.Connection, int64, error) {
	q := r.client.UpstreamConnection.Query()

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count upstream connections: %w", err)
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
		Order(dbent.Desc(upstreamconnection.FieldID)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list upstream connections: %w", err)
	}

	out := make([]*upstreamratesync.Connection, 0, len(rows))
	for _, row := range rows {
		out = append(out, entToUpstreamConnection(row))
	}
	return out, int64(total), nil
}

// ListEnabled 返回全部 enabled=true 的连接，runner 按 interval_minutes 与
// last_sync_at 判定到期（due）连接。
func (r *upstreamConnectionRepository) ListEnabled(ctx context.Context) ([]*upstreamratesync.Connection, error) {
	rows, err := r.client.UpstreamConnection.Query().
		Where(upstreamconnection.EnabledEQ(true)).
		Order(dbent.Asc(upstreamconnection.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled upstream connections: %w", err)
	}
	out := make([]*upstreamratesync.Connection, 0, len(rows))
	for _, row := range rows {
		out = append(out, entToUpstreamConnection(row))
	}
	return out, nil
}

func (r *upstreamConnectionRepository) UpdateSyncResult(ctx context.Context, id int64, syncedAt time.Time, status string, lastError string) error {
	client := clientFromContext(ctx, r.client)
	if err := client.UpstreamConnection.UpdateOneID(id).
		SetLastSyncAt(syncedAt).
		SetLastStatus(status).
		SetLastError(lastError).
		Exec(ctx); err != nil {
		return translatePersistenceError(err, upstreamratesync.ErrConnectionNotFound, nil)
	}
	return nil
}

// UpdateTokens refresh 轮转成功后持久化最新 token。
// refreshTokenEncrypted 为 nil 表示清除 refresh token（token 模式无 refresh）。
func (r *upstreamConnectionRepository) UpdateTokens(ctx context.Context, id int64, accessTokenEncrypted string, refreshTokenEncrypted *string, tokenExpiresAt time.Time) error {
	client := clientFromContext(ctx, r.client)
	updater := client.UpstreamConnection.UpdateOneID(id).
		SetAccessTokenEncrypted(accessTokenEncrypted).
		SetTokenExpiresAt(tokenExpiresAt)
	if refreshTokenEncrypted != nil {
		updater = updater.SetRefreshTokenEncrypted(*refreshTokenEncrypted)
	} else {
		updater = updater.SetRefreshTokenEncrypted("")
	}
	if err := updater.Exec(ctx); err != nil {
		return translatePersistenceError(err, upstreamratesync.ErrConnectionNotFound, nil)
	}
	return nil
}

func entToUpstreamConnection(row *dbent.UpstreamConnection) *upstreamratesync.Connection {
	if row == nil {
		return nil
	}
	return &upstreamratesync.Connection{
		ID:                    row.ID,
		Name:                  row.Name,
		BaseURL:               row.BaseURL,
		AuthMode:              string(row.AuthMode),
		CredentialsEncrypted:  row.CredentialsEncrypted, // 仍为密文，service 层负责解密
		AccessTokenEncrypted:  row.AccessTokenEncrypted,
		RefreshTokenEncrypted: row.RefreshTokenEncrypted,
		TokenExpiresAt:        row.TokenExpiresAt,
		Enabled:               row.Enabled,
		IntervalMinutes:       row.IntervalMinutes,
		LastSyncAt:            row.LastSyncAt,
		LastStatus:            row.LastStatus,
		LastError:             row.LastError,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}
