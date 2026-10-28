package service

import (
	"errors"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/relayguard"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const relayGuardContext = "relay_guard_state"
const RelayGuardReplayContext = "relay_guard_replay"
const RelayGuardReplayTokensContext = "relay_guard_replay_tokens"

var RelayResponseCache = relayguard.NewCache()
var relayBudgetOnce sync.Once
var relayBudget *relayguard.Budget

func RelayRequestBudget() *relayguard.Budget {
	relayBudgetOnce.Do(func() {
		statePath := os.Getenv("RELAY_GUARD_STATE_PATH")
		if statePath == "" {
			statePath = ".relay-guard/usage.json"
		}
		relayBudget = relayguard.NewBudget(statePath)
	})
	return relayBudget
}

type RelayGuardState struct {
	mu          sync.Mutex
	reservation *relayguard.Reservation
	settings    operation_setting.RelayGuardConfig
	Usage       *dto.Usage
	Quota       int
	tokens      int64
	transferred bool
	finished    bool
	receipt     relayguard.TeachingReceipt
}

// BeginRelayGuard is called after API-key authentication and before relay work.
// Existing user/group limits continue to run independently.
func BeginRelayGuard(c *gin.Context, resolvedModel ...string) (*RelayGuardState, *types.NewAPIError) {
	if value, ok := c.Get(relayGuardContext); ok {
		return value.(*RelayGuardState), nil
	}
	cfg := operation_setting.RelayGuardSettings()
	if cfg.Invalid {
		return nil, relayGuardError(c, errors.New("invalid request policy configuration"))
	}
	if !cfg.Limits.Enabled && !cfg.Cache.Enabled {
		return nil, nil
	}
	if common.QuotaPerUnit < 0.000001 || common.QuotaPerUnit > 1e15 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return nil, relayGuardError(c, errors.New("invalid teaching quota conversion"))
	}
	state := &RelayGuardState{settings: cfg, receipt: relayguard.TeachingReceipt{QuotaPerUnit: common.QuotaPerUnit}}
	if cfg.Limits.Enabled {
		modelName := c.GetString("original_model")
		if len(resolvedModel) > 0 {
			modelName = resolvedModel[0]
		}
		if modelName == "" && c.ContentType() == "application/json" {
			storage, err := common.GetBodyStorage(c)
			if err != nil {
				status := http.StatusBadRequest
				if common.IsRequestBodyTooLargeError(err) {
					status = http.StatusRequestEntityTooLarge
				}
				return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, status, types.ErrOptionWithSkipRetry())
			}
			body, err := storage.Bytes()
			if err != nil {
				return nil, relayGuardError(c, err)
			}
			modelName = gjson.GetBytes(body, "model").String()
		}
		if modelName == "" && strings.HasPrefix(c.Request.URL.Path, "/v1beta/models/") {
			modelName = strings.TrimPrefix(c.Request.URL.Path, "/v1beta/models/")
			modelName, _, _ = strings.Cut(modelName, ":")
		}
		reservation, err := RelayRequestBudget().Begin(cfg.Limits.Rules, c.ClientIP(), c.GetInt("token_id"), modelName, time.Now())
		if err != nil {
			return nil, relayGuardError(c, err)
		}
		state.reservation = reservation
	}
	c.Set(relayGuardContext, state)
	return state, nil
}

func relayGuardError(c *gin.Context, err error) *types.NewAPIError {
	status := http.StatusServiceUnavailable
	message := i18n.T(c, i18n.MsgRelayGuardUnavailable)
	var limit *relayguard.LimitError
	if errors.As(err, &limit) {
		status = http.StatusTooManyRequests
		dimension := limit.Dimension
		if dimension == "quota" {
			dimension = i18n.T(c, i18n.MsgRelayGuardQuota)
		} else if dimension == "concurrency" {
			dimension = i18n.T(c, i18n.MsgRelayGuardConcurrency)
		}
		message = i18n.T(c, i18n.MsgRelayGuardLimitReached, map[string]any{"Rule": limit.Rule, "Dimension": dimension})
	} else {
		logger.LogError(c, "request policy enforcement failed: "+err.Error())
	}
	return types.NewErrorWithStatusCode(errors.New(message), types.ErrorCodeInvalidRequest, status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
}

func ReserveRelayGuard(c *gin.Context, info *relaycommon.RelayInfo, quota int) *types.NewAPIError {
	if c == nil {
		return nil
	}
	value, ok := c.Get(relayGuardContext)
	if !ok {
		return nil
	}
	state := value.(*RelayGuardState)
	if state.reservation == nil {
		return nil
	}
	tokens := info.GetEstimatePromptTokens()
	if info.Request != nil {
		meta := info.Request.GetTokenCountMeta()
		if meta != nil {
			if !constant.CountToken {
				estimated, err := CountRequestToken(c, meta, info)
				if err != nil {
					return relayGuardError(c, err)
				}
				tokens = estimated
			}
			output := meta.MaxTokens
			if output <= 0 {
				output = state.settings.Limits.DefaultOutputTokens
			}
			tokens = common.QuotaFromFloat(float64(tokens) + float64(output))
		}
	}
	if c.GetBool(RelayGuardReplayContext) && !state.settings.Limits.CountCachedTokens {
		tokens = 0
	} else if value, exists := c.Get(RelayGuardReplayTokensContext); exists {
		tokens = value.(int)
	}
	if err := state.reservation.Reserve(int64(max(0, tokens)), int64(max(0, quota)), time.Now()); err != nil {
		return relayGuardError(c, err)
	}
	state.mu.Lock()
	state.tokens = int64(tokens)
	state.receipt.Group = info.UsingGroup
	state.mu.Unlock()
	return nil
}

func ObserveRelayGuardUsage(c *gin.Context, usage *dto.Usage, tokens int) {
	if c == nil {
		return
	}
	value, ok := c.Get(relayGuardContext)
	if !ok {
		return
	}
	s := value.(*RelayGuardState)
	s.mu.Lock()
	defer s.mu.Unlock()
	if usage != nil {
		copy := *usage
		copy.TotalTokens = max(0, tokens)
		s.Usage = &copy
	} else if tokens > 0 {
		s.Usage = &dto.Usage{TotalTokens: tokens}
	}
	s.tokens = int64(max(0, tokens))
}
func ObserveRelayGuardQuota(c *gin.Context, quota int, settled bool) {
	if c == nil {
		return
	}
	value, ok := c.Get(relayGuardContext)
	if !ok {
		return
	}
	s := value.(*RelayGuardState)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Quota = max(0, quota)
	s.receipt.BillingStatus = "settled"
	if !settled {
		s.receipt.BillingStatus = "failed"
	}
}

func SettleRelayGuardReplay(c *gin.Context, info *relaycommon.RelayInfo, result *relayguard.Result, percent int) {
	charge, clamp := common.QuotaRoundChecked(float64(result.Quota) * float64(percent) / 100)
	info.QuotaClamp = clamp
	if result.Usage != nil {
		ObserveRelayGuardUsage(c, result.Usage, result.Usage.TotalTokens)
	}
	if err := SettleBilling(c, info, charge); err != nil {
		logger.LogError(c, "cache replay settlement failed: "+err.Error())
	}
	other := model.NewLogOther()
	other.SetPublic("response_cache", c.Writer.Header().Get("X-New-Api-Cache"))
	other.SetPublic("cache_charge_percent", percent)
	appendRequestPath(c, info, other)
	appendBillingInfo(info, other)
	AppendRelayLogAdminInfo(c, info, other)
	attachQuotaSaturation(c, info, other)
	prompt, completion := 0, 0
	if result.Usage != nil {
		prompt, completion = result.Usage.PromptTokens, result.Usage.CompletionTokens
	}
	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, charge)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{ChannelId: info.ChannelId, TokenId: info.TokenId, TokenName: c.GetString("token_name"), ModelName: info.OriginModelName, PromptTokens: prompt, CompletionTokens: completion, Quota: charge, IsStream: info.IsStream, Group: info.UsingGroup, Other: other})
}
func (s *RelayGuardState) Snapshot() (*dto.Usage, int) {
	if s == nil {
		return nil, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Usage, s.Quota
}
func (s *RelayGuardState) Transfer() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transferred = true
}
func (s *RelayGuardState) Finish(c *gin.Context, producer bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.transferred && !producer {
		return
	}
	s.finished = true
	tokens := s.tokens
	if c.GetBool(RelayGuardReplayContext) && !s.settings.Limits.CountCachedTokens {
		tokens = 0
	}
	if s.Usage == nil && s.Quota == 0 {
		tokens = 0
	}
	receipt := s.receipt
	// Reports never turn a preflight estimate into reported usage.
	receipt.UsageObserved = true
	if c.GetBool(RelayGuardReplayContext) {
		receipt.Source = c.Writer.Header().Get("X-New-Api-Cache")
	}
	if s.Usage != nil {
		receipt.PromptTokens = int64(s.Usage.PromptTokens)
		receipt.CompletionTokens = int64(s.Usage.CompletionTokens)
		receipt.TotalTokens = int64(s.Usage.TotalTokens)
	}
	if err := s.reservation.Finish(tokens, int64(s.Quota), time.Now(), receipt); err != nil {
		logger.LogError(c, "request limit state persistence failed: "+err.Error())
	}
}
