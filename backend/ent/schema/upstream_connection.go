package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UpstreamConnection holds the schema definition for the UpstreamConnection entity.
// 上游倍率同步连接：保存一个上游 sub2api 实例的地址与鉴权（密文），
// 由 UpstreamRateSyncService 按 interval_minutes 定期同步账号倍率。
// 设计见 openspec add-upstream-rate-sync（迁移 193 建表）。
type UpstreamConnection struct {
	ent.Schema
}

func (UpstreamConnection) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "upstream_connections"},
	}
}

func (UpstreamConnection) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (UpstreamConnection) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(100),
		field.String("base_url").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("归一化后的上游 base_url（小写 scheme/host、去尾斜杠、去默认端口），全局唯一，作为连接作用域隔离键"),
		field.Enum("auth_mode").
			Values("password", "token"),
		// 凭证密文列：只允许 SecretEncryptor（AES-256-GCM）密文入库，
		// 明文/密文均不得出现在日志、API 响应与同步明细中。
		field.String("credentials_encrypted").
			Optional().
			Sensitive().
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("password 模式账密 / token 模式手贴 access token 的密文"),
		field.String("access_token_encrypted").
			Optional().
			Sensitive().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("refresh_token_encrypted").
			Optional().
			Sensitive().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("token_expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("access token 到期时间，到期前提前刷新"),
		field.Bool("enabled").
			Default(false).
			Comment("定时同步开关，默认关闭"),
		field.Int("interval_minutes").
			Default(30).
			Comment("同步间隔（分钟），合法边界 5–1440（service 层校验）"),
		field.Time("last_sync_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("last_status").
			Optional().
			MaxLen(20).
			Comment("最近一次同步结果：success|partial|failed"),
		field.String("last_error").
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("最近一次错误摘要（脱敏，不含 token/密码）"),
	}
}

func (UpstreamConnection) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("sync_runs", UpstreamSyncRun.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (UpstreamConnection) Indexes() []ent.Index {
	return []ent.Index{
		// base_url 归一化结果全局唯一（多连接按 base_url 作用域隔离）
		index.Fields("base_url").
			Unique().
			StorageKey("idx_upstream_connections_base_url"),
	}
}
