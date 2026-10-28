package relayguard

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

var ErrClassroomBusy = errors.New("Classroom has active requests")
var ErrClassroomOverlap = errors.New("Active classrooms cannot share an API key and overlapping IP ranges")
var ErrClassroomNotFound = errors.New("Classroom not found")
var ErrClassroomSettings = errors.New("Invalid classroom settings")
var ErrTeachingCapacity = errors.New("Teaching archive capacity reached")
var ErrBillingDates = errors.New("Invalid billing date range")
var ErrClassroomRules = errors.New("End active classrooms before disabling their limit rules")

const maxTeachingRows = 100000
const maxClassroomResets = 10000

var teachingLocation = time.FixedZone("UTC+08:00", 8*60*60)

type ClassroomInput struct {
	Name    string   `json:"name"`
	Course  string   `json:"course"`
	Teacher string   `json:"teacher"`
	Note    string   `json:"note"`
	KeyID   int      `json:"key_id"`
	IPs     []string `json:"ips"`
	RuleIDs []string `json:"rule_ids"`
}

type Classroom struct {
	ClassroomInput
	ID         string `json:"id"`
	StartedAt  int64  `json:"started_at"`
	EndedAt    int64  `json:"ended_at"`
	Concurrent int    `json:"concurrent"`
	ResetCount int    `json:"reset_count"`
}

// TeachingReceipt contains settlement facts only. Prompts, completions and
// credentials never enter the teaching ledger. Amounts use gateway prices.
type TeachingReceipt struct {
	Source           string  `json:"source"`
	BillingStatus    string  `json:"billing_status"`
	QuotaPerUnit     float64 `json:"quota_per_unit"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Group            string  `json:"group"`
	// TotalTokens is independent of the limiter's count-cached-tokens policy.
	TotalTokens   int64 `json:"-"`
	UsageObserved bool  `json:"-"`
}

type TeachingUsage struct {
	TeachingReceipt
	ClassroomID string `json:"classroom_id"`
	Date        string `json:"date"`
	IP          string `json:"ip"`
	Model       string `json:"model"`
	Requests    int64  `json:"requests"`
	Tokens      int64  `json:"tokens"`
	Quota       int64  `json:"quota"`
	FirstAt     int64  `json:"first_at"`
	LastAt      int64  `json:"last_at"`
}

type ClassroomReset struct {
	ClassroomID string `json:"classroom_id"`
	At          int64  `json:"at"`
	Subjects    int    `json:"subjects"`
}

type TeachingSummary struct {
	Requests       int64   `json:"requests"`
	Tokens         int64   `json:"tokens"`
	Quota          int64   `json:"quota"`
	AmountUSD      float64 `json:"amount_usd"`
	ReusedRequests int64   `json:"reused_requests"`
	Unsettled      int64   `json:"unsettled_requests"`
	IPs            int     `json:"ips"`
}

type ClassroomBill struct {
	Classroom Classroom        `json:"classroom"`
	Rows      []TeachingUsage  `json:"rows"`
	Summary   TeachingSummary  `json:"summary"`
	Resets    []ClassroomReset `json:"resets"`
	Timezone  string           `json:"timezone"`
}

type classroomBook struct {
	Classes map[string]*Classroom      `json:"classes"`
	Rows    map[string]*TeachingUsage  `json:"rows"`
	Resets  []ClassroomReset           `json:"resets"`
	Pending map[uint64]teachingPending `json:"pending,omitempty"`
}

type teachingPending struct {
	ClassroomID  string  `json:"classroom_id"`
	IP           string  `json:"ip"`
	Model        string  `json:"model"`
	At           int64   `json:"at"`
	Quota        int64   `json:"quota"`
	QuotaPerUnit float64 `json:"quota_per_unit"`
}

type budgetSnapshot struct {
	Version  int               `json:"version"`
	Usage    map[string]*Usage `json:"usage"`
	Teaching classroomBook     `json:"teaching"`
}

func (b *Budget) ValidateClassroomLimits(config operation_setting.RelayGuardConfig) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return err
	}
	for _, classroom := range b.teaching.Classes {
		if classroom.EndedAt != 0 {
			continue
		}
		if !config.Limits.Enabled {
			return ErrClassroomRules
		}
		for _, id := range classroom.RuleIDs {
			if !slices.ContainsFunc(config.Limits.Rules, func(rule operation_setting.RelayLimitRule) bool { return rule.ID == id && rule.Enabled }) {
				return ErrClassroomRules
			}
		}
	}
	return nil
}

// classroomPrefixes also normalizes IPv4-mapped single addresses. Mapped CIDRs
// are rejected to avoid disagreeing with Gin's normalized client address.
func classroomPrefixes(ips []string) ([]netip.Prefix, error) {
	if len(ips) == 0 || len(ips) > 256 {
		return nil, errors.New("Specify between 1 and 256 IP addresses or CIDRs")
	}
	result := make([]netip.Prefix, 0, len(ips))
	for _, value := range ips {
		if value == "*" {
			if len(ips) != 1 {
				return nil, errors.New("Use * alone to match all classroom IP addresses")
			}
			return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}, nil
		}
		if ip, err := netip.ParseAddr(value); err == nil && ip.Zone() == "" {
			ip = ip.Unmap()
			result = append(result, netip.PrefixFrom(ip, ip.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() {
			return nil, errors.New("Invalid classroom IP address or CIDR")
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func (c *Classroom) matches(ip netip.Addr, keyID int) bool {
	if c.EndedAt != 0 || c.KeyID != keyID || !ip.IsValid() {
		return false
	}
	prefixes, err := classroomPrefixes(c.IPs)
	if err != nil {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func (b *Budget) StartClassroom(input ClassroomInput, rules []operation_setting.RelayLimitRule, now time.Time) (*Classroom, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.IPs = slices.Clone(input.IPs)
	input.RuleIDs = slices.Clone(input.RuleIDs)
	if input.Name == "" || len(input.Name) > 200 || len(input.Course) > 200 || len(input.Teacher) > 100 || len(input.Note) > 2000 || input.KeyID <= 0 || len(input.RuleIDs) == 0 || len(input.RuleIDs) > 200 {
		return nil, ErrClassroomSettings
	}
	prefixes, err := classroomPrefixes(input.IPs)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrClassroomSettings, err)
	}
	seen := map[string]bool{}
	for _, id := range input.RuleIDs {
		if seen[id] {
			return nil, ErrClassroomSettings
		}
		seen[id] = true
		if !slices.ContainsFunc(rules, func(rule operation_setting.RelayLimitRule) bool {
			return rule.ID == id && rule.Enabled && (rule.Subject == "ip" || rule.Match == "*" || rule.Match == fmt.Sprint(input.KeyID))
		}) {
			return nil, ErrClassroomSettings
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return nil, err
	}
	if len(b.teaching.Classes) >= 1000 {
		return nil, ErrTeachingCapacity
	}
	for _, other := range b.teaching.Classes {
		if other.EndedAt != 0 || other.KeyID != input.KeyID {
			continue
		}
		existing, _ := classroomPrefixes(other.IPs)
		for _, a := range prefixes {
			for _, bb := range existing {
				if a.Overlaps(bb) {
					return nil, ErrClassroomOverlap
				}
			}
		}
	}
	classroom := &Classroom{ClassroomInput: input, ID: common.GetUUID(), StartedAt: now.Unix()}
	b.teaching.Classes[classroom.ID] = classroom
	if err := b.persistLocked(); err != nil {
		delete(b.teaching.Classes, classroom.ID)
		return nil, err
	}
	copy := *classroom
	copy.IPs, copy.RuleIDs = slices.Clone(classroom.IPs), slices.Clone(classroom.RuleIDs)
	return &copy, nil
}

func (b *Budget) Classrooms() ([]Classroom, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return nil, err
	}
	result := make([]Classroom, 0, len(b.teaching.Classes))
	for _, c := range b.teaching.Classes {
		copy := *c
		copy.IPs = slices.Clone(c.IPs)
		copy.RuleIDs = slices.Clone(c.RuleIDs)
		result = append(result, copy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt > result[j].StartedAt })
	return result, b.storageError
}

// ResetClassroom resets all students in the class, including subjects outside
// the statistics table's 1,000-row display cap. Global budgets and bills remain.
func (b *Budget) ResetClassroom(id string, now time.Time) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return 0, err
	}
	classroom := b.teaching.Classes[id]
	if classroom == nil || classroom.EndedAt != 0 {
		return 0, ErrClassroomNotFound
	}
	if classroom.Concurrent > 0 {
		return 0, ErrClassroomBusy
	}
	if len(b.teaching.Resets) >= maxClassroomResets {
		return 0, ErrTeachingCapacity
	}
	removed := map[string]*Usage{}
	for key, usage := range b.usage {
		if usage.ClassroomID == id {
			removed[key] = usage
			delete(b.usage, key)
			b.eventCount -= len(usage.Events)
		}
	}
	classroom.ResetCount++
	b.teaching.Resets = append(b.teaching.Resets, ClassroomReset{ClassroomID: id, At: now.Unix(), Subjects: len(removed)})
	if err := b.persistLocked(); err != nil {
		for key, usage := range removed {
			b.usage[key] = usage
			b.eventCount += len(usage.Events)
		}
		classroom.ResetCount--
		b.teaching.Resets = b.teaching.Resets[:len(b.teaching.Resets)-1]
		return 0, err
	}
	return len(removed), nil
}

func (b *Budget) EndClassroom(id string, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return err
	}
	classroom := b.teaching.Classes[id]
	if classroom == nil {
		return ErrClassroomNotFound
	}
	if classroom.Concurrent > 0 {
		return ErrClassroomBusy
	}
	if classroom.EndedAt != 0 {
		return nil
	}
	classroom.EndedAt = now.Unix()
	if err := b.persistLocked(); err != nil {
		classroom.EndedAt = 0
		return err
	}
	return nil
}

func (b *Budget) ClassroomReport(id, from, to string) (*ClassroomBill, error) {
	for _, date := range []string{from, to} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return nil, ErrBillingDates
			}
		}
	}
	if from != "" && to != "" && from > to {
		return nil, ErrBillingDates
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadLocked(); err != nil {
		return nil, err
	}
	classroom := b.teaching.Classes[id]
	if classroom == nil {
		return nil, ErrClassroomNotFound
	}
	report := &ClassroomBill{Classroom: *classroom, Rows: []TeachingUsage{}, Resets: []ClassroomReset{}, Timezone: "UTC+08:00"}
	report.Classroom.IPs, report.Classroom.RuleIDs = slices.Clone(classroom.IPs), slices.Clone(classroom.RuleIDs)
	ips := map[string]bool{}
	for _, row := range b.teaching.Rows {
		if row.ClassroomID != id || from != "" && row.Date < from || to != "" && row.Date > to {
			continue
		}
		report.Rows = append(report.Rows, *row)
		report.Summary.Requests += row.Requests
		report.Summary.Tokens += row.Tokens
		if row.BillingStatus == "settled" {
			report.Summary.Quota += row.Quota
			report.Summary.AmountUSD += float64(row.Quota) / row.QuotaPerUnit
		} else if row.BillingStatus != "settled" {
			report.Summary.Unsettled += row.Requests
		}
		if row.Source == "HIT" || row.Source == "SHARED" {
			report.Summary.ReusedRequests += row.Requests
		}
		ips[row.IP] = true
	}
	for _, reset := range b.teaching.Resets {
		date := time.Unix(reset.At, 0).In(teachingLocation).Format("2006-01-02")
		if reset.ClassroomID == id && (from == "" || date >= from) && (to == "" || date <= to) {
			report.Resets = append(report.Resets, reset)
		}
	}
	report.Summary.IPs = len(ips)
	sort.Slice(report.Rows, func(i, j int) bool {
		a, bb := report.Rows[i], report.Rows[j]
		return a.Date+"/"+a.IP+"/"+a.Model+"/"+a.Source+"/"+a.BillingStatus < bb.Date+"/"+bb.IP+"/"+bb.Model+"/"+bb.Source+"/"+bb.BillingStatus
	})
	return report, b.storageError
}

func (b *Budget) recordTeachingLocked(r *Reservation, tokens, quota int64, now time.Time, receipt TeachingReceipt) error {
	if r.classroomID == "" {
		return nil
	}
	classroom := b.teaching.Classes[r.classroomID]
	classroom.Concurrent = max(0, classroom.Concurrent-1)
	if receipt.Source != "HIT" && receipt.Source != "SHARED" {
		receipt.Source = "UPSTREAM"
	}
	if receipt.BillingStatus != "settled" && receipt.BillingStatus != "failed" {
		receipt.BillingStatus = "unbilled"
	}
	if receipt.QuotaPerUnit < 0.000001 || receipt.QuotaPerUnit > 1e15 || math.IsNaN(receipt.QuotaPerUnit) || math.IsInf(receipt.QuotaPerUnit, 0) {
		return errors.New("Invalid teaching quota conversion")
	}
	if receipt.UsageObserved {
		tokens = max(0, min(receipt.TotalTokens, common.MaxQuota))
	} else {
		tokens = max(tokens, max(0, min(receipt.TotalTokens, common.MaxQuota)))
	}
	date := now.In(teachingLocation).Format("2006-01-02")
	keyData, err := common.Marshal([]any{r.classroomID, date, r.ip, r.model, receipt.Group, receipt.Source, receipt.BillingStatus, receipt.QuotaPerUnit})
	if err != nil {
		return err
	}
	key := string(keyData)
	row := b.teaching.Rows[key]
	if row == nil {
		if len(b.teaching.Rows) >= maxTeachingRows {
			return errors.New("Teaching bill capacity reached")
		}
		row = &TeachingUsage{TeachingReceipt: receipt, ClassroomID: r.classroomID, Date: date, IP: r.ip, Model: r.model, FirstAt: now.Unix()}
		row.PromptTokens, row.CompletionTokens = 0, 0
		b.teaching.Rows[key] = row
	}
	if row.Requests >= common.MaxWalletQuota || row.Quota > common.MaxWalletQuota-quota {
		return fmt.Errorf("teaching bill aggregate is out of range")
	}
	row.Requests++
	row.Quota += quota
	row.Tokens += tokens
	row.PromptTokens += max(0, min(receipt.PromptTokens, common.MaxQuota))
	row.CompletionTokens += max(0, min(receipt.CompletionTokens, common.MaxQuota))
	row.LastAt = now.Unix()
	b.teachingTotals.Requests++
	b.teachingTotals.Quota += quota
	b.teachingTotals.Tokens += tokens
	b.teachingTotals.PromptTokens += max(0, min(receipt.PromptTokens, common.MaxQuota))
	b.teachingTotals.CompletionTokens += max(0, min(receipt.CompletionTokens, common.MaxQuota))
	return nil
}

// validateTeachingLedger rejects damaged archives before they can enter totals
// or leave an ambiguous class-to-IP mapping after restart.
func (b *Budget) validateTeachingLedger() error {
	if len(b.teaching.Classes) > 1000 || len(b.teaching.Rows)+len(b.teaching.Pending) > maxTeachingRows || len(b.teaching.Resets) > maxClassroomResets {
		return errors.New("Invalid teaching ledger capacity")
	}
	for id, classroom := range b.teaching.Classes {
		if classroom == nil || classroom.ID != id || classroom.KeyID <= 0 || classroom.StartedAt <= 0 || classroom.EndedAt < 0 {
			return errors.New("Invalid classroom state")
		}
		prefixes, err := classroomPrefixes(classroom.IPs)
		if err != nil {
			return err
		}
		classroom.Concurrent = 0
		if classroom.EndedAt != 0 {
			continue
		}
		for otherID, other := range b.teaching.Classes {
			if otherID <= id || other == nil || other.EndedAt != 0 || other.KeyID != classroom.KeyID {
				continue
			}
			existing, err := classroomPrefixes(other.IPs)
			if err != nil {
				return err
			}
			for _, a := range prefixes {
				for _, otherPrefix := range existing {
					if a.Overlaps(otherPrefix) {
						return ErrClassroomOverlap
					}
				}
			}
		}
	}
	for _, row := range b.teaching.Rows {
		if row == nil || row.QuotaPerUnit < 0.000001 || row.QuotaPerUnit > 1e15 || math.IsNaN(row.QuotaPerUnit) || math.IsInf(row.QuotaPerUnit, 0) || b.teaching.Classes[row.ClassroomID] == nil {
			return errors.New("Invalid teaching usage")
		}
		if _, err := time.Parse("2006-01-02", row.Date); err != nil {
			return errors.New("Invalid teaching usage date")
		}
		if row.Source != "UPSTREAM" && row.Source != "HIT" && row.Source != "SHARED" || row.BillingStatus != "settled" && row.BillingStatus != "failed" && row.BillingStatus != "unbilled" {
			return errors.New("Invalid teaching settlement state")
		}
		for _, pair := range [][2]*int64{{&b.teachingTotals.Requests, &row.Requests}, {&b.teachingTotals.Quota, &row.Quota}, {&b.teachingTotals.Tokens, &row.Tokens}, {&b.teachingTotals.PromptTokens, &row.PromptTokens}, {&b.teachingTotals.CompletionTokens, &row.CompletionTokens}} {
			if *pair[1] < 0 || *pair[1] > common.MaxWalletQuota-*pair[0] {
				return errors.New("Teaching bill aggregate is out of range")
			}
			*pair[0] += *pair[1]
		}
	}
	for _, reset := range b.teaching.Resets {
		if b.teaching.Classes[reset.ClassroomID] == nil || reset.At <= 0 || reset.Subjects < 0 {
			return errors.New("Invalid classroom reset history")
		}
	}
	if b.teaching.Pending == nil {
		b.teaching.Pending = map[uint64]teachingPending{}
	}
	// A terminated process cannot prove settlement of an in-flight request.
	// Retain it as an unconfirmed receipt, never as a reimbursable charge.
	for id, pending := range b.teaching.Pending {
		if b.teaching.Classes[pending.ClassroomID] == nil || pending.At <= 0 || pending.Quota < 0 || pending.Quota > common.MaxQuota {
			return errors.New("Invalid pending teaching receipt")
		}
		for _, total := range []int64{b.teachingTotals.Requests, b.teachingTotals.Quota} {
			if total > common.MaxWalletQuota-common.MaxQuota {
				return errors.New("Teaching bill aggregate is out of range")
			}
		}
		r := &Reservation{classroomID: pending.ClassroomID, ip: pending.IP, model: pending.Model}
		if err := b.recordTeachingLocked(r, 0, pending.Quota, time.Unix(pending.At, 0), TeachingReceipt{BillingStatus: "unbilled", QuotaPerUnit: pending.QuotaPerUnit}); err != nil {
			return err
		}
		b.nextID = max(b.nextID, id)
		delete(b.teaching.Pending, id)
	}
	return nil
}
