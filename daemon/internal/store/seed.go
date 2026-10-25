package store

import (
	"fmt"
	"math"
	"time"

	"ajsaccountd/internal/model"
)

// Seed constants mirror dashboard/src/lib/mock.ts.
const (
	seedRequests = 40
	seedAudit    = 20
)

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func ptrString(v string) *string { return &v }

func ptrInt64(v int64) *int64 { return &v }

// mulberry32 reproduces the dashboard mock PRNG bit-for-bit so seeded request
// rows match mock mode.
func mulberry32(seed uint32) func() float64 {
	t := seed
	return func() float64 {
		t += 0x6d2b79f5
		r := t
		r = (r ^ (r >> 15)) * (r | 1)
		r ^= r + (r^(r>>7))*(r|61)
		return float64(r^(r>>14)) / 4294967296.0
	}
}

func pick[T any](rand func() float64, values []T) T {
	return values[int(rand()*float64(len(values)))]
}

func jsRound(v float64) int { return int(math.Round(v)) }

func buildAccounts(now time.Time) []model.Account {
	const day = 24 * time.Hour
	const minute = time.Minute
	const hour = 60 * time.Minute
	return []model.Account{
		{
			ID: "acc-01", Label: "Northwind Ops", Username: "demo-northwind",
			Platform: model.PlatformAndroid, UseCount: 2, State: model.AccountActive,
			Concurrency: 200, Regions: []string{"cn-zj", "cn-sh", "hk"},
			NodeCount: 3, LiveNodeCount: 2, UsageBytes: 82_400_000_000,
			ExpiresAt: iso(now.Add(18 * day)), LastLoginAt: iso(now.Add(-12 * minute)),
			CreatedAt: iso(now.Add(-96 * day)),
		},
		{
			ID: "acc-02", Label: "Field Lab", Username: "demo-fieldlab",
			Platform: model.PlatformIOS, UseCount: 2, State: model.AccountActive,
			Concurrency: 80, Regions: []string{"jp-tyo", "sg-01"},
			NodeCount: 2, LiveNodeCount: 2, UsageBytes: 31_700_000_000,
			ExpiresAt: iso(now.Add(41 * day)), LastLoginAt: iso(now.Add(-3 * hour)),
			CreatedAt: iso(now.Add(-74 * day)),
		},
		{
			ID: "acc-03", Label: "Edge APAC", Username: "demo-edgeapac",
			Platform: model.PlatformLinux, UseCount: 2, State: model.AccountActive,
			Concurrency: 120, Regions: []string{"hk", "tw-tpe", "kr-sel"},
			NodeCount: 3, LiveNodeCount: 2, UsageBytes: 57_100_000_000,
			ExpiresAt: iso(now.Add(9 * day)), LastLoginAt: iso(now.Add(-22 * minute)),
			CreatedAt: iso(now.Add(-51 * day)),
		},
		{
			ID: "acc-04", Label: "Partner EU", Username: "demo-partnereu",
			Platform: model.PlatformWindows, UseCount: 2, State: model.AccountActive,
			Concurrency: 40, Regions: []string{"de-fra", "uk-lon"},
			NodeCount: 2, LiveNodeCount: 2, UsageBytes: 12_900_000_000,
			ExpiresAt: iso(now.Add(64 * day)), LastLoginAt: iso(now.Add(-1 * hour)),
			CreatedAt: iso(now.Add(-33 * day)),
		},
		{
			ID: "acc-05", Label: "QA Sandbox", Username: "demo-qasandbox",
			Platform: model.PlatformAndroid, State: model.AccountPaused,
			Concurrency: 20, Regions: []string{"us-lax", "ca-yyz"},
			NodeCount: 1, LiveNodeCount: 0, UsageBytes: 4_200_000_000,
			ExpiresAt: iso(now.Add(120 * day)), LastLoginAt: iso(now.Add(-2 * day)),
			CreatedAt: iso(now.Add(-21 * day)),
		},
		{
			ID: "acc-06", Label: "Legacy HK", Username: "demo-legacyhk",
			Platform: model.PlatformAndroid, State: model.AccountAuthFailed,
			Concurrency: 10, Regions: []string{"hk"},
			NodeCount: 1, LiveNodeCount: 0, UsageBytes: 900_000_000,
			ExpiresAt: iso(now.Add(-2 * day)), LastLoginAt: iso(now.Add(-5 * day)),
			CreatedAt: iso(now.Add(-140 * day)),
		},
	}
}

func buildNodes(now time.Time) []model.ProxyNode {
	seed := []model.ProxyNode{
		{ID: "node-01", AccountID: "acc-01", AccountLabel: "Northwind Ops", Label: "cn-zj-01", Region: "cn-zj", Country: "CN", City: "Hangzhou", State: model.NodeLive, LatencyMs: 42, SuccessRate: 99.4, InUse: 3, Capacity: 20, EgressIP: "203.0.113.11", Protocol: model.ProtocolSOCKS5, GatePort: 13000},
		{ID: "node-02", AccountID: "acc-01", AccountLabel: "Northwind Ops", Label: "cn-sh-01", Region: "cn-sh", Country: "CN", City: "Shanghai", State: model.NodeLive, LatencyMs: 51, SuccessRate: 98.7, InUse: 6, Capacity: 20, EgressIP: "203.0.113.12", Protocol: model.ProtocolSOCKS5, GatePort: 13001},
		{ID: "node-03", AccountID: "acc-01", AccountLabel: "Northwind Ops", Label: "hk-01", Region: "hk", Country: "HK", City: "Hong Kong", State: model.NodeReady, LatencyMs: 78, SuccessRate: 97.1, InUse: 0, Capacity: 10, EgressIP: "203.0.113.13", Protocol: model.ProtocolSOCKS5, GatePort: 13002},
		{ID: "node-04", AccountID: "acc-02", AccountLabel: "Field Lab", Label: "jp-tyo-01", Region: "jp-tyo", Country: "JP", City: "Tokyo", State: model.NodeLive, LatencyMs: 96, SuccessRate: 99.1, InUse: 2, Capacity: 8, EgressIP: "198.51.100.21", Protocol: model.ProtocolSOCKS5, GatePort: 13003},
		{ID: "node-05", AccountID: "acc-02", AccountLabel: "Field Lab", Label: "sg-01", Region: "sg-01", Country: "SG", City: "Singapore", State: model.NodeLive, LatencyMs: 63, SuccessRate: 99.8, InUse: 1, Capacity: 8, EgressIP: "198.51.100.22", Protocol: model.ProtocolSOCKS5, GatePort: 13004},
		{ID: "node-06", AccountID: "acc-03", AccountLabel: "Edge APAC", Label: "hk-02", Region: "hk", Country: "HK", City: "Hong Kong", State: model.NodeLive, LatencyMs: 71, SuccessRate: 98.2, InUse: 7, Capacity: 12, EgressIP: "198.51.100.31", Protocol: model.ProtocolSOCKS5, GatePort: 13005},
		{ID: "node-07", AccountID: "acc-03", AccountLabel: "Edge APAC", Label: "tw-tpe-01", Region: "tw-tpe", Country: "TW", City: "Taipei", State: model.NodeLive, LatencyMs: 88, SuccessRate: 96.4, InUse: 4, Capacity: 12, EgressIP: "198.51.100.32", Protocol: model.ProtocolSOCKS5, GatePort: 13006},
		{ID: "node-08", AccountID: "acc-03", AccountLabel: "Edge APAC", Label: "kr-sel-01", Region: "kr-sel", Country: "KR", City: "Seoul", State: model.NodeDraining, LatencyMs: 104, SuccessRate: 94.9, InUse: 2, Capacity: 12, EgressIP: "198.51.100.33", Protocol: model.ProtocolSOCKS5, GatePort: 13007},
		{ID: "node-09", AccountID: "acc-04", AccountLabel: "Partner EU", Label: "de-fra-01", Region: "de-fra", Country: "DE", City: "Frankfurt", State: model.NodeLive, LatencyMs: 132, SuccessRate: 99.0, InUse: 2, Capacity: 4, EgressIP: "192.0.2.41", Protocol: model.ProtocolHTTP, GatePort: 13008},
		{ID: "node-10", AccountID: "acc-04", AccountLabel: "Partner EU", Label: "uk-lon-01", Region: "uk-lon", Country: "GB", City: "London", State: model.NodeLive, LatencyMs: 141, SuccessRate: 98.5, InUse: 1, Capacity: 4, EgressIP: "192.0.2.42", Protocol: model.ProtocolHTTP, GatePort: 13009},
		{ID: "node-11", AccountID: "acc-05", AccountLabel: "QA Sandbox", Label: "us-lax-01", Region: "us-lax", Country: "US", City: "Los Angeles", State: model.NodeReady, LatencyMs: 168, SuccessRate: 95.2, InUse: 0, Capacity: 4, EgressIP: "192.0.2.51", Protocol: model.ProtocolSOCKS5, GatePort: 13010},
		{ID: "node-12", AccountID: "acc-06", AccountLabel: "Legacy HK", Label: "hk-03", Region: "hk", Country: "HK", City: "Hong Kong", State: model.NodeDown, LatencyMs: 0, SuccessRate: 0, InUse: 0, Capacity: 2, EgressIP: "192.0.2.61", Protocol: model.ProtocolSOCKS5, GatePort: 13011},
	}
	for i := range seed {
		seed[i].LastProbeAt = iso(now.Add(-time.Duration(i+1) * 47 * time.Second))
	}
	return seed
}

func buildLeases(now time.Time) []model.Lease {
	type row struct {
		lease      model.Lease
		createdAgo time.Duration
		expiresIn  time.Duration
	}
	const minute = time.Minute
	rows := []row{
		{model.Lease{ID: "lease-01", UserID: "u-01", UserName: "ada.ops", AccountID: "acc-01", AccountLabel: "Northwind Ops", NodeID: "node-01", NodeLabel: "cn-zj-01", Region: "cn-zj", EgressIP: "203.0.113.11", Protocol: model.ProtocolSOCKS5, GatePort: 13000, TTLSeconds: 8 * 3600, Requests: 18_432, Bytes: 4_200_000_000, Sticky: true}, 168 * minute, 312 * minute},
		{model.Lease{ID: "lease-02", UserID: "u-01", UserName: "ada.ops", AccountID: "acc-03", AccountLabel: "Edge APAC", NodeID: "node-06", NodeLabel: "hk-02", Region: "hk", EgressIP: "198.51.100.31", Protocol: model.ProtocolSOCKS5, GatePort: 13005, TTLSeconds: 8 * 3600, Requests: 6_204, Bytes: 1_100_000_000, Sticky: true}, 66 * minute, 414 * minute},
		{model.Lease{ID: "lease-03", UserID: "u-02", UserName: "build-bot", AccountID: "acc-02", AccountLabel: "Field Lab", NodeID: "node-04", NodeLabel: "jp-tyo-01", Region: "jp-tyo", EgressIP: "198.51.100.21", Protocol: model.ProtocolSOCKS5, GatePort: 13003, TTLSeconds: 3600, Requests: 3_912, Bytes: 780_000_000, Sticky: false}, 24 * minute, 36 * minute},
		{model.Lease{ID: "lease-04", UserID: "u-02", UserName: "build-bot", AccountID: "acc-02", AccountLabel: "Field Lab", NodeID: "node-05", NodeLabel: "sg-01", Region: "sg-01", EgressIP: "198.51.100.22", Protocol: model.ProtocolSOCKS5, GatePort: 13004, TTLSeconds: 3600, Requests: 1_477, Bytes: 310_000_000, Sticky: false}, 12 * minute, 48 * minute},
		{model.Lease{ID: "lease-05", UserID: "u-03", UserName: "qa.pipeline", AccountID: "acc-03", AccountLabel: "Edge APAC", NodeID: "node-07", NodeLabel: "tw-tpe-01", Region: "tw-tpe", EgressIP: "198.51.100.32", Protocol: model.ProtocolSOCKS5, GatePort: 13006, TTLSeconds: 24 * 3600, Requests: 22_018, Bytes: 6_800_000_000, Sticky: true}, 450 * minute, 990 * minute},
		{model.Lease{ID: "lease-06", UserID: "u-03", UserName: "qa.pipeline", AccountID: "acc-01", AccountLabel: "Northwind Ops", NodeID: "node-02", NodeLabel: "cn-sh-01", Region: "cn-sh", EgressIP: "203.0.113.12", Protocol: model.ProtocolSOCKS5, GatePort: 13001, TTLSeconds: 24 * 3600, Requests: 9_301, Bytes: 2_450_000_000, Sticky: false}, 192 * minute, 1248 * minute},
		{model.Lease{ID: "lease-07", UserID: "u-04", UserName: "field.lab", AccountID: "acc-04", AccountLabel: "Partner EU", NodeID: "node-09", NodeLabel: "de-fra-01", Region: "de-fra", EgressIP: "192.0.2.41", Protocol: model.ProtocolHTTP, GatePort: 13008, TTLSeconds: 8 * 3600, Requests: 2_884, Bytes: 640_000_000, Sticky: true}, 324 * minute, 156 * minute},
		{model.Lease{ID: "lease-08", UserID: "u-04", UserName: "field.lab", AccountID: "acc-04", AccountLabel: "Partner EU", NodeID: "node-10", NodeLabel: "uk-lon-01", Region: "uk-lon", EgressIP: "192.0.2.42", Protocol: model.ProtocolHTTP, GatePort: 13009, TTLSeconds: 8 * 3600, Requests: 1_190, Bytes: 205_000_000, Sticky: false}, 114 * minute, 366 * minute},
	}
	leases := make([]model.Lease, 0, len(rows))
	for _, r := range rows {
		lease := r.lease
		lease.CreatedAt = iso(now.Add(-r.createdAgo))
		lease.ExpiresAt = iso(now.Add(r.expiresIn))
		leases = append(leases, lease)
	}
	return leases
}

func buildUsers(now time.Time) []model.PanelUser {
	const day = 24 * time.Hour
	return []model.PanelUser{
		{ID: "u-01", Name: "ada.ops", Platform: "ajs-ada", StickyTTL: "8h", QuotaBytesMonth: ptrInt64(200_000_000_000), UsedBytes: 64_000_000_000, Enabled: true, Allowlist: []string{"127.0.0.1/32", "10.20.0.0/16"}, CreatedAt: iso(now.Add(-88 * day))},
		{ID: "u-02", Name: "build-bot", Platform: "ajs-build", StickyTTL: "1h", QuotaBytesMonth: ptrInt64(50_000_000_000), UsedBytes: 12_400_000_000, Enabled: true, Allowlist: []string{"127.0.0.1/32"}, CreatedAt: iso(now.Add(-61 * day))},
		{ID: "u-03", Name: "qa.pipeline", Platform: "ajs-qa", StickyTTL: "24h", QuotaBytesMonth: nil, UsedBytes: 88_000_000_000, Enabled: true, Allowlist: []string{"127.0.0.1/32", "10.30.4.0/24"}, CreatedAt: iso(now.Add(-40 * day))},
		{ID: "u-04", Name: "field.lab", Platform: "ajs-field", StickyTTL: "8h", QuotaBytesMonth: ptrInt64(500_000_000_000), UsedBytes: 410_000_000_000, Enabled: true, Allowlist: []string{"127.0.0.1/32", "192.168.40.0/24"}, CreatedAt: iso(now.Add(-27 * day))},
	}
}

func buildKeys(now time.Time) []model.APIKey {
	const day = 24 * time.Hour
	const hour = time.Hour
	const minute = time.Minute
	return []model.APIKey{
		{ID: "key-01", UserID: "u-01", UserName: "ada.ops", Label: "CI runner", Prefix: "ajs_live_01", CreatedAt: iso(now.Add(-30 * day)), LastUsedAt: ptrString(iso(now.Add(-2 * hour)))},
		{ID: "key-02", UserID: "u-01", UserName: "ada.ops", Label: "Laptop", Prefix: "ajs_live_02", CreatedAt: iso(now.Add(-22 * day)), LastUsedAt: ptrString(iso(now.Add(-18 * minute)))},
		{ID: "key-03", UserID: "u-02", UserName: "build-bot", Label: "Build farm", Prefix: "ajs_live_03", CreatedAt: iso(now.Add(-19 * day)), LastUsedAt: ptrString(iso(now.Add(-6 * minute)))},
		{ID: "key-04", UserID: "u-03", UserName: "qa.pipeline", Label: "QA harness", Prefix: "ajs_live_04", CreatedAt: iso(now.Add(-14 * day)), LastUsedAt: ptrString(iso(now.Add(-44 * minute)))},
		{ID: "key-05", UserID: "u-04", UserName: "field.lab", Label: "Field device", Prefix: "ajs_live_05", CreatedAt: iso(now.Add(-9 * day)), LastUsedAt: ptrString(iso(now.Add(-3 * hour)))},
		{ID: "key-06", UserID: "u-04", UserName: "field.lab", Label: "Old tablet", Prefix: "ajs_live_06", CreatedAt: iso(now.Add(-80 * day)), LastUsedAt: ptrString(iso(now.Add(-12 * day))), RevokedAt: ptrString(iso(now.Add(-3 * day)))},
	}
}

func buildRequests(now time.Time, users []model.PanelUser, nodes []model.ProxyNode) []model.RequestLog {
	rand := mulberry32(20261023)
	targets := []string{
		"api.example.com:443",
		"cdn.example.net:443",
		"auth.example.org:443",
		"telemetry.example.dev:443",
		"registry.example.io:443",
		"storage.example.net:443",
		"docs.example.com:443",
	}
	methods := []string{"CONNECT", "CONNECT", "CONNECT", "GET", "POST", "HEAD"}
	statuses := []int{200, 200, 200, 204, 200, 407, 502, 200}
	liveNodes := make([]model.ProxyNode, 0, len(nodes))
	for _, node := range nodes {
		if node.State == model.NodeLive || node.State == model.NodeReady {
			liveNodes = append(liveNodes, node)
		}
	}
	requests := make([]model.RequestLog, 0, seedRequests)
	for index := range seedRequests {
		user := users[index%len(users)]
		node := liveNodes[index%len(liveNodes)]
		status := pick(rand, statuses)
		var latency int
		switch status {
		case 502:
			latency = 900 + jsRound(rand()*1400)
		case 407:
			latency = 9 + jsRound(rand()*20)
		default:
			latency = 38 + jsRound(rand()*260)
		}
		ts := now.Add(-time.Duration(index*31)*time.Minute - time.Duration(jsRound(rand()*9*60000))*time.Millisecond)
		requests = append(requests, model.RequestLog{
			ID:           fmt.Sprintf("req-%03d", index+1),
			TS:           iso(ts),
			UserID:       user.ID,
			UserName:     user.Name,
			AccountLabel: node.AccountLabel,
			NodeLabel:    node.Label,
			Region:       node.Region,
			Method:       pick(rand, methods),
			Target:       pick(rand, targets),
			Status:       status,
			Bytes:        1200 + int64(jsRound(rand()*8_500_000)),
			LatencyMs:    latency,
			Source:       model.SourceProxy,
		})
	}
	return requests
}

func buildAudit(now time.Time) []model.AuditEvent {
	rows := []struct {
		severity model.Severity
		actor    string
		action   string
		target   string
		detail   string
	}{
		{model.SeverityInfo, "operator", "account.login", "Northwind Ops", "Session refreshed · token valid for 10m"},
		{model.SeverityInfo, "ajs-accountd", "subscription.publish", "ajs-live", "8 nodes rendered · hash 7c1e…9a2"},
		{model.SeverityWarn, "ajs-accountd", "node.drain", "kr-sel-01", "Drain requested · 2 in-flight flows"},
		{model.SeverityInfo, "operator", "lease.rotate", "ada.ops", "203.0.113.11 → 203.0.113.24"},
		{model.SeverityError, "ajs-accountd", "account.auth_failed", "Legacy HK", "WrongPassword · 3 attempts in 15m"},
		{model.SeverityInfo, "operator", "key.create", "build-bot", "Key prefix ajs_live_03"},
		{model.SeverityInfo, "system", "health.probe", "cn-zj-01", "Canary ok · 42 ms · egress verified"},
		{model.SeverityWarn, "ajs-accountd", "node.probe", "us-lax-01", "Probe skipped · node is READY"},
		{model.SeverityInfo, "operator", "settings.update", "health interval", "60s → 90s"},
		{model.SeverityInfo, "operator", "account.test", "Field Lab", "Login round-trip ok · 812 ms"},
		{model.SeverityInfo, "system", "health.probe", "jp-tyo-01", "Canary ok · 96 ms · egress verified"},
		{model.SeverityInfo, "operator", "account.pause", "QA Sandbox", "All nodes unpublished"},
		{model.SeverityInfo, "operator", "lease.release", "qa.pipeline", "Lease on cn-sh-01 released"},
		{model.SeverityWarn, "ajs-accountd", "publish.coalesced", "ajs-live", "PATCH deferred · 5s window"},
		{model.SeverityInfo, "system", "health.probe", "de-fra-01", "Canary ok · 132 ms · egress verified"},
		{model.SeverityError, "ajs-accountd", "gate.timeout", "hk-03", "Gate did not answer within 30s"},
		{model.SeverityInfo, "operator", "key.revoke", "field.lab", "Key prefix ajs_live_06 revoked"},
		{model.SeverityInfo, "operator", "node.undrain", "tw-tpe-01", "Node returned to service"},
		{model.SeverityInfo, "system", "usage.rollup", "2026-10-22", "40 requests · 18.2 GB egress"},
		{model.SeverityInfo, "operator", "account.resume", "Edge APAC", "Concurrency raised to 120"},
	}
	events := make([]model.AuditEvent, 0, len(rows))
	for index, row := range rows {
		events = append(events, model.AuditEvent{
			ID:       fmt.Sprintf("audit-%03d", index+1),
			TS:       iso(now.Add(-time.Duration(index*23)*time.Minute - time.Duration(index%4)*3*time.Minute)),
			Severity: row.severity,
			Actor:    row.actor,
			Action:   row.action,
			Target:   row.target,
			Detail:   row.detail,
		})
	}
	return events
}

func defaultSettings() model.PanelSettings {
	return model.PanelSettings{
		Transport:              model.TransportHTTPUpgrade,
		HealthIntervalSec:      60,
		LeaseTTL:               "8h",
		SubscriptionRefreshSec: 30,
		Theme:                  model.ThemeDark,
	}
}

// buildMetrics renders one metrics window. The store has no traffic meter, so
// the series and totals stay empty/zero and TelemetryAvailable is false;
// inventing a curve here would present mock traffic as live. Account health is
// real: it comes from each account's state and live node count.
func buildMetrics(window model.MetricRange, now time.Time, accounts []model.Account) model.Metrics {
	health := make([]model.AccountHealth, 0, len(accounts))
	for _, account := range accounts {
		health = append(health, model.AccountHealth{
			AccountID: account.ID,
			Label:     account.Label,
			Score:     accountHealthScore(account),
			State:     account.State,
		})
	}
	return model.Metrics{
		Range:              window,
		Series:             []model.TrafficPoint{},
		Totals:             model.MetricsTotals{},
		AccountHealth:      health,
		TelemetryAvailable: false,
		GeneratedAt:        iso(now),
	}
}

// accountHealthScore maps an account's state and live node count to the
// dashboard's 0-100 health score. An active account scores 100 when at least
// one node is LIVE and 70 when the session is up but no node is serving. A
// paused account scores 40; auth_failed and expired accounts cannot serve and
// score 0.
func accountHealthScore(account model.Account) int {
	switch account.State {
	case model.AccountActive:
		if account.LiveNodeCount > 0 {
			return 100
		}
		return 70
	case model.AccountPaused:
		return 40
	default:
		return 0
	}
}
