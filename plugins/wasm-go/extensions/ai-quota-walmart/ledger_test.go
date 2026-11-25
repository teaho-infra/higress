package main

import (
	"testing"
	"time"
)

func TestPeriod(t *testing.T) {
	for _, c := range []struct{ at, want string }{{"2026-01-18T23:59:59+08:00", "2026-01-05"}, {"2026-01-19T00:00:00+08:00", "2026-01-19"}, {"2026-01-04T23:59:59+08:00", "2025-12-22"}} {
		v, _ := time.Parse(time.RFC3339, c.at)
		p, _, _ := periodAt(v)
		if p != c.want {
			t.Fatal(p, c.want)
		}
	}
}
func TestValidAmount(t *testing.T) {
	for _, s := range []string{"2300000000", "-2300000000", "9999999999999"} {
		if !validAmount(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"1.2", "10000000000000", "abc"} {
		if validAmount(s) {
			t.Fatal(s)
		}
	}
}

func TestTierValidation(t *testing.T) {
	if err := validateTiers(defaultTiers); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"L1":{"quota":1},"L2":{"quota":0},"L3":{"quota":2}}`, `{"L1":{"quota":1.5},"L2":{"quota":2}}`} {
		if validateTiers(raw) == nil {
			t.Fatal(raw)
		}
	}
}

func TestPublicAdminBoundary(t *testing.T) {
	for _, a := range []string{"check", "settle", "unknown"} {
		if publicAction("POST", a) || publicAction("GET", a) {
			t.Fatal(a)
		}
	}
	for _, k := range []string{"action", "operator", "period_start_unix", "period_end_unix", "payer_dimension", "fingerprint", "occurred_at"} {
		if validateAdminFields("delta", map[string]any{k: "evil"}) == nil {
			t.Fatal(k)
		}
	}
	if !publicAction("GET", "accounts") || !publicAction("POST", "assign") {
		t.Fatal("public actions rejected")
	}
	if !publicAction("POST", "cycle") || !publicAction("POST", "reset") || publicAction("GET", "reset") {
		t.Fatal("money cycle actions rejected")
	}
	if validateAdminFields("cycle", map[string]any{"days": 7, "period": "p", "ticket_id": "t"}) != nil ||
		validateAdminFields("reset", map[string]any{"days": 7}) == nil {
		t.Fatal("cycle action fields invalid")
	}
	if validateAdminFields("assign", map[string]any{"dimension": "user", "subject": "u", "tier": "L2", "period": "p", "ticket_id": "t", "app": "a"}) != nil {
		t.Fatal("valid fields rejected")
	}
}

func TestAdminAppType(t *testing.T) {
	for _, v := range []any{map[string]any{}, []any{}, true, 123} {
		if validateAdminFields("assign", map[string]any{"app": v}) == nil {
			t.Fatal(v)
		}
	}
	if validateAdminFields("delta", map[string]any{"app": "a"}) == nil {
		t.Fatal("delta must not change association")
	}
}

func TestMissingModelRejected(t *testing.T) {
	for _, v := range []any{nil, 123, "", "  "} {
		if validModel(v) {
			t.Fatal(v)
		}
	}
	if !validModel("demo-model") {
		t.Fatal("model")
	}
}
func TestNullModelsRejected(t *testing.T) {
	if validateTiers(`{"L1":{"quota":1,"models":null},"L2":{"quota":2}}`) == nil {
		t.Fatal("null models")
	}
}

func TestModelArrayNormalization(t *testing.T) {
	v := map[string]any{"tiers": map[string]any{"L1": map[string]any{"models": map[string]any{}}}, "records": []any{map[string]any{"after": map[string]any{"models": map[string]any{}}}}}
	normalizeModelArrays(v)
	tiers := v["tiers"].(map[string]any)
	if _, ok := tiers["L1"].(map[string]any)["models"].([]any); !ok {
		t.Fatal(v)
	}
}
