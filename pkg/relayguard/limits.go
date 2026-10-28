// Package relayguard coordinates response reuse and independent IP/key budgets.
package relayguard

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type LimitError struct{ Rule, Dimension string }

func (e *LimitError) Error() string {
	return fmt.Sprintf("request limit reached (%s: %s)", e.Rule, e.Dimension)
}

type Usage struct {
	Key         string           `json:"key"`
	RuleID      string           `json:"rule_id"`
	ClassroomID string           `json:"classroom_id,omitempty"`
	Subject     string           `json:"subject"`
	Period      string           `json:"period"`
	PeriodKind  string           `json:"period_kind"`
	Quota       int64            `json:"quota"`
	Quotas      map[string]int64 `json:"quota_periods"`
	Requests    int64            `json:"requests"`
	Tokens      int64            `json:"tokens"`
	Concurrent  int              `json:"concurrent"`
	Events      []usageEvent     `json:"events,omitempty"`
	LastSeen    int64            `json:"last_seen"`
}
type usageEvent struct {
	ID      uint64 `json:"id"`
	At      int64  `json:"at"`
	TokenAt int64  `json:"token_at,omitempty"`
	Tokens  int64  `json:"tokens"`
	Pending bool   `json:"pending,omitempty"`
}
type reservationItem struct {
	key, period string
	rule        operation_setting.RelayLimitRule
}
type Reservation struct {
	engine                 *Budget
	id                     uint64
	items                  []reservationItem
	tokens, quota          int64
	closed                 bool
	classroomID, ip, model string
}

// Budget writes quota reservations before admission. A crash conservatively
// retains reserved quota, so restarting the process cannot replenish a budget.
// One state file belongs to one gateway process. Redis is not required.
type Budget struct {
	mu             sync.Mutex
	path           string
	loaded         bool
	loadError      error
	nextID         uint64
	usage          map[string]*Usage
	storageError   error
	eventCount     int
	teaching       classroomBook
	teachingTotals TeachingUsage
}

func NewBudget(statePath string) *Budget {
	return &Budget{path: statePath, usage: map[string]*Usage{}, teaching: classroomBook{Classes: map[string]*Classroom{}, Rows: map[string]*TeachingUsage{}, Resets: []ClassroomReset{}, Pending: map[uint64]teachingPending{}}}
}

func (b *Budget) loadLocked() error {
	if b.loaded {
		return b.loadError
	}
	b.loaded = true
	if b.path == "" {
		return nil
	}
	data, err := os.ReadFile(b.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		var snapshot budgetSnapshot
		err = common.Unmarshal(data, &snapshot)
		if err == nil && snapshot.Version == 0 {
			// Upgrade the earlier usage-only file without replenishing quotas.
			err = common.Unmarshal(data, &b.usage)
		} else if err == nil && snapshot.Version == 1 {
			b.usage, b.teaching = snapshot.Usage, snapshot.Teaching
			if b.teaching.Classes == nil || b.teaching.Rows == nil {
				err = fmt.Errorf("invalid teaching ledger")
			}
		} else if err == nil {
			err = fmt.Errorf("unsupported request limit state version")
		}
	}
	if err != nil {
		b.loadError = fmt.Errorf("cannot load request limit state: %w", err)
		return b.loadError
	}
	if b.usage == nil {
		b.usage = map[string]*Usage{}
	}
	if err := b.validateTeachingLedger(); err != nil {
		b.loadError = err
		return err
	}
	for _, u := range b.usage {
		if u == nil || u.Quota < 0 || u.Quota > common.MaxWalletQuota || u.Quotas == nil || (u.PeriodKind != "day" && u.PeriodKind != "month" && u.PeriodKind != "total") {
			b.loadError = fmt.Errorf("invalid request limit state")
			return b.loadError
		}
		for _, quota := range u.Quotas {
			if quota < 0 || quota > common.MaxWalletQuota {
				b.loadError = fmt.Errorf("invalid request limit state")
				return b.loadError
			}
		}
		u.Concurrent = 0
		b.eventCount += len(u.Events)
		for _, event := range u.Events {
			b.nextID = max(b.nextID, event.ID)
		}
		for i := range u.Events {
			if u.Events[i].Tokens < 0 || u.Events[i].Tokens > common.MaxQuota {
				b.loadError = fmt.Errorf("invalid request limit state")
				return b.loadError
			}
			u.Events[i].Pending = false
		}
	}
	return nil
}

func (b *Budget) persistLocked() (persistErr error) {
	defer func() { b.storageError = persistErr }()
	if b.path == "" {
		return nil
	}
	data, err := common.Marshal(budgetSnapshot{Version: 1, Usage: b.usage, Teaching: b.teaching})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(b.path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(b.path), ".relay-guard-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, b.path)
	}
	return err
}

func quotaPeriod(period string, now time.Time) string {
	switch period {
	case "day":
		return now.UTC().Format("2006-01-02")
	case "month":
		return now.UTC().Format("2006-01")
	default:
		return "total"
	}
}

func (b *Budget) pruneUsage(u *Usage, now time.Time) {
	cutoff := now.Add(-time.Minute).UnixMilli()
	previousCount := len(u.Events)
	events := u.Events[:0]
	for _, event := range u.Events {
		if event.Pending || max(event.At, event.TokenAt) > cutoff {
			events = append(events, event)
		}
	}
	u.Events = events
	b.eventCount -= previousCount - len(events)
	u.Period = quotaPeriod(u.PeriodKind, now)
	u.Quota = u.Quotas[u.Period]
	if u.Concurrent == 0 {
		for period := range u.Quotas {
			if period != "total" && period < now.Add(-32*24*time.Hour).UTC().Format("2006-01-02") {
				delete(u.Quotas, period)
			}
		}
	}
	u.Requests = 0
	u.Tokens = 0
	for _, event := range u.Events {
		if event.At > cutoff {
			u.Requests++
		}
		if event.Pending || event.TokenAt > cutoff {
			u.Tokens += event.Tokens
		}
	}
}

func (b *Budget) Begin(rules []operation_setting.RelayLimitRule, ip string, keyID int, model string, now time.Time) (*Reservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return nil, err
	}
	if b.storageError != nil {
		if err := b.persistLocked(); err != nil {
			return nil, err
		}
	}
	if b.eventCount >= 1000000 {
		for _, usage := range b.usage {
			b.pruneUsage(usage, now)
		}
	}
	b.nextID++
	r := &Reservation{engine: b, id: b.nextID}
	address, _ := netip.ParseAddr(ip)
	r.ip, r.model = address.Unmap().String(), model
	for _, classroom := range b.teaching.Classes {
		if classroom.matches(address, keyID) {
			pending := 0
			for _, active := range b.teaching.Classes {
				pending += active.Concurrent
			}
			if len(b.teaching.Rows)+pending >= maxTeachingRows {
				return nil, fmt.Errorf("teaching bill capacity reached")
			}
			headroom := int64(pending+1) * common.MaxQuota
			for _, total := range []int64{b.teachingTotals.Requests, b.teachingTotals.Quota, b.teachingTotals.Tokens, b.teachingTotals.PromptTokens, b.teachingTotals.CompletionTokens} {
				if total > common.MaxWalletQuota-headroom {
					return nil, fmt.Errorf("teaching bill aggregate capacity reached")
				}
			}
			r.classroomID = classroom.ID
			break
		}
	}
	for _, rule := range rules {
		if !rule.Enabled || !operation_setting.RelayGuardModelMatches(rule.Models, model) {
			continue
		}
		subject := strconv.Itoa(keyID)
		switch rule.Subject {
		case "ip":
			if !address.IsValid() {
				return nil, fmt.Errorf("cannot resolve client IP")
			}
			address = address.Unmap()
			subject = address.String()
			if rule.Match != "*" {
				prefix, err := netip.ParsePrefix(rule.Match)
				if err == nil {
					if !prefix.Contains(address) {
						continue
					}
				} else {
					expected, _ := netip.ParseAddr(rule.Match)
					if expected.Unmap() != address {
						continue
					}
				}
			}
		case "key", "key_ip":
			if keyID <= 0 || rule.Match != "*" && rule.Match != subject {
				continue
			}
			if rule.Subject == "key_ip" {
				if !address.IsValid() {
					return nil, fmt.Errorf("cannot resolve client IP")
				}
				subject += "@" + address.Unmap().String()
			}
		}
		period := quotaPeriod(rule.Period, now)
		key := rule.ID + "/" + subject
		classroomID := ""
		if classroom := b.teaching.Classes[r.classroomID]; classroom != nil && slices.Contains(classroom.RuleIDs, rule.ID) {
			classroomID = classroom.ID
			key += "/class/" + classroomID
		}
		u := b.usage[key]
		if u == nil {
			// Drop expired finite periods; never evict a lifetime budget.
			if len(b.usage) >= 100000 {
				for k, old := range b.usage {
					if old.Concurrent == 0 && old.PeriodKind != "total" && old.LastSeen < now.Add(-32*24*time.Hour).Unix() {
						b.eventCount -= len(old.Events)
						delete(b.usage, k)
					}
				}
			}
			if len(b.usage) >= 100000 {
				return nil, fmt.Errorf("request limit subject capacity reached")
			}
			u = &Usage{Key: key, RuleID: rule.ID, ClassroomID: classroomID, Subject: subject, PeriodKind: rule.Period, Quotas: map[string]int64{}}
			b.usage[key] = u
		}
		u.PeriodKind = rule.Period
		b.pruneUsage(u, now)
		if len(u.Events) >= 65536 || b.eventCount+len(r.items)+1 > 1000000 {
			return nil, fmt.Errorf("request limit event capacity reached")
		}
		if rule.RPM > 0 && u.Requests >= int64(rule.RPM) {
			return nil, &LimitError{rule.Name, "RPM"}
		}
		if rule.Concurrent > 0 && u.Concurrent >= rule.Concurrent {
			return nil, &LimitError{rule.Name, "concurrency"}
		}
		if rule.Quota > 0 && u.Quota >= rule.Quota {
			return nil, &LimitError{rule.Name, "quota"}
		}
		r.items = append(r.items, reservationItem{key, period, rule})
	}
	if r.classroomID != "" {
		b.teaching.Pending[r.id] = teachingPending{ClassroomID: r.classroomID, IP: r.ip, Model: r.model, At: now.Unix(), QuotaPerUnit: common.QuotaPerUnit}
		if err := b.persistLocked(); err != nil {
			delete(b.teaching.Pending, r.id)
			return nil, err
		}
	}
	for _, item := range r.items {
		u := b.usage[item.key]
		u.Concurrent++
		u.Requests++
		u.LastSeen = now.Unix()
		u.Events = append(u.Events, usageEvent{ID: r.id, At: now.UnixMilli(), Pending: true})
		b.eventCount++
	}
	if classroom := b.teaching.Classes[r.classroomID]; classroom != nil {
		classroom.Concurrent++
	}
	// RPM and concurrency are ephemeral; quota reservations below are durable.
	return r, nil
}

func (r *Reservation) Reserve(tokens, quota int64, now time.Time) error {
	if r == nil {
		return nil
	}
	b := r.engine
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.closed {
		return fmt.Errorf("request reservation is closed")
	}
	if len(r.items) == 0 {
		return nil
	}
	if tokens < 0 || tokens > common.MaxQuota || quota < 0 || quota > common.MaxQuota {
		return fmt.Errorf("request reservation is out of range")
	}
	tokens = max(tokens, r.tokens)
	quota = max(quota, r.quota)
	for _, item := range r.items {
		u := b.usage[item.key]
		b.pruneUsage(u, now)
		if item.rule.TPM > 0 && (tokens-r.tokens) > item.rule.TPM-u.Tokens {
			return &LimitError{item.rule.Name, "TPM"}
		}
		if item.rule.Quota > 0 && (quota-r.quota) > item.rule.Quota-u.Quotas[item.period] {
			return &LimitError{item.rule.Name, "quota"}
		}
	}
	for _, item := range r.items {
		u := b.usage[item.key]
		u.Quotas[item.period] += quota - r.quota
		for i := range u.Events {
			if u.Events[i].ID == r.id {
				u.Events[i].Tokens = tokens
				break
			}
		}
		b.pruneUsage(u, now)
	}
	r.tokens = tokens
	r.quota = quota
	if pending, ok := b.teaching.Pending[r.id]; ok {
		pending.Quota = quota
		b.teaching.Pending[r.id] = pending
	}
	return b.persistLocked()
}

func (r *Reservation) Finish(tokens, quota int64, now time.Time, receipts ...TeachingReceipt) error {
	if r == nil {
		return nil
	}
	b := r.engine
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if len(r.items) == 0 && r.classroomID == "" {
		return nil
	}
	tokens = max(0, min(tokens, common.MaxQuota))
	quota = max(0, min(quota, common.MaxQuota))
	for _, item := range r.items {
		u := b.usage[item.key]
		if u == nil {
			continue
		}
		u.Concurrent = max(0, u.Concurrent-1)
		u.Quotas[item.period] = max(0, min(int64(common.MaxWalletQuota), u.Quotas[item.period]-r.quota+quota))
		for i := range u.Events {
			if u.Events[i].ID == r.id {
				u.Events[i].Tokens = tokens
				u.Events[i].Pending = false
				u.Events[i].TokenAt = now.UnixMilli()
				break
			}
		}
		b.pruneUsage(u, now)
	}
	if r.classroomID != "" {
		receipt := TeachingReceipt{QuotaPerUnit: common.QuotaPerUnit}
		if len(receipts) > 0 {
			receipt = receipts[0]
		}
		if err := b.recordTeachingLocked(r, tokens, quota, now, receipt); err != nil {
			b.storageError = err
			// An unrecordable receipt must not be hidden by a later successful
			// file write. Keep admission closed for this process.
			b.loadError = err
			return err
		}
		delete(b.teaching.Pending, r.id)
	}
	return b.persistLocked()
}

func (b *Budget) Stats(now time.Time) ([]Usage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return nil, err
	}
	result := make([]Usage, 0, min(1000, len(b.usage)))
	for _, u := range b.usage {
		b.pruneUsage(u, now)
		copy := *u
		copy.Events = nil
		copy.Quotas = nil
		result = append(result, copy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastSeen > result[j].LastSeen })
	if len(result) > 1000 {
		result = result[:1000]
	}
	return result, b.storageError
}

func (b *Budget) Reset(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return err
	}
	u := b.usage[key]
	if u == nil {
		return fmt.Errorf("request limit subject not found")
	}
	if u.Concurrent > 0 {
		return fmt.Errorf("cannot reset a subject with active requests")
	}
	b.eventCount -= len(u.Events)
	delete(b.usage, key)
	if err := b.persistLocked(); err != nil {
		b.usage[key] = u
		b.eventCount += len(u.Events)
		return err
	}
	return nil
}
