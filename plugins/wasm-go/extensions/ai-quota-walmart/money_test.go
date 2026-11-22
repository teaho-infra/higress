package main

import (
	"encoding/json"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"testing"
)

func TestMoneyCost(t *testing.T) {
	for _, c := range []struct{ i, o, p, q, w int64 }{{1, 1, 1, 1, 1}, {1000000, 2000000, 2000000, 6000000, 14000000}, {9999999999998, 1, 999999, 1, 9999989999999}} {
		got, e := moneyCost(c.i, c.o, c.p, c.q)
		if e != nil || got != c.w {
			t.Fatalf("%+v got %d %v", c, got, e)
		}
	}
	if _, e := moneyCost(9999999999999, 0, 9999999999999, 0); e == nil {
		t.Fatal("overflow")
	}
}
func TestMoneyPricesValidation(t *testing.T) {
	for _, s := range []string{`null`, `[]`, `{"x":{"input_price_micros":1}}`, `{"x":{"input_price_micros":1.1,"output_price_micros":0}}`, `{"x":{"input_price_micros":0,"output_price_micros":0,"extra":1}}`} {
		if validatePrices(s) == nil {
			t.Fatal(s)
		}
	}
	if validatePrices(`{"x":{"input_price_micros":0,"output_price_micros":1}}`) != nil {
		t.Fatal("valid")
	}
}
func TestPriceStringsRejected(t *testing.T) {
	if validatePrices(`{"m":{"input_price_micros":"1","output_price_micros":0}}`) == nil {
		t.Fatal("numeric string accepted")
	}
}

type moneyRecorder struct {
	wrapper.RedisClient
	requests []map[string]any
}

func (c *moneyRecorder) Eval(script string, num int, keys []interface{}, args []interface{}, cb wrapper.RedisResponseCallback) error {
	var r map[string]any
	json.Unmarshal([]byte(args[0].(string)), &r)
	c.requests = append(c.requests, r)
	return nil
}
func TestMoneySettlementFrozenQuote(t *testing.T) {
	c := &moneyRecorder{}
	ctx := newRecordingHTTPContext(ChatModeCompletion, "app")
	ctx.SetContext("wmRequest", map[string]any{"action": "check", "period": "old", "subject": "original", "app": "app"})
	ctx.SetContext("wmPayer", "user")
	ctx.SetContext("wmPayerSubject", "actual-user")
	ctx.SetContext("wmQuote", map[string]any{"input_price_micros": float64(1), "output_price_micros": float64(1)})
	config := QuotaConfig{Dimensions: true, BudgetPolicy: moneyPolicy, Prices: `{"m":{"input_price_micros":1000000,"output_price_micros":1000000}}`, redisClient: c}
	processResponseUsage(ctx, config, []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`), true)
	processResponseUsage(ctx, config, []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`), true)
	if len(c.requests) != 1 {
		t.Fatalf("settlements %d", len(c.requests))
	}
	r := c.requests[0]
	if r["cost_micros"] != float64(1) || r["subject"] != "actual-user" || r["period"] != "old" {
		t.Fatal(r)
	}
}
func TestMoneyOverflowBlocksOriginalPayer(t *testing.T) {
	c := &moneyRecorder{}
	ctx := newRecordingHTTPContext(ChatModeCompletion, "a")
	ctx.SetContext("wmRequest", map[string]any{"period": "original", "app": "a"})
	ctx.SetContext("wmPayer", "user")
	ctx.SetContext("wmPayerSubject", "original-user")
	ctx.SetContext("wmQuote", map[string]any{"input_price_micros": float64(9999999999999), "output_price_micros": float64(0)})
	settleDimensions(ctx, QuotaConfig{BudgetPolicy: moneyPolicy, redisClient: c}, 2000000, 2000000, 0)
	if len(c.requests) != 1 || c.requests[0]["action"] != "block" || c.requests[0]["subject"] != "original-user" {
		t.Fatal(c.requests)
	}
	if publicAction("GET", "block") || publicAction("POST", "block") {
		t.Fatal("internal block exposed")
	}
}
func TestMoneyTotalTokenRange(t *testing.T) {
	if _, err := moneyCost(6000000000000, 6000000000000, 1, 1); err == nil {
		t.Fatal("combined usage exceeds safe range")
	}
}
