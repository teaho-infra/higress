package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/resp"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed ledger.lua
var ledgerScript string

const defaultTiers = `{"L1":{"quota":500000000,"models":[]},"L2":{"quota":1500000000,"models":[]}}`

func periodAt(t time.Time) (string, string, string) {
	z := time.FixedZone("Asia/Shanghai", 8*3600)
	anchor := time.Date(2026, 1, 5, 0, 0, 0, 0, z)
	n := int64(t.Sub(anchor) / time.Second)
	w := int64(14 * 86400)
	q := n / w
	if n < 0 && n%w != 0 {
		q--
	}
	s := anchor.Add(time.Duration(q*w) * time.Second)
	return s.Format("2006-01-02"), s.Format(time.RFC3339), s.Add(14 * 24 * time.Hour).Format(time.RFC3339)
}
func validAmount(s string) bool {
	n, e := strconv.ParseInt(s, 10, 64)
	return e == nil && n >= -9999999999999 && n <= 9999999999999
}
func reply(status uint32, v any) {
	b, _ := json.Marshal(v)
	proxywasm.SendHttpResponse(status, [][2]string{{"content-type", "application/json"}}, b, -1)
}
func problem(status uint32, code, msg string) types.Action {
	reply(status, map[string]any{"ok": false, "error": "ai-quota-walmart." + code, "message": msg})
	return types.ActionPause
}
func request(action string) map[string]any {
	p, s, e := periodAt(time.Now())
	return map[string]any{"action": action, "period": p, "period_start": s, "period_end": e, "occurred_at": time.Now().UTC().Format(time.RFC3339), "period_start_unix": parseUnix(s), "period_end_unix": parseUnix(e)}
}
func eval(c QuotaConfig, r map[string]any, cb func(map[string]any)) {
	b, _ := json.Marshal(r)
	storageError := func(message string) {
		if r["action"] == "settle" || r["action"] == "block" {
			log.Errorf("Quota settlement failed: %s", message)
		} else {
			problem(503, "redis_unavailable", message)
		}
	}
	script := ledgerScript
	if c.BudgetPolicy == moneyPolicy {
		script = moneyScript
	}
	err := c.redisClient.Eval(script, 1, []interface{}{c.RedisKeyPrefix}, []interface{}{string(b), c.Tiers, c.Prices}, func(v resp.Value) {
		if v.Error() != nil {
			storageError("Quota storage unavailable")
			return
		}
		var out map[string]any
		if json.Unmarshal([]byte(v.String()), &out) != nil {
			storageError("Invalid quota storage response")
			return
		}
		cb(out)
	})
	if err != nil {
		storageError("Quota storage unavailable")
	}
}
func output(out map[string]any) {
	normalizeModelArrays(out)
	for _, k := range []string{"accounts", "records"} {
		if m, ok := out[k].(map[string]any); ok && len(m) == 0 {
			out[k] = []any{}
		}
	}
	status := uint32(200)
	if n, ok := out["status"].(float64); ok {
		status = uint32(n)
		delete(out, "status")
	}
	reply(status, out)
}
func dimensionalHeaders(ctx wrapper.HttpContext, c QuotaConfig) types.Action {
	ctx.DisableReroute()
	u, e := url.Parse(ctx.Path())
	if e != nil {
		return problem(400, "invalid_request", "Invalid path")
	}
	consumer, _ := proxywasm.GetHttpRequestHeader("x-mse-consumer")
	admin := "/v1/chat/completions" + c.AdminPath
	if u.Path == admin || strings.HasPrefix(u.Path, admin+"/") {
		if consumer != c.AdminConsumer {
			return problem(403, "unauthorized", "Administrator required")
		}
		a := strings.Trim(strings.TrimPrefix(u.Path, admin), "/")
		if a == "" {
			a = "query"
		}
		ctx.SetContext("wmAdmin", a)
		ctx.SetContext("consumer", consumer)
		method, _ := proxywasm.GetHttpRequestHeader(":method")
		if (a == "prices" || a == "cycle" || a == "reset") && c.BudgetPolicy != moneyPolicy {
			return problem(405, "method_not_allowed", "Money policy required")
		}
		if !publicAction(method, a) {
			return problem(405, "method_not_allowed", "Unsupported method or administrative action")
		}
		if method == "POST" {
			ctx.BufferRequestBody()
			return types.HeaderStopIteration
		}
		if method != "GET" {
			return problem(405, "method_not_allowed", "GET or POST required")
		}
		if a != "meta" && a != "accounts" && a != "audit" && a != "query" && a != "usage" && a != "prices" {
			return problem(405, "method_not_allowed", "POST required")
		}
		data := map[string]any{}
		for k, v := range u.Query() {
			if len(v) != 1 {
				return problem(400, "invalid_request", "Repeated parameter")
			}
			data[k] = v[0]
		}
		if a == "prices" && len(data) != 0 {
			return problem(400, "invalid_request", "Prices GET takes no fields")
		}
		if err := validateAdminFields(a, data); err != nil {
			return problem(400, "invalid_request", err.Error())
		}
		r := request(a)
		for k, v := range u.Query() {
			r[k] = v[0]
		}
		return adminRun(c, r, consumer)
	}
	enabled := false
	for _, s := range c.EnablePathSuffixes {
		if strings.HasSuffix(u.Path, s) {
			enabled = true
		}
	}
	if !enabled {
		ctx.DontReadRequestBody()
		return types.ActionContinue
	}
	if consumer == "" {
		return problem(401, "no_key", "Key authentication required")
	}
	ctx.SetContext("consumer", consumer)
	ctx.SetContext("chatMode", ChatModeCompletion)
	ctx.BufferRequestBody()
	return types.HeaderStopIteration
}
func dimensionalBody(ctx wrapper.HttpContext, c QuotaConfig, b []byte) types.Action {
	if a, ok := ctx.GetContext("wmAdmin").(string); ok {
		r := request(a)
		delete(r, "period")
		var data map[string]any
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		decodedErr := dec.Decode(&data)
		if decodedErr == nil {
			var extra any
			if dec.Decode(&extra) != io.EOF {
				return problem(400, "invalid_request", "One JSON object required")
			}
		}
		if decodedErr != nil {
			v, e := url.ParseQuery(string(b))
			if e != nil {
				return problem(400, "invalid_request", "Invalid JSON or form")
			}
			data = map[string]any{}
			for k, x := range v {
				if len(x) != 1 {
					return problem(400, "invalid_request", "Repeated parameter")
				}
				data[k] = x[0]
			}
		}
		if err := validateAdminFields(a, data); err != nil {
			return problem(400, "invalid_request", err.Error())
		}
		if a == "prices" && data["prices"] == nil {
			return problem(400, "invalid_prices", "prices object required")
		}
		for k, v := range data {
			r[k] = v
		}
		r["action"] = a
		consumer, _ := ctx.GetContext("consumer").(string)
		return adminRun(c, r, consumer)
	}
	consumer, ok := ctx.GetContext("consumer").(string)
	if !ok {
		return types.ActionContinue
	}
	user, _ := proxywasm.GetHttpRequestHeader("x-codebuddy-user-id")
	if len(user) > 256 {
		return problem(400, "invalid_subject", "User ID too long")
	}
	r := request("check")
	r["app"] = consumer
	r["subject"] = user
	var body map[string]any
	if json.Unmarshal(b, &body) != nil {
		return problem(400, "invalid_request", "JSON body required")
	}
	if !validModel(body["model"]) {
		return problem(400, "invalid_model", "model must be a nonempty string of at most 256 bytes")
	}
	r["model"], _ = body["model"].(string)
	eval(c, r, func(out map[string]any) {
		if out["ok"] != true {
			output(out)
			return
		}
		ctx.SetContext("wmRequest", r)
		ctx.SetContext("wmPayer", out["payer_dimension"])
		ctx.SetContext("wmPayerSubject", out["payer_subject"])
		ctx.SetContext("wmQuote", out["price"])
		proxywasm.ResumeHttpRequest()
	})
	return types.ActionPause
}
func adminRun(c QuotaConfig, r map[string]any, operator string) types.Action {
	a, _ := r["action"].(string)
	if !publicAction("GET", a) && !publicAction("POST", a) {
		return problem(400, "invalid_action", "Unsupported public action")
	}
	r["operator"] = operator
	if a == "accounts" || a == "audit" {
		for k, def := range map[string]int{"offset": 0, "limit": 100} {
			n := def
			if v, ok := r[k]; ok {
				parsed, e := strconv.Atoi(fmt.Sprint(v))
				if e != nil {
					return problem(400, "invalid_request", "Invalid pagination")
				}
				n = parsed
			}
			if n < 0 || (k == "limit" && (n < 1 || n > 200)) {
				return problem(400, "invalid_request", "Pagination out of range")
			}
			r[k] = n
		}
	}
	if a != "meta" && a != "audit" && a != "tiers" && a != "prices" && a != "cycle" && a != "reset" {
		d, _ := r["dimension"].(string)
		if d != "user" && d != "app" && !(c.BudgetPolicy == moneyPolicy && d == "department") {
			return problem(400, "invalid_dimension", "dimension must be user or app")
		}
		if a != "accounts" {
			s, _ := r["subject"].(string)
			if s == "" || len(s) > 256 {
				return problem(400, "invalid_subject", "subject is required, maximum 256 bytes")
			}
		}
	}
	if c.BudgetPolicy == moneyPolicy {
		if (a == "delta" || a == "refresh") && r["dimension"] != "user" {
			return problem(400, "invalid_dimension", "User required")
		}
		if a == "assign" && r["dimension"] == "app" {
			return problem(400, "invalid_dimension", "App is usage only")
		}
	}
	mutation := (a == "prices" && r["prices"] != nil) || a == "assign" || a == "delta" || a == "refresh" || a == "tiers" || a == "cycle" || a == "reset"
	if mutation {
		p, _, _ := periodAt(time.Now())
		if c.BudgetPolicy != moneyPolicy && r["period"] != p {
			return problem(409, "period_conflict", "Current period required")
		}
		ticket, _ := r["ticket_id"].(string)
		if ticket == "" || len(ticket) > 128 {
			return problem(400, "invalid_ticket", "ticket_id required, maximum 128 bytes")
		}
		if a == "assign" {
			t, _ := r["tier"].(string)
			if t != "L1" && t != "L2" && !(c.BudgetPolicy == moneyPolicy && a == "assign" && r["dimension"] == "user" && t == "inherit") {
				return problem(400, "invalid_tier", "Unknown tier")
			}
		}
		if a == "delta" {
			s := fmt.Sprint(r["value"])
			if !validAmount(s) {
				return problem(400, "invalid_amount", "Integer outside safe range")
			}
			r["value"], _ = strconv.ParseInt(s, 10, 64)
		}
		if a == "cycle" {
			days, err := strconv.Atoi(fmt.Sprint(r["days"]))
			if err != nil || days < 1 || days > 365 {
				return problem(400, "invalid_cycle", "days must be an integer from 1 to 365")
			}
			r["days"] = days
		}
		if a == "reset" {
			startText, hasStart := r["start_date"].(string)
			endText, hasEnd := r["end_date"].(string)
			if hasStart != hasEnd || (hasStart && (startText == "" || endText == "")) {
				return problem(400, "invalid_cycle", "start_date and end_date must be supplied together")
			}
			if hasStart {
				zone := time.FixedZone("Asia/Shanghai", 8*3600)
				start, startErr := time.ParseInLocation("2006-01-02", startText, zone)
				end, endErr := time.ParseInLocation("2006-01-02", endText, zone)
				if startErr != nil || endErr != nil || end.Before(start) || end.Sub(start) >= 365*24*time.Hour {
					return problem(400, "invalid_cycle", "Reset date range must last 1 to 365 days")
				}
				endExclusive := end.AddDate(0, 0, 1)
				now := time.Now()
				if now.Before(start) || !now.Before(endExclusive) {
					return problem(400, "invalid_cycle", "Reset date range must include today in Asia/Shanghai")
				}
				r["start_unix"] = start.Unix()
				r["end_unix"] = endExclusive.Unix()
			}
		}
		if a == "tiers" {
			v := r["tiers"]
			if s, ok := v.(string); ok {
				json.Unmarshal([]byte(s), &v)
			}
			b, _ := json.Marshal(v)
			if err := validateTiers(string(b)); err != nil {
				return problem(400, "invalid_tiers", err.Error())
			}
			var tiers map[string]struct {
				Quota  json.Number `json:"quota"`
				Models []string    `json:"models"`
			}
			if json.Unmarshal(b, &tiers) != nil || len(tiers) != 2 {
				return problem(400, "invalid_tiers", "Two tiers required")
			}
			for _, k := range []string{"L1", "L2"} {
				t, ok := tiers[k]
				if !ok || !validAmount(string(t.Quota)) || strings.HasPrefix(string(t.Quota), "-") {
					return problem(400, "invalid_tiers", "Invalid tier quota")
				}
			}
			r["tiers"] = v
		}
		if a == "prices" {
			b, _ := json.Marshal(r["prices"])
			if c.BudgetPolicy != moneyPolicy || validatePrices(string(b)) != nil {
				return problem(400, "invalid_prices", "Invalid model prices")
			}
		}
		canonical := map[string]any{}
		for _, k := range []string{"action", "period", "dimension", "subject", "app", "tier", "value", "tiers", "department", "prices", "days", "start_date", "end_date"} {
			if v, ok := r[k]; ok {
				canonical[k] = v
			}
		}
		b, _ := json.Marshal(canonical)
		h := sha256.Sum256(b)
		r["fingerprint"] = hex.EncodeToString(h[:])
	}
	eval(c, r, output)
	return types.ActionPause
}
func settleDimensions(ctx wrapper.HttpContext, c QuotaConfig, n int64, tokens ...int64) {
	r, ok := ctx.GetContext("wmRequest").(map[string]any)
	if !ok || (n <= 0 && c.BudgetPolicy != moneyPolicy) {
		return
	}
	ctx.SetContext(ctxKeyQuotaDeducted, true)
	r["action"] = "settle"
	r["value"] = n
	r["payer_dimension"] = ctx.GetContext("wmPayer")
	if c.BudgetPolicy == moneyPolicy {
		quote, ok := ctx.GetContext("wmQuote").(map[string]any)
		if !ok || len(tokens) != 2 {
			return
		}
		ip, iok := quote["input_price_micros"].(float64)
		op, ook := quote["output_price_micros"].(float64)
		if !iok || !ook {
			return
		}
		cost, err := moneyCost(tokens[0], tokens[1], int64(ip), int64(op))
		if err != nil {
			log.Errorf("Money settlement calculation failed: %v", err)
			r["action"] = "block"
			r["subject"] = ctx.GetContext("wmPayerSubject")
			eval(c, r, func(map[string]any) {})
			return
		}
		r["cost_micros"] = cost
		r["input_tokens"] = tokens[0]
		r["output_tokens"] = tokens[1]
		r["subject"] = ctx.GetContext("wmPayerSubject")
	}
	r["ticket_id"] = uuid.NewString()
	eval(c, r, func(out map[string]any) {
		if out["ok"] != true {
			log.Errorf("Quota settlement failed: %v", out)
		}
	})
}

func parseUnix(s string) int64 { t, _ := time.Parse(time.RFC3339, s); return t.Unix() }
func validateDimensions(d gjson.Result) error {
	if !d.IsArray() || len(d.Array()) != 2 {
		return fmt.Errorf("dimensions must contain user and app")
	}
	seen := map[string]bool{}
	for _, v := range d.Array() {
		name := v.String()
		if v.IsObject() {
			name = v.Get("name").String()
			if name == "user" && v.Get("require").Bool() {
				return fmt.Errorf("user require:true is unsupported")
			}
			if name == "app" && v.Get("require").Exists() && !v.Get("require").Bool() {
				return fmt.Errorf("app require:false is unsupported")
			}
			if v.Get("enabled").Exists() && !v.Get("enabled").Bool() {
				return fmt.Errorf("both dimensions must be enabled")
			}
			if name == "user" && (v.Get("subject_source").String() != "request_header" || v.Get("subject_key").String() != "x-codebuddy-user-id") {
				return fmt.Errorf("user requires trusted x-codebuddy-user-id header")
			}
			if name == "app" && v.Get("subject_source").String() != "consumer" {
				return fmt.Errorf("app requires authenticated consumer")
			}
		}
		if name != "app" && name != "user" || seen[name] {
			return fmt.Errorf("dimensions must contain user and app once")
		}
		seen[name] = true
	}
	return nil
}
func validateTiers(raw string) error {
	for _, k := range []string{"L1", "L2"} {
		models := gjson.Get(raw, k+".models")
		if models.Exists() && !models.IsArray() {
			return fmt.Errorf("models must be an array when provided")
		}
	}
	var tiers map[string]struct {
		Quota  json.Number `json:"quota"`
		Models []string    `json:"models"`
	}
	if json.Unmarshal([]byte(raw), &tiers) != nil || len(tiers) != 2 {
		return fmt.Errorf("exactly two tiers required")
	}
	var previous int64
	for _, k := range []string{"L1", "L2"} {
		t, ok := tiers[k]
		if !ok || !validAmount(string(t.Quota)) {
			return fmt.Errorf("invalid tier quota")
		}
		n, _ := strconv.ParseInt(string(t.Quota), 10, 64)
		if n < previous {
			return fmt.Errorf("tier quotas must be nonnegative and ordered")
		}
		previous = n
	}
	return nil
}

func publicAction(method, action string) bool {
	if method == "GET" {
		switch action {
		case "meta", "query", "usage", "accounts", "audit", "prices":
			return true
		}
	}
	if method == "POST" {
		switch action {
		case "assign", "delta", "refresh", "tiers", "prices", "cycle", "reset":
			return true
		}
	}
	return false
}
func validateAdminFields(action string, data map[string]any) error {
	allowed := map[string]bool{}
	fields := map[string][]string{
		"meta": {}, "query": {"dimension", "subject"}, "usage": {"dimension", "subject"},
		"accounts": {"dimension", "offset", "limit"}, "audit": {"offset", "limit"},
		"assign":  {"dimension", "subject", "app", "department", "tier", "ticket_id", "period"},
		"delta":   {"dimension", "subject", "value", "ticket_id", "period"},
		"refresh": {"dimension", "subject", "ticket_id", "period"},
		"tiers":   {"tiers", "ticket_id", "period"},
		"prices":  {"prices", "ticket_id", "period"},
		"cycle":   {"days", "ticket_id", "period"},
		"reset":   {"ticket_id", "period", "start_date", "end_date"},
	}
	list, ok := fields[action]
	if !ok {
		return fmt.Errorf("unsupported public action")
	}
	for _, k := range list {
		allowed[k] = true
	}
	for k, v := range data {
		if !allowed[k] {
			return fmt.Errorf("unknown or reserved field: %s", k)
		}
		if k == "app" || k == "department" || k == "dimension" || k == "subject" || k == "tier" || k == "period" || k == "ticket_id" || k == "start_date" || k == "end_date" {
			a, ok := v.(string)
			if !ok || len(a) > 256 {
				return fmt.Errorf("app must be a string of at most 256 bytes")
			}
		}
	}
	return nil
}

func validModel(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != "" && len(s) <= 256
}

func normalizeModelArrays(v any) {
	switch n := v.(type) {
	case map[string]any:
		for k, x := range n {
			if k == "models" {
				if m, ok := x.(map[string]any); ok && len(m) == 0 {
					n[k] = []any{}
					continue
				}
			}
			normalizeModelArrays(x)
		}
	case []any:
		for _, x := range n {
			normalizeModelArrays(x)
		}
	}
}
