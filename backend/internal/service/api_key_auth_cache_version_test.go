package service

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAPIKeyService_RejectsV10AuthSnapshotWithoutModelsListConfig(t *testing.T) {
	groupID := int64(9)
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-models-list", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{
			Version:  10,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   StatusActive,
			User: APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      StatusActive,
				Role:        RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &APIKeyAuthGroupSnapshot{
				ID:               groupID,
				Name:             "openai",
				Platform:         PlatformOpenAI,
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeStandard,
				RateMultiplier:   1,
			},
		},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected v10 auth snapshot to be rejected after models_list_config was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV15AuthSnapshotWithoutReasoningEffortPolicy(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-reasoning-mappings", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 15},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v15 auth snapshot to be rejected after reasoning effort policy was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV17AuthSnapshotWithoutBillingMode(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-billing-mode", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 17},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v17 auth snapshot to be rejected after group billing_mode was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyAuthSnapshot_BillingModeRoundTrip(t *testing.T) {
	svc := &APIKeyService{}
	groupID := int64(9)
	source := &APIKey{
		ID:      1,
		UserID:  2,
		GroupID: &groupID,
		Status:  StatusActive,
		User:    &User{ID: 2, Status: StatusActive, Role: RoleUser},
		Group: &Group{
			ID:             groupID,
			Name:           "openai",
			Platform:       PlatformOpenAI,
			Status:         StatusActive,
			RateMultiplier: 1.5,
			BillingMode:    GroupBillingModeAccountUpstream,
		},
	}

	snapshot := svc.snapshotFromAPIKey(context.Background(), source)
	if snapshot == nil || snapshot.Group == nil {
		t.Fatal("expected snapshot with group")
	}
	if snapshot.Group.BillingMode != GroupBillingModeAccountUpstream {
		t.Fatalf("expected snapshot billing_mode %q, got %q", GroupBillingModeAccountUpstream, snapshot.Group.BillingMode)
	}

	// 序列化往返后 billing_mode 不丢失。
	raw, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var entry APIKeyAuthCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	restored := svc.snapshotToAPIKey("k-roundtrip", entry.Snapshot)
	if restored == nil || restored.Group == nil {
		t.Fatal("expected restored api key with group")
	}
	if restored.Group.BillingMode != GroupBillingModeAccountUpstream {
		t.Fatalf("expected restored billing_mode %q, got %q", GroupBillingModeAccountUpstream, restored.Group.BillingMode)
	}
}

func TestAPIKeyAuthSnapshot_LegacyWithoutBillingModeRestoresDefault(t *testing.T) {
	svc := &APIKeyService{}

	// 旧版本快照不含 billing_mode 字段：反序列化后还原必须落默认值
	// group_multiplier，而不是被当作 account_upstream（或空值歧义）。
	raw := `{"snapshot":{"version":17,"api_key_id":1,"user_id":2,"group_id":9,"status":"active",` +
		`"user":{"id":2,"status":"active","role":"user"},` +
		`"group":{"id":9,"name":"openai","platform":"openai","status":"active","rate_multiplier":1.5}}}`
	var entry APIKeyAuthCacheEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal legacy snapshot: %v", err)
	}
	restored := svc.snapshotToAPIKey("k-legacy", entry.Snapshot)
	if restored == nil || restored.Group == nil {
		t.Fatal("expected restored api key with group")
	}
	if restored.Group.BillingMode != GroupBillingModeGroupMultiplier {
		t.Fatalf("expected legacy snapshot billing_mode to default to %q, got %q", GroupBillingModeGroupMultiplier, restored.Group.BillingMode)
	}
	if got := resolveBillingBaseMultiplier(restored.Group, &Account{RateMultiplier: f64p(2.5)}); got != 1.5 {
		t.Fatalf("expected legacy-restored group to bill by group rate 1.5, got %v", got)
	}
}
