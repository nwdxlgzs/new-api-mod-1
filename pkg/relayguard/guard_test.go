package relayguard

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFullRequestMatchingPreservesParametersAndIsolation(t *testing.T) {
	cfg := operation_setting.DefaultRelayGuardConfig().Cache
	cfg.Stream = true
	first := []byte(`{"model":"gpt-test","temperature":0,"seed":9007199254740992,"messages":[{"role":"user","content":"same prefix and final question"}]}`)
	key, ok := RequestFingerprint(first, "user:1", cfg)
	require.True(t, ok)
	reordered := []byte(`{ "messages": [ {"content":"same prefix and final question", "role":"user"} ], "seed":9007199254740992, "temperature":0,"model":"gpt-test" }`)
	other, ok := RequestFingerprint(reordered, "user:1", cfg)
	require.True(t, ok)
	assert.Equal(t, key, other)
	for _, body := range []string{
		strings.Replace(string(first), "final question", "another question", 1),
		strings.Replace(string(first), `"temperature":0,`, "", 1),
		strings.Replace(string(first), "9007199254740992", "9007199254740993", 1),
		strings.Replace(string(first), `"model":"gpt-test"`, `"model":"another-model"`, 1),
	} {
		candidate, ok := RequestFingerprint([]byte(body), "user:1", cfg)
		require.True(t, ok)
		assert.NotEqual(t, key, candidate)
	}
	other, _ = RequestFingerprint(first, "user:2", cfg)
	assert.NotEqual(t, key, other)
	for _, body := range []string{
		`{"model":"gpt-test","tools":[],"messages":[{"role":"user","content":"hello"}]}`,
		`{"model":"gpt-test","previous_response_id":"resp_1","input":"hello"}`,
		`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"image_url","image_url":"private"}]}]}`,
	} {
		_, ok := RequestFingerprint([]byte(body), "user:1", cfg)
		assert.False(t, ok)
	}
	gemini := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	key, ok = RequestFingerprint(gemini, "gemini-path", cfg, "gemini-test")
	require.True(t, ok)
	other, ok = RequestFingerprint(gemini, "another-model-path", cfg, "gemini-other")
	require.True(t, ok)
	assert.NotEqual(t, key, other)
	_, ok = RequestFingerprint([]byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA"}}]}]}`), "gemini-path", cfg, "gemini-test")
	assert.False(t, ok)
	for _, extra := range []string{
		`"generationConfig":{"responseModalities":["TEXT","IMAGE"]}`,
		`"systemInstruction":{"parts":[{"fileData":{"fileUri":"remote-image"}}]}`,
		`"cached_content":"projects/provider-state"`,
	} {
		body := `{"contents":[{"parts":[{"text":"hello"}]}],` + extra + `}`
		_, ok = RequestFingerprint([]byte(body), "gemini-path", cfg, "gemini-test")
		assert.False(t, ok)
	}
}

func TestPrefixMatchingRequiresExplicitPolicyAndModelAllowlist(t *testing.T) {
	cfg := operation_setting.DefaultRelayGuardConfig().Cache
	cfg.Match = "prefix"
	cfg.PrefixChars = 128
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"` + strings.Repeat("题", 128) + ` first suffix"}]}`
	_, ok := RequestFingerprint([]byte(body), "key:1", cfg)
	assert.False(t, ok)
	cfg.Models = []string{"gpt-*"}
	first, ok := RequestFingerprint([]byte(body), "key:1", cfg)
	require.True(t, ok)
	second, ok := RequestFingerprint([]byte(strings.Replace(body, "first suffix", "different suffix", 1)), "key:1", cfg)
	require.True(t, ok)
	assert.Equal(t, first, second)
	cfg.Match = "exact"
	first, _ = RequestFingerprint([]byte(body), "key:1", cfg)
	second, _ = RequestFingerprint([]byte(strings.Replace(body, "first suffix", "different suffix", 1)), "key:1", cfg)
	assert.NotEqual(t, first, second)
}

func TestSharedStreamReplaysPrefixAndDoesNotCancelRemainingClient(t *testing.T) {
	cache := NewCache()
	cfg := operation_setting.DefaultRelayGuardConfig().Cache
	cfg.Coalesce = true
	_, flight, first, leader := cache.Acquire("same", cfg, time.Now())
	require.True(t, leader)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	flight.SetCancel(cancel)
	flight.Publish(200, http.Header{"Content-Type": []string{"text/event-stream"}}, []byte("data: first\n\n"))
	_, joined, second, leader := cache.Acquire("same", cfg, time.Now())
	require.False(t, leader)
	require.Same(t, flight, joined)
	limitedConfig := cfg
	limitedConfig.MaxSubscribers = 2
	cached, overCapacity, extra, _ := cache.Acquire("same", limitedConfig, time.Now())
	assert.Nil(t, cached)
	assert.Nil(t, overCapacity, "full flights must remain tracked while extra clients bypass reuse")
	assert.Nil(t, extra)
	first.Close()
	assert.NoError(t, ctx.Err(), "one disconnect must not cancel another subscriber")
	flight.Publish(200, http.Header{}, []byte("data: second\n\n"))
	cache.Complete("same", flight, cfg, &Result{Status: 200, Header: http.Header{}}, true)
	var received strings.Builder
	for {
		frame, more, err := second.Next(ctx)
		require.NoError(t, err)
		if !more {
			break
		}
		received.Write(frame)
	}
	assert.Equal(t, "data: first\n\ndata: second\n\n", received.String())
	second.Close()
	cached, _, _, leader = cache.Acquire("same", cfg, time.Now())
	require.False(t, leader)
	require.NotNil(t, cached)
	assert.True(t, cached.Successful)
	assert.Len(t, cached.Frames, 2)
}

func TestCacheDoesNotKeepFailedExpiredOversizedOrClearedGenerations(t *testing.T) {
	for _, mode := range []string{"failure", "expiry", "oversize", "clear"} {
		t.Run(mode, func(t *testing.T) {
			cache := NewCache()
			cfg := operation_setting.DefaultRelayGuardConfig().Cache
			cfg.MaxResponseKB = 1
			_, flight, sub, _ := cache.Acquire("key", cfg, time.Now())
			defer sub.Close()
			frame := "ok"
			if mode == "oversize" {
				frame = strings.Repeat("x", 3<<20)
			}
			flight.Publish(200, http.Header{}, []byte(frame))
			if mode == "clear" {
				cache.Clear()
			}
			cache.Complete("key", flight, cfg, &Result{Status: 200, Header: http.Header{}}, mode != "failure")
			if mode == "oversize" {
				received, more, err := sub.Next(context.Background())
				require.NoError(t, err)
				require.True(t, more)
				assert.Equal(t, frame, string(received), "cache size limits must not truncate an ordinary response")
			}
			now := time.Now()
			if mode == "expiry" {
				now = now.Add(time.Hour)
			}
			cached, _, next, leader := cache.Acquire("key", cfg, now)
			require.Nil(t, cached)
			assert.True(t, leader)
			next.Close()
		})
	}
}

func TestJointIPAndKeyBudgetsAreAtomicAndSettleReservations(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rules := []operation_setting.RelayLimitRule{
		{ID: "ip", Name: "IP", Enabled: true, Subject: "ip", Match: "192.0.2.0/24", TPM: 100, RPM: 10, Quota: 100, Period: "day", Concurrent: 2},
		{ID: "key", Name: "Key", Enabled: true, Subject: "key", Match: "*", RPM: 1, Period: "day"},
	}
	b := NewBudget("")
	first, err := b.Begin(rules, "192.0.2.1", 1, "test", now)
	require.NoError(t, err)
	require.NoError(t, first.Reserve(70, 70, now))
	_, err = b.Begin(rules, "192.0.2.2", 1, "test", now)
	require.Error(t, err, "same key must remain limited across IPs")
	second, err := b.Begin(rules, "192.0.2.1", 2, "test", now)
	require.NoError(t, err)
	assert.Error(t, second.Reserve(40, 40, now), "another key must not bypass the IP token reservation")
	require.NoError(t, first.Finish(20, 20, now))
	require.NoError(t, second.Reserve(40, 40, now))
	require.NoError(t, second.Finish(30, 30, now))
	require.NoError(t, second.Finish(90, 90, now))
	stats, err := b.Stats(now)
	require.NoError(t, err)
	for _, u := range stats {
		if u.RuleID == "ip" && u.Subject == "192.0.2.1" {
			assert.EqualValues(t, 50, u.Quota)
			assert.EqualValues(t, 50, u.Tokens)
			assert.Zero(t, u.Concurrent)
			assert.EqualValues(t, 2, u.Requests)
		}
	}
}

func TestInFlightTokenReservationsOutliveOneMinuteAndQuotaSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 10, 27, 23, 58, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "guard.json")
	rules := []operation_setting.RelayLimitRule{{ID: "limit", Name: "IP", Enabled: true, Subject: "ip", Match: "*", TPM: 100, Quota: 100, Period: "day"}}
	b := NewBudget(statePath)
	first, err := b.Begin(rules, "192.0.2.1", 1, "test", now)
	require.NoError(t, err)
	require.NoError(t, first.Reserve(90, 90, now))
	second, err := b.Begin(rules, "192.0.2.1", 2, "test", now.Add(70*time.Second))
	require.NoError(t, err)
	assert.Error(t, second.Reserve(20, 0, now.Add(70*time.Second)))
	restarted := NewBudget(statePath)
	third, err := restarted.Begin(rules, "192.0.2.1", 3, "test", now.Add(80*time.Second))
	require.NoError(t, err)
	assert.Error(t, third.Reserve(1, 20, now.Add(80*time.Second)))
	require.NoError(t, first.Finish(20, 20, now.Add(80*time.Second)))
	stats, err := b.Stats(now.Add(80 * time.Second))
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.EqualValues(t, 20, stats[0].Quota)
	nextDay := now.Add(24 * time.Hour)
	next, err := b.Begin(rules, "192.0.2.1", 4, "test", nextDay)
	require.NoError(t, err)
	require.NoError(t, next.Reserve(100, 100, nextDay))
}

func TestQuotaResetPreservesRollingLimitsAndActiveReservations(t *testing.T) {
	now := time.Date(2026, 10, 27, 23, 59, 50, 0, time.UTC)
	rules := []operation_setting.RelayLimitRule{{ID: "ip", Enabled: true, Subject: "ip", Match: "*", RPM: 2, TPM: 100, Quota: 100, Period: "day", Concurrent: 1}}
	b := NewBudget("")
	first, err := b.Begin(rules, "192.0.2.1", 1, "test", now)
	require.NoError(t, err)
	require.NoError(t, first.Reserve(90, 90, now))
	midnight := now.Add(15 * time.Second)
	_, err = b.Begin(rules, "192.0.2.1", 2, "test", midnight)
	require.Error(t, err, "quota reset must not release an active concurrency slot")
	require.NoError(t, first.Finish(20, 20, midnight))
	second, err := b.Begin(rules, "192.0.2.1", 2, "test", midnight)
	require.NoError(t, err)
	assert.Error(t, second.Reserve(90, 100, midnight), "previous-day tokens remain in the rolling minute")
	require.NoError(t, second.Reserve(80, 100, midnight))
	require.NoError(t, second.Finish(50, 100, midnight))
	_, err = b.Begin(rules, "192.0.2.1", 3, "test", midnight)
	assert.Error(t, err)
	stats, err := b.Stats(midnight)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, "2026-10-28", stats[0].Period)
	assert.EqualValues(t, 100, stats[0].Quota)
	assert.EqualValues(t, 2, stats[0].Requests)
}

func TestInvalidConfigurationRejectsUnsafeRangesAndAmbiguousRules(t *testing.T) {
	for _, change := range []func(*operation_setting.RelayGuardConfig){
		func(c *operation_setting.RelayGuardConfig) { c.Cache.ChargePercent = -1 },
		func(c *operation_setting.RelayGuardConfig) { c.Cache.Scope = "global" },
		func(c *operation_setting.RelayGuardConfig) { c.Cache.Match = "prefix" },
		func(c *operation_setting.RelayGuardConfig) { c.Cache.MaxMemoryMB = 1; c.Cache.MaxResponseKB = 2048 },
		func(c *operation_setting.RelayGuardConfig) {
			c.Limits.Rules = []operation_setting.RelayLimitRule{{ID: "bad", Subject: "ip", Match: "not-an-ip", Period: "day"}}
		},
	} {
		cfg := operation_setting.DefaultRelayGuardConfig()
		change(&cfg)
		assert.Error(t, operation_setting.ValidateRelayGuardConfig(cfg))
	}
}

func TestConcurrentClassroomsIsolateResetsAndKeepBillsAcrossRestart(t *testing.T) {
	now := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "teaching.json")
	b := NewBudget(statePath)
	rules := []operation_setting.RelayLimitRule{
		{ID: "student", Name: "Student", Enabled: true, Subject: "key_ip", Match: "7", RPM: 1, Quota: 100, Period: "total"},
		{ID: "school", Name: "School", Enabled: true, Subject: "key", Match: "7", Quota: 1000, Period: "total"},
	}
	first, err := b.StartClassroom(ClassroomInput{Name: "Class A", KeyID: 7, IPs: []string{"192.0.2.0/25"}, RuleIDs: []string{"student"}}, rules, now)
	require.NoError(t, err)
	second, err := b.StartClassroom(ClassroomInput{Name: "Class B", KeyID: 7, IPs: []string{"192.0.2.128/25"}, RuleIDs: []string{"student"}}, rules, now)
	require.NoError(t, err)
	_, err = b.StartClassroom(ClassroomInput{Name: "Overlapping", KeyID: 7, IPs: []string{"192.0.2.10"}, RuleIDs: []string{"student"}}, rules, now)
	require.Error(t, err, "one key and IP cannot belong to two active classes")
	a, err := b.Begin(rules, "192.0.2.10", 7, "model", now)
	require.NoError(t, err)
	bb, err := b.Begin(rules, "192.0.2.200", 7, "model", now)
	require.NoError(t, err)
	_, err = b.ResetClassroom(first.ID, now)
	require.Error(t, err, "reset must reject active class requests")
	require.NoError(t, a.Reserve(50, 50, now))
	require.NoError(t, bb.Reserve(60, 60, now))
	receipt := TeachingReceipt{Source: "UPSTREAM", BillingStatus: "settled", QuotaPerUnit: 500000, PromptTokens: 10, CompletionTokens: 2}
	require.NoError(t, a.Finish(12, 50, now, receipt))
	require.NoError(t, bb.Finish(12, 60, now, receipt))
	reset, err := b.ResetClassroom(first.ID, now)
	require.NoError(t, err)
	assert.Equal(t, 1, reset)
	_, err = b.Begin(rules, "192.0.2.200", 7, "model", now)
	assert.Error(t, err, "resetting A must not replenish B")
	afterReset, err := b.Begin(rules, "192.0.2.10", 7, "model", now)
	require.NoError(t, err)
	require.NoError(t, afterReset.Finish(12, 0, now, TeachingReceipt{Source: "HIT", BillingStatus: "settled", QuotaPerUnit: 500000, PromptTokens: 10, CompletionTokens: 2}))
	require.NoError(t, b.EndClassroom(first.ID, now.Add(time.Minute)))
	restarted := NewBudget(statePath)
	report, err := restarted.ClassroomReport(first.ID, "2026-10-28", "2026-10-28")
	require.NoError(t, err)
	assert.Equal(t, "Class A", report.Classroom.Name)
	assert.EqualValues(t, 2, report.Summary.Requests)
	assert.EqualValues(t, 50, report.Summary.Quota)
	assert.EqualValues(t, 1, report.Summary.ReusedRequests)
	assert.EqualValues(t, 24, report.Summary.Tokens)
	assert.InDelta(t, 0.0001, report.Summary.AmountUSD, 1e-10)
	assert.Len(t, report.Resets, 1)
	bReport, err := restarted.ClassroomReport(second.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 60, bReport.Summary.Quota)
	stats, err := restarted.Stats(now)
	require.NoError(t, err)
	for _, usage := range stats {
		if usage.RuleID == "school" {
			assert.EqualValues(t, 110, usage.Quota, "class resets preserve the global key budget")
		}
	}
}

func TestClassroomScopeValidationAndFailedPersistenceDoNotResetLimits(t *testing.T) {
	now := time.Now()
	b := NewBudget(filepath.Join(t.TempDir(), "state.json"))
	rules := []operation_setting.RelayLimitRule{{ID: "ip", Enabled: true, Subject: "ip", Match: "*", RPM: 1, Period: "total"}}
	for _, ips := range [][]string{{"*", "192.0.2.1"}, {"not-an-ip"}, {"::ffff:192.0.2.0/120"}} {
		_, err := b.StartClassroom(ClassroomInput{Name: "Invalid", KeyID: 1, IPs: ips, RuleIDs: []string{"ip"}}, rules, now)
		require.Error(t, err)
	}
	classroom, err := b.StartClassroom(ClassroomInput{Name: "Class", KeyID: 1, IPs: []string{"*"}, RuleIDs: []string{"ip"}}, rules, now)
	require.NoError(t, err)
	cfg := operation_setting.DefaultRelayGuardConfig()
	cfg.Limits.Rules = rules
	assert.ErrorIs(t, b.ValidateClassroomLimits(cfg), ErrClassroomRules)
	cfg.Limits.Enabled = true
	require.NoError(t, b.ValidateClassroomLimits(cfg))
	cfg.Limits.Rules = nil
	assert.ErrorIs(t, b.ValidateClassroomLimits(cfg), ErrClassroomRules)
	r, err := b.Begin(rules, "192.0.2.1", 1, "model", now)
	require.NoError(t, err)
	require.NoError(t, r.Finish(0, 0, now))
	originalPath := b.path
	b.path = t.TempDir() // Atomic replacement cannot overwrite a directory.
	_, err = b.ResetClassroom(classroom.ID, now)
	require.Error(t, err)
	b.path = originalPath
	_, err = b.Begin(rules, "192.0.2.1", 1, "model", now)
	require.Error(t, err, "a failed reset must leave admission counters intact")
	_, err = b.ClassroomReport(classroom.ID, "2026-10-29", "2026-10-28")
	assert.Error(t, err)
}

func TestTeachingBillsKeepActualReplayedTokensAndExcludeUnconfirmedCharges(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 59, 0, 0, time.UTC)
	b := NewBudget(filepath.Join(t.TempDir(), "teaching.json"))
	rules := []operation_setting.RelayLimitRule{{ID: "students", Enabled: true, Subject: "key_ip", Match: "*", TPM: 100, Period: "total"}}
	classroom, err := b.StartClassroom(ClassroomInput{Name: "Teaching", KeyID: 9, IPs: []string{"*"}, RuleIDs: []string{"students"}}, rules, now)
	require.NoError(t, err)
	for _, request := range []struct {
		at             time.Time
		tokens, quota  int64
		source, status string
		units          float64
	}{
		{now, 12, 50, "UPSTREAM", "settled", 500000},
		{now, 0, 0, "HIT", "settled", 500000},
		{now.Add(2 * time.Minute), 12, 70, "UPSTREAM", "failed", 500000},
		{now.Add(2 * time.Minute), 12, 100, "UPSTREAM", "settled", 1000000},
	} {
		r, err := b.Begin(rules, "192.0.2.1", 9, "model", request.at)
		require.NoError(t, err)
		require.NoError(t, r.Finish(request.tokens, request.quota, request.at, TeachingReceipt{Source: request.source, BillingStatus: request.status, QuotaPerUnit: request.units, TotalTokens: 12}))
	}
	firstDay, err := b.ClassroomReport(classroom.ID, "2026-10-28", "2026-10-28")
	require.NoError(t, err)
	assert.EqualValues(t, 2, firstDay.Summary.Requests)
	assert.EqualValues(t, 24, firstDay.Summary.Tokens, "free cached tokens remain in bills even when excluded from TPM")
	assert.InDelta(t, 0.0001, firstDay.Summary.AmountUSD, 1e-12)
	all, err := b.ClassroomReport(classroom.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 150, all.Summary.Quota)
	assert.InDelta(t, 0.0002, all.Summary.AmountUSD, 1e-12, "each receipt uses its captured conversion rate")
	assert.EqualValues(t, 1, all.Summary.Unsettled)
	assert.EqualValues(t, 4, all.Summary.Requests)
}

func TestTeachingRestartMarksInterruptedRequestsUnconfirmedWithoutCountingThemAsDeductions(t *testing.T) {
	now := time.Now()
	statePath := filepath.Join(t.TempDir(), "teaching.json")
	b := NewBudget(statePath)
	rules := []operation_setting.RelayLimitRule{{ID: "student", Enabled: true, Subject: "key_ip", Match: "*", Quota: 100, Period: "total"}}
	classroom, err := b.StartClassroom(ClassroomInput{Name: "Teaching", KeyID: 1, IPs: []string{"*"}, RuleIDs: []string{"student"}}, rules, now)
	require.NoError(t, err)
	r, err := b.Begin(rules, "192.0.2.1", 1, "model", now)
	require.NoError(t, err)
	require.NoError(t, r.Reserve(12, 50, now))
	restarted := NewBudget(statePath)
	bill, err := restarted.ClassroomReport(classroom.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 1, bill.Summary.Unsettled)
	assert.EqualValues(t, 1, bill.Summary.Requests)
	assert.Zero(t, bill.Summary.AmountUSD)
	assert.Zero(t, bill.Classroom.Concurrent)
	assert.EqualValues(t, 50, bill.Rows[0].Quota, "unconfirmed reservation remains auditable")
	_, err = restarted.ResetClassroom(classroom.ID, now)
	require.NoError(t, err)
	alsoRestarted := NewBudget(statePath)
	bill, err = alsoRestarted.ClassroomReport(classroom.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 1, bill.Summary.Requests, "restart must not duplicate an unconfirmed receipt")
}

func TestTeachingUpgradePreservesOldGlobalQuotaAndAllowsSeparateKeysOnSameIP(t *testing.T) {
	now := time.Now()
	statePath := filepath.Join(t.TempDir(), "state.json")
	b := NewBudget(statePath)
	rules := []operation_setting.RelayLimitRule{{ID: "global", Enabled: true, Subject: "key", Match: "*", Quota: 100, Period: "total"}}
	r, err := b.Begin(rules, "192.0.2.1", 1, "model", now)
	require.NoError(t, err)
	require.NoError(t, r.Finish(12, 100, now))
	oldState, err := common.Marshal(b.usage)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, oldState, 0600))
	upgraded := NewBudget(statePath)
	first, err := upgraded.StartClassroom(ClassroomInput{Name: "One", KeyID: 1, IPs: []string{"*"}, RuleIDs: []string{"global"}}, rules, now)
	require.NoError(t, err)
	_, err = upgraded.StartClassroom(ClassroomInput{Name: "Two", KeyID: 2, IPs: []string{"*"}, RuleIDs: []string{"global"}}, rules, now)
	require.NoError(t, err)
	first.IPs[0] = "invalid"
	classes, err := upgraded.Classrooms()
	require.NoError(t, err)
	for _, class := range classes {
		assert.Equal(t, []string{"*"}, class.IPs, "returned slices cannot mutate stored class scopes")
	}
	stats, err := upgraded.Stats(now)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.EqualValues(t, 100, stats[0].Quota)
}

func TestClassroomResetIncludesStudentsOutsideRuntimeDisplayLimit(t *testing.T) {
	now := time.Now()
	b := NewBudget("")
	rules := []operation_setting.RelayLimitRule{{ID: "students", Enabled: true, Subject: "key_ip", Match: "*", RPM: 1, Period: "total"}}
	classroom, err := b.StartClassroom(ClassroomInput{Name: "All students", KeyID: 1, IPs: []string{"*"}, RuleIDs: []string{"students"}}, rules, now)
	require.NoError(t, err)
	// One more student than the runtime table exposes; this protects bulk reset
	// against accidentally resetting only the displayed rows.
	for i := range 1001 {
		ip := netip.AddrFrom4([4]byte{10, 0, byte(i / 256), byte(i % 256)}).String()
		r, err := b.Begin(rules, ip, 1, "model", now)
		require.NoError(t, err)
		require.NoError(t, r.Finish(12, 0, now))
	}
	stats, err := b.Stats(now)
	require.NoError(t, err)
	assert.Len(t, stats, 1000)
	subjects, err := b.ResetClassroom(classroom.ID, now)
	require.NoError(t, err)
	assert.Equal(t, 1001, subjects)
	stats, err = b.Stats(now)
	require.NoError(t, err)
	assert.Empty(t, stats)
	bill, err := b.ClassroomReport(classroom.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 1001, bill.Summary.Requests)
	assert.Equal(t, 1001, bill.Summary.IPs)
}
