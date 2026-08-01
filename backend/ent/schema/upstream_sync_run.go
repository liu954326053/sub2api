package schema

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UpstreamSyncRun holds the schema definition for the UpstreamSyncRun entity.
// 上游倍率同步日志：每次同步一条 run 记录（五个计数 + JSONB 明细数组）。
// 日志类表无软删除：超保留期由清理任务分批物理删（每连接保留最近 200 条
// 且 30 天内，见迁移 193 与 SyncRunRepository.Prune）。
type UpstreamSyncRun struct {
	ent.Schema
}

func (UpstreamSyncRun) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_sync_runs"},
	}
}

func (UpstreamSyncRun) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("connection_id"),
		field.Time("started_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("finished_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("NULL 表示进行中"),
		field.Enum("status").
			Values("success", "partial", "failed").
			Default("success"),
		field.Int("keys_fetched").
			Default(0).
			NonNegative().
			Comment("上游拉到的 key 总数"),
		field.Int("accounts_matched").
			Default(0).
			NonNegative().
			Comment("匹配到本地账号数"),
		field.Int("accounts_updated").
			Default(0).
			NonNegative().
			Comment("实际写回数"),
		field.Int("accounts_unchanged").
			Default(0).
			NonNegative().
			Comment("值未变化跳过数"),
		field.Int("accounts_unmatched").
			Default(0).
			NonNegative().
			Comment("本地 key 在上游找不到数"),
		field.JSON("details", []upstreamratesync.SyncRunDetail{}).
			Default([]upstreamratesync.SyncRunDetail{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("明细数组 {account_id,key_prefix,group_name,old_rate,new_rate,action}"),
		field.String("error").
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("失败原因摘要（脱敏）"),
	}
}

func (UpstreamSyncRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("connection", UpstreamConnection.Type).
			Ref("sync_runs").
			Field("connection_id").
			Unique().
			Required(),
	}
}

func (UpstreamSyncRun) Indexes() []ent.Index {
	return []ent.Index{
		// 日志页按连接/状态/时间筛选（迁移 193 中实际建 DESC 索引）
		index.Fields("connection_id", "started_at").
			StorageKey("idx_upstream_sync_runs_connection_started"),
		index.Fields("status", "started_at").
			StorageKey("idx_upstream_sync_runs_status_started"),
	}
}
