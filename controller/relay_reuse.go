package controller

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/relayguard"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func serveReusableRelay(c *gin.Context, format types.RelayFormat) bool {
	cfg := operation_setting.RelayGuardSettings()
	if !cfg.Cache.Enabled || c.Request.Method != http.MethodPost || format != types.RelayFormatOpenAI && format != types.RelayFormatOpenAIResponses && format != types.RelayFormatClaude && format != types.RelayFormatGemini {
		return false
	}
	geminiPath := strings.HasPrefix(c.Request.URL.Path, "/v1beta/models/") && (strings.HasSuffix(c.Request.URL.Path, ":generateContent") || strings.HasSuffix(c.Request.URL.Path, ":streamGenerateContent"))
	if c.Request.URL.Path != "/v1/chat/completions" && c.Request.URL.Path != "/v1/responses" && c.Request.URL.Path != "/v1/messages" && !geminiPath {
		return false
	}
	if strings.Contains(strings.ToLower(c.GetHeader("Cache-Control")), "no-cache") || strings.Contains(strings.ToLower(c.GetHeader("Cache-Control")), "no-store") {
		c.Header("X-New-Api-Cache", "BYPASS")
		return false
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return false
	}
	body, err := storage.Bytes()
	if err != nil {
		return false
	}
	identity := c.GetInt("token_id")
	if cfg.Cache.Scope == "user" {
		identity = c.GetInt("id")
	}
	headers := c.Request.Header.Clone()
	for _, name := range []string{"Authorization", "Cookie", "X-Api-Key", "X-Goog-Api-Key", "Content-Length", "Connection"} {
		headers.Del(name)
	}
	pricingOptions := map[string]string{}
	common.OptionMapRWMutex.RLock()
	for key, value := range common.OptionMap {
		if strings.Contains(key, "Ratio") || strings.Contains(key, "Price") || strings.HasPrefix(key, "billing_setting.") {
			pricingOptions[key] = value
		}
	}
	common.OptionMapRWMutex.RUnlock()
	namespace := []any{identity, c.Request.URL.Path, c.Request.URL.RawQuery, headers, cfg.Cache, pricingOptions}
	for _, contextKey := range []constant.ContextKey{constant.ContextKeyUserGroup, constant.ContextKeyUsingGroup, constant.ContextKeyChannelId, constant.ContextKeyChannelKey, constant.ContextKeyChannelBaseUrl, constant.ContextKeyChannelModelMapping, constant.ContextKeyChannelParamOverride, constant.ContextKeyChannelHeaderOverride, constant.ContextKeyChannelSetting, constant.ContextKeyChannelOtherSetting, constant.ContextKeyChannelOrganization} {
		value, _ := c.Get(string(contextKey))
		namespace = append(namespace, value)
	}
	namespaceData, namespaceErr := common.Marshal(namespace)
	if namespaceErr != nil {
		return false
	}
	if !cfg.Cache.Stream && strings.HasSuffix(c.Request.URL.Path, ":streamGenerateContent") {
		return false
	}
	key, eligible := relayguard.RequestFingerprint(body, string(namespaceData), cfg.Cache, c.GetString("original_model"))
	if !eligible {
		c.Header("X-New-Api-Cache", "BYPASS")
		return false
	}
	// Validate every subscriber; cache hits never skip current request validation.
	request, err := helper.GetAndValidateRequest(c, format)
	if err != nil {
		return false
	}
	guard, apiErr := service.BeginRelayGuard(c)
	if apiErr != nil {
		writeReuseError(c, apiErr)
		return true
	}
	result, flight, subscription, leader := service.RelayResponseCache.Acquire(key, cfg.Cache, time.Now())
	if result == nil && flight == nil {
		c.Header("X-New-Api-Cache", "BYPASS")
		return false
	}
	if leader {
		producer := c.Copy()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), time.Duration(cfg.Cache.TimeoutSeconds)*time.Second)
		producer.Request = c.Request.Clone(ctx)
		// BodyStorageCleanup on the original connection must not close a shared producer's body.
		producerStorage, storageErr := common.CreateBodyStorage(append([]byte(nil), body...))
		if storageErr != nil {
			cancel()
			subscription.Close()
			service.RelayResponseCache.Complete(key, flight, cfg.Cache, &relayguard.Result{Status: 503, Header: http.Header{}}, false)
			return false
		}
		producer.Set(common.KeyBodyStorage, producerStorage)
		producer.Request.Body = io.NopCloser(producerStorage)
		producer.Set("request_policy_state", &service.RequestPolicyState{StartedAt: time.Now()})
		writer := &reusableResponseWriter{header: http.Header{}, status: http.StatusOK, size: -1, flight: flight, ctx: ctx}
		producer.Writer = writer
		guard.Transfer()
		flight.SetCancel(cancel)
		go func() {
			success := false
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.LogError(producer, fmt.Sprintf("shared relay panic: %v", recovered))
					apiErr := types.NewErrorWithStatusCode(errors.New(i18n.T(producer, i18n.MsgRelayGuardGenerationFailed)), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
					if value, exists := producer.Get("relay_guard_info"); exists {
						if info, ok := value.(*relaycommon.RelayInfo); ok && info.Billing != nil {
							info.Billing.Refund(producer)
						}
					}
					if writer.Written() && strings.HasPrefix(writer.Header().Get("Content-Type"), "text/event-stream") {
						writeRelayStreamError(producer, format, apiErr)
					} else if !writer.Written() {
						writeReuseError(producer, apiErr)
					}
				}
				usage, quota := guard.Snapshot()
				completed := &relayguard.Result{Status: writer.Status(), Header: writer.Header().Clone(), Usage: usage, Quota: quota}
				guard.Finish(producer, true)
				common.CleanupBodyStorage(producer)
				service.RelayResponseCache.Complete(key, flight, cfg.Cache, completed, success)
				cancel()
			}()
			relayOnce(producer, format)
			streamStatus, _ := common.GetContextKeyType[*relaycommon.StreamStatus](producer, constant.ContextKeyResponseStreamStatus)
			if value, exists := producer.Get("relay_guard_info"); exists {
				if info, ok := value.(*relaycommon.RelayInfo); ok && info.StreamStatus != nil {
					streamStatus = info.StreamStatus
				}
			}
			success = writer.Status() == http.StatusOK && writer.Size() > 0 && (streamStatus == nil || streamStatus.IsNormalEnd() && !streamStatus.HasErrors() && (streamStatus.ResponseOutcome() == "" || streamStatus.ResponseOutcome() == "completed")) && ctx.Err() == nil && !producer.GetBool("relay_guard_billing_failed") && !producer.GetBool("relay_guard_failed")
		}()
		c.Header("X-New-Api-Cache", "MISS")
	} else if result != nil {
		c.Header("X-New-Api-Cache", "HIT")
	} else {
		c.Header("X-New-Api-Cache", "SHARED")
	}
	var replayInfo *relaycommon.RelayInfo
	if !leader {
		c.Set(service.RelayGuardReplayContext, true)
		replayInfo, err = relaycommon.GenRelayInfo(c, format, request, nil)
		if err != nil {
			if subscription != nil {
				subscription.Close()
			}
			writeReuseError(c, types.NewError(err, types.ErrorCodeInvalidRequest))
			return true
		}
		replayInfo.InitChannelMeta(c)
		if apiErr = relay.PrepareRequestBilling(c, replayInfo); apiErr != nil {
			if subscription != nil {
				subscription.Close()
			}
			writeReuseError(c, apiErr)
			return true
		}
		preCharge := replayInfo.PriceData.QuotaToPreConsume
		if result != nil {
			preCharge = result.Quota
			if result.Usage != nil {
				c.Set(service.RelayGuardReplayTokensContext, result.Usage.TotalTokens)
			}
		}
		charge, clamp := common.QuotaRoundChecked(float64(preCharge) * float64(cfg.Cache.ChargePercent) / 100)
		if clamp != nil {
			replayInfo.QuotaClamp = clamp
		}
		if cfg.Cache.ChargePercent > 0 {
			apiErr = service.PreConsumeBilling(c, charge, replayInfo)
		} else {
			apiErr = service.ReserveRelayGuard(c, replayInfo, 0)
		}
		if apiErr != nil {
			if subscription != nil {
				subscription.Close()
			}
			writeReuseError(c, apiErr)
			return true
		}
	}
	if subscription != nil {
		defer subscription.Close()
	}
	var deliveryErr error
	if result != nil {
		copyReuseHeaders(c, result.Status, result.Header)
		for _, frame := range result.Frames {
			if c.Request.Context().Err() != nil {
				deliveryErr = c.Request.Context().Err()
				break
			}
			if _, deliveryErr = c.Writer.Write(frame); deliveryErr != nil {
				break
			}
			if strings.HasPrefix(result.Header.Get("Content-Type"), "text/event-stream") {
				if deliveryErr = helper.FlushWriter(c); deliveryErr != nil {
					break
				}
				if cfg.Cache.ReplayDelayMS > 0 {
					timer := time.NewTimer(time.Duration(cfg.Cache.ReplayDelayMS) * time.Millisecond)
					select {
					case <-timer.C:
					case <-c.Request.Context().Done():
						timer.Stop()
						deliveryErr = c.Request.Context().Err()
					}
					if deliveryErr != nil {
						break
					}
				}
			}
		}
	} else {
		status, header, headerErr := subscription.Headers(c.Request.Context())
		if headerErr == nil {
			copyReuseHeaders(c, status, header)
		} else {
			deliveryErr = headerErr
		}
		for deliveryErr == nil {
			frame, more, nextErr := subscription.Next(c.Request.Context())
			if nextErr != nil {
				deliveryErr = nextErr
				break
			}
			if !more {
				break
			}
			if _, deliveryErr = c.Writer.Write(frame); deliveryErr != nil {
				break
			}
			if strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") {
				deliveryErr = helper.FlushWriter(c)
			}
		}
		result = subscription.Result()
	}
	if !leader && replayInfo != nil {
		if deliveryErr == nil && result != nil && result.Successful {
			service.SettleRelayGuardReplay(c, replayInfo, result, cfg.Cache.ChargePercent)
		} else if replayInfo.Billing != nil {
			replayInfo.Billing.Refund(c)
		}
	}
	if deliveryErr != nil && !c.Writer.Written() {
		c.Status(http.StatusBadGateway)
	}
	if deliveryErr != nil || result != nil && !result.Successful {
		status := relaycommon.NewStreamStatus()
		status.MarkFailed("response_reuse_failed", "", http.StatusBadGateway)
		common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, status)
	}
	return true
}

func writeReuseError(c *gin.Context, err *types.NewAPIError) {
	if err.StatusCode == 429 {
		c.Header("Retry-After", "60")
	}
	c.JSON(err.StatusCode, gin.H{"error": err.ToOpenAIError(common.UpstreamAddresses(c)...)})
}
func copyReuseHeaders(c *gin.Context, status int, header http.Header) {
	// Never replay another request's cookies, credentials, trace IDs or upstream headers.
	for _, name := range []string{"Content-Type", "Cache-Control", "X-Accel-Buffering"} {
		if value := header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
	c.Header("Cache-Control", "no-store")
	c.Status(status)
}

type reusableResponseWriter struct {
	header       http.Header
	status, size int
	flight       *relayguard.Flight
	ctx          context.Context
}

func (w *reusableResponseWriter) Header() http.Header { return w.header }
func (w *reusableResponseWriter) WriteHeader(status int) {
	if status > 0 && !w.Written() {
		w.status = status
	}
}
func (w *reusableResponseWriter) WriteHeaderNow() {
	if !w.Written() {
		w.size = 0
		w.flight.Publish(w.status, w.header, nil)
	}
}
func (w *reusableResponseWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	w.WriteHeaderNow()
	w.size += len(data)
	w.flight.Publish(w.status, w.header, data)
	return len(data), nil
}
func (w *reusableResponseWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }
func (w *reusableResponseWriter) Status() int                          { return w.status }
func (w *reusableResponseWriter) Size() int                            { return w.size }
func (w *reusableResponseWriter) Written() bool                        { return w.size >= 0 }
func (w *reusableResponseWriter) Flush()                               { w.WriteHeaderNow() }
func (w *reusableResponseWriter) FlushError() error {
	w.WriteHeaderNow()
	return w.ctx.Err()
}
func (w *reusableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("shared relay does not support WebSockets")
}
func (w *reusableResponseWriter) CloseNotify() <-chan bool { ch := make(chan bool); return ch }
func (w *reusableResponseWriter) Pusher() http.Pusher      { return nil }
