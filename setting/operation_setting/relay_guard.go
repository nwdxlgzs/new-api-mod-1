package operation_setting

import (
	"fmt"
	"net/netip"
	"path"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const RelayGuardOption = "RelayGuard"

// A single validated option keeps related controls atomic without a schema migration.
type RelayGuardConfig struct {
	Invalid bool             `json:"-"`
	Cache   RelayCacheConfig `json:"cache"`
	Limits  RelayLimitConfig `json:"limits"`
}

type RelayCacheConfig struct {
	Enabled        bool     `json:"enabled"`
	Stream         bool     `json:"stream"`
	Coalesce       bool     `json:"coalesce"`
	Scope          string   `json:"scope"`
	Match          string   `json:"match"`
	PrefixChars    int      `json:"prefix_chars"`
	TTLSeconds     int      `json:"ttl_seconds"`
	MaxEntries     int      `json:"max_entries"`
	MaxMemoryMB    int      `json:"max_memory_mb"`
	MaxResponseKB  int      `json:"max_response_kb"`
	MaxSubscribers int      `json:"max_subscribers"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	ReplayDelayMS  int      `json:"replay_delay_ms"`
	ChargePercent  int      `json:"charge_percent"`
	Models         []string `json:"models"`
}

type RelayLimitConfig struct {
	Enabled             bool             `json:"enabled"`
	CountCachedTokens   bool             `json:"count_cached_tokens"`
	DefaultOutputTokens int              `json:"default_output_tokens"`
	Rules               []RelayLimitRule `json:"rules"`
}

type RelayLimitRule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	Subject    string   `json:"subject"`
	Match      string   `json:"match"`
	Models     []string `json:"models"`
	RPM        int      `json:"rpm"`
	TPM        int64    `json:"tpm"`
	Quota      int64    `json:"quota"`
	Period     string   `json:"period"`
	Concurrent int      `json:"concurrent"`
}

func DefaultRelayGuardConfig() RelayGuardConfig {
	return RelayGuardConfig{
		Cache:  RelayCacheConfig{Stream: true, Coalesce: true, Scope: "key", Match: "exact", PrefixChars: 1024, TTLSeconds: 300, MaxEntries: 1000, MaxMemoryMB: 128, MaxResponseKB: 2048, MaxSubscribers: 32, TimeoutSeconds: 300, Models: []string{}},
		Limits: RelayLimitConfig{CountCachedTokens: true, DefaultOutputTokens: 4096, Rules: []RelayLimitRule{}},
	}
}

func RelayGuardSettings() RelayGuardConfig {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[RelayGuardOption]
	common.OptionMapRWMutex.RUnlock()
	cfg := DefaultRelayGuardConfig()
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, &cfg); err != nil || ValidateRelayGuardConfig(cfg) != nil {
			// A corrupt policy must not silently turn enforcement off.
			cfg.Invalid = true
		}
	}
	if cfg.Cache.Models == nil {
		cfg.Cache.Models = []string{}
	}
	if cfg.Limits.Rules == nil {
		cfg.Limits.Rules = []RelayLimitRule{}
	}
	for i := range cfg.Limits.Rules {
		if cfg.Limits.Rules[i].Models == nil {
			cfg.Limits.Rules[i].Models = []string{}
		}
	}
	return cfg
}

func ValidateRelayGuardConfig(cfg RelayGuardConfig) error {
	c := cfg.Cache
	if c.Match == "prefix" && len(c.Models) == 0 {
		return fmt.Errorf("prefix matching requires an explicit model allowlist")
	}
	if c.Scope != "key" && c.Scope != "user" {
		return fmt.Errorf("invalid cache scope")
	}
	if c.Match != "exact" && c.Match != "prefix" {
		return fmt.Errorf("invalid cache match mode")
	}
	if c.PrefixChars < 128 || c.PrefixChars > 100000 || c.TTLSeconds < 1 || c.TTLSeconds > 86400 || c.MaxEntries < 1 || c.MaxEntries > 100000 || c.MaxMemoryMB < 1 || c.MaxMemoryMB > 2048 || c.MaxResponseKB < 1 || c.MaxResponseKB > 65536 || c.MaxSubscribers < 1 || c.MaxSubscribers > 128 || c.TimeoutSeconds < 10 || c.TimeoutSeconds > 3600 || c.ReplayDelayMS < 0 || c.ReplayDelayMS > 1000 || c.ChargePercent < 0 || c.ChargePercent > 100 {
		return fmt.Errorf("cache settings are out of range")
	}
	if c.MaxResponseKB > c.MaxMemoryMB*1024 {
		return fmt.Errorf("response size must fit within cache memory")
	}
	if cfg.Limits.DefaultOutputTokens < 1 || cfg.Limits.DefaultOutputTokens > 1000000 || len(cfg.Limits.Rules) > 200 {
		return fmt.Errorf("limit settings are out of range")
	}
	if err := validateGuardModels(c.Models); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, rule := range cfg.Limits.Rules {
		if rule.ID == "" || len(rule.ID) > 64 || ids[rule.ID] || len(rule.Name) > 100 {
			return fmt.Errorf("rule IDs must be unique and nonempty")
		}
		ids[rule.ID] = true
		if rule.RPM < 0 || rule.RPM > 1000000 || rule.TPM < 0 || rule.TPM > 1000000000 || rule.Quota < 0 || rule.Quota > common.MaxWalletQuota || rule.Concurrent < 0 || rule.Concurrent > 10000 {
			return fmt.Errorf("rule limits are out of range")
		}
		if rule.Period != "day" && rule.Period != "month" && rule.Period != "total" {
			return fmt.Errorf("invalid quota period")
		}
		if rule.Subject != "ip" && rule.Subject != "key" && rule.Subject != "key_ip" {
			return fmt.Errorf("invalid limit subject")
		}
		if rule.Match != "*" {
			if rule.Subject == "ip" {
				if _, err := netip.ParseAddr(rule.Match); err != nil {
					if _, err = netip.ParsePrefix(rule.Match); err != nil {
						return fmt.Errorf("invalid IP or CIDR")
					}
				}
			} else if id, err := strconv.Atoi(rule.Match); err != nil || id <= 0 {
				return fmt.Errorf("key match must be an API key ID or *")
			}
		}
		if err := validateGuardModels(rule.Models); err != nil {
			return err
		}
	}
	return nil
}

func validateGuardModels(models []string) error {
	if len(models) > 100 {
		return fmt.Errorf("too many model patterns")
	}
	for _, pattern := range models {
		if strings.TrimSpace(pattern) == "" || len(pattern) > 200 {
			return fmt.Errorf("invalid model pattern")
		}
		if _, err := path.Match(pattern, "model"); err != nil {
			return fmt.Errorf("invalid model pattern")
		}
	}
	return nil
}

func RelayGuardModelMatches(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, model); matched {
			return true
		}
	}
	return false
}
