package controller

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/relayguard"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReusableRelayUsesOneUpstreamAndRetainsPerClientBilling(t *testing.T) {
	require.NoError(t, i18n.Init())
	t.Setenv("RELAY_GUARD_STATE_PATH", filepath.Join(t.TempDir(), "usage.json"))
	user, token := setupResponsesWSRequestTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.Option{}))
	t.Cleanup(func() { require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error) })
	require.NoError(t, model.DB.Model(user).Update("setting", `{"billing_preference":"wallet_only"}`).Error)
	require.NoError(t, model.DB.Model(token).Update("remain_quota", 1000).Error)
	token.RemainQuota = 1000
	previousCache := service.RelayResponseCache
	service.RelayResponseCache = relayguard.NewCache()
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	previousPolicy := common.OptionMap[operation_setting.RelayGuardOption]
	cfg := operation_setting.DefaultRelayGuardConfig()
	cfg.Cache.Enabled, cfg.Cache.Stream, cfg.Cache.Coalesce = true, true, true
	cfg.Cache.ChargePercent = 50
	cfg.Limits.Enabled = true
	cfg.Limits.Rules = []operation_setting.RelayLimitRule{
		{ID: "integration-ip", Name: "IP", Enabled: true, Subject: "ip", Match: "*", Period: "day", RPM: 10, TPM: 100000, Quota: 1000, Concurrent: 3},
		{ID: "integration-key", Name: "Key", Enabled: true, Subject: "key", Match: "*", Period: "day", RPM: 5, TPM: 100000, Quota: 1000, Concurrent: 3},
	}
	encoded, err := common.Marshal(cfg)
	require.NoError(t, err)
	common.OptionMap[operation_setting.RelayGuardOption] = string(encoded)
	common.OptionMapRWMutex.Unlock()
	// Two classrooms share this key, while their IPs and resets stay isolated.
	classAPI := gin.New()
	classAPI.POST("/classes", StartRelayClassroom)
	classAPI.GET("/classes/:id/bill", GetRelayClassroomBill)
	classBody, err := common.Marshal(relayguard.ClassroomInput{Name: "Class A", KeyID: token.Id, IPs: []string{"192.0.2.8"}, RuleIDs: []string{"integration-ip"}})
	require.NoError(t, err)
	createResponse := httptest.NewRecorder()
	classRequest := httptest.NewRequest(http.MethodPost, "/classes", strings.NewReader(string(classBody)))
	classRequest.Header.Set("Content-Type", "application/json")
	classAPI.ServeHTTP(createResponse, classRequest)
	require.Equal(t, http.StatusOK, createResponse.Code, createResponse.Body.String())
	var created struct {
		Data relayguard.Classroom `json:"data"`
	}
	require.NoError(t, common.Unmarshal(createResponse.Body.Bytes(), &created))
	classA := created.Data
	classB, err := service.RelayRequestBudget().StartClassroom(relayguard.ClassroomInput{Name: "Class B", KeyID: token.Id, IPs: []string{"127.0.0.1"}, RuleIDs: []string{"integration-ip"}}, cfg.Limits.Rules, time.Now())
	require.NoError(t, err)
	previousLog, previousCount := common.LogConsumeEnabled, constant.CountToken
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 60
	common.LogConsumeEnabled, constant.CountToken = true, true
	previousModelPrices := ratio_setting.GetModelPriceCopy()
	priceJSON, err := common.Marshal(previousModelPrices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"reuse-test":0.0001}`))
	t.Cleanup(func() {
		service.RelayResponseCache = previousCache
		common.LogConsumeEnabled, constant.CountToken = previousLog, previousCount
		constant.StreamingTimeout = previousTimeout
		common.OptionMapRWMutex.Lock()
		common.OptionMap[operation_setting.RelayGuardOption] = previousPolicy
		common.OptionMapRWMutex.Unlock()
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(priceJSON)))
	})
	var upstreamCalls atomic.Int32
	var upstreamCanceled atomic.Bool
	releaseShared := make(chan struct{})
	var releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat-test\",\"object\":\"chat.completion.chunk\",\"model\":\"reuse-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat-test\",\"object\":\"chat.completion.chunk\",\"model\":\"reuse-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"cached hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		if call == 2 {
			select {
			case <-releaseShared:
			case <-r.Context().Done():
				upstreamCanceled.Store(true)
				return
			}
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"chat-test\",\"model\":\"reuse-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	defer releaseOnce.Do(func() { close(releaseShared) })
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: "reuse", Key: "upstream-test", Status: common.ChannelStatusEnabled, Models: "reuse-test", Group: "default", BaseURL: common.GetPointer(upstream.URL)}
	require.NoError(t, model.DB.Create(channel).Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Unscoped().Delete(channel).Error) })
	service.InitHttpClient()
	engine := gin.New()
	engine.Use(middleware.BodyStorageCleanup(), middleware.TokenAuth())
	engine.POST("/v1/chat/completions", middleware.ModelRequestRateLimit(), func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "reuse-test"))
		Relay(c, types.RelayFormatOpenAI)
	})
	newRequest := func(ctx context.Context, url, prompt string) *http.Request {
		body, err := common.Marshal(gin.H{"model": "reuse-test", "stream": true, "messages": []gin.H{{"role": "user", "content": prompt}}})
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", strings.NewReader(string(body)))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer sk-"+token.Key)
		request.Header.Set("User-Agent", "relay-guard-test")
		request.Header.Set("Accept-Encoding", "identity")
		request.RemoteAddr = "192.0.2.8:12345"
		return request
	}
	call := func(prompt string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newRequest(context.Background(), "http://gateway.local", prompt)
		request.URL.Scheme, request.URL.Host = "", ""
		engine.ServeHTTP(recorder, request)
		return recorder
	}
	first := call("hello")
	require.Equal(t, 200, first.Code, first.Body.String())
	assert.Equal(t, "MISS", first.Header().Get("X-New-Api-Cache"))
	assert.Contains(t, first.Body.String(), "cached hello")
	second := call("hello")
	require.Equal(t, 200, second.Code, second.Body.String())
	assert.Equal(t, "HIT", second.Header().Get("X-New-Api-Cache"))
	assert.Equal(t, first.Body.String(), second.Body.String())
	assert.EqualValues(t, 1, upstreamCalls.Load())
	server := httptest.NewServer(engine)
	defer func() {
		releaseOnce.Do(func() { close(releaseShared) })
		server.Close()
	}()
	client := &http.Client{Timeout: 10 * time.Second}
	ctx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	sharedFirst, err := client.Do(newRequest(ctx, server.URL, "shared"))
	require.NoError(t, err)
	defer sharedFirst.Body.Close()
	require.Equal(t, "MISS", sharedFirst.Header.Get("X-New-Api-Cache"))
	sharedSecond, err := client.Do(newRequest(context.Background(), server.URL, "shared"))
	require.NoError(t, err)
	defer sharedSecond.Body.Close()
	require.Equal(t, "SHARED", sharedSecond.Header.Get("X-New-Api-Cache"))
	cancelFirst()
	releaseOnce.Do(func() { close(releaseShared) })
	sharedBody, err := io.ReadAll(sharedSecond.Body)
	require.NoError(t, err)
	assert.Contains(t, string(sharedBody), "cached hello")
	assert.Contains(t, string(sharedBody), "[DONE]")
	assert.False(t, upstreamCanceled.Load(), "the remaining client must keep its upstream generation")
	third := call("shared")
	assert.Equal(t, "HIT", third.Header().Get("X-New-Api-Cache"))
	assert.EqualValues(t, 2, upstreamCalls.Load())
	limited := call("hello")
	assert.Equal(t, http.StatusTooManyRequests, limited.Code, "cache hits must still pass current key/IP limits")
	assert.Equal(t, "60", limited.Header().Get("Retry-After"))
	for _, language := range []struct{ code, message string }{
		{"en", "Request limit reached"},
		{"zh-CN", "已达到请求限制"},
		{"zh-TW", "已達到請求限制"},
	} {
		request := newRequest(context.Background(), "http://gateway.local", "hello")
		request.URL.Scheme, request.URL.Host = "", ""
		request.Header.Set("Accept-Language", language.code)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
		assert.Contains(t, recorder.Body.String(), language.message)
	}
	usage, err := service.RelayRequestBudget().Stats(time.Now())
	require.NoError(t, err)
	for _, row := range usage {
		if row.RuleID == "integration-key" {
			assert.EqualValues(t, 175, row.Quota)
			assert.EqualValues(t, 5, row.Requests)
			assert.EqualValues(t, 60, row.Tokens)
			assert.Zero(t, row.Concurrent)
		}
	}
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Order("id").Find(&logs).Error)
	require.Len(t, logs, 5)
	assert.EqualValues(t, 50, logs[0].Quota)
	assert.EqualValues(t, 25, logs[1].Quota)
	remaining, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, user.Quota-175, remaining)
	remainingToken, err := model.GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	assert.Equal(t, token.RemainQuota-175, remainingToken.RemainQuota)
	// The default zero-percent replay still checks limits and records usage,
	// while preserving both balances after the original generation was paid.
	cfg.Cache.ChargePercent = 0
	cfg.Limits.CountCachedTokens = false
	cfg.Limits.Rules[1].RPM = 10
	encoded, err = common.Marshal(cfg)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	common.OptionMap[operation_setting.RelayGuardOption] = string(encoded)
	common.OptionMapRWMutex.Unlock()
	require.Equal(t, http.StatusOK, call("free-replay").Code)
	freeReplay := call("free-replay")
	require.Equal(t, http.StatusOK, freeReplay.Code)
	assert.Equal(t, "HIT", freeReplay.Header().Get("X-New-Api-Cache"))
	assert.EqualValues(t, 3, upstreamCalls.Load())
	remaining, err = model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, user.Quota-225, remaining)
	remainingToken, err = model.GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	assert.Equal(t, token.RemainQuota-225, remainingToken.RemainQuota)
	logs = nil
	require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Order("id").Find(&logs).Error)
	require.Len(t, logs, 7)
	assert.Zero(t, logs[6].Quota)
	aBill, err := service.RelayRequestBudget().ClassroomReport(classA.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 5, aBill.Summary.Requests)
	assert.EqualValues(t, 150, aBill.Summary.Quota)
	assert.EqualValues(t, 60, aBill.Summary.Tokens, "free replay tokens remain in the teaching report")
	assert.EqualValues(t, 3, aBill.Summary.ReusedRequests)
	assert.Zero(t, aBill.Summary.Unsettled)
	bBill, err := service.RelayRequestBudget().ClassroomReport(classB.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 2, bBill.Summary.Requests)
	assert.EqualValues(t, 75, bBill.Summary.Quota)
	_, err = service.RelayRequestBudget().ResetClassroom(classA.ID, time.Now())
	require.NoError(t, err)
	aBill, err = service.RelayRequestBudget().ClassroomReport(classA.ID, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 150, aBill.Summary.Quota, "reset preserves all historical receipts")
	assert.Len(t, aBill.Resets, 1)
	billResponse := httptest.NewRecorder()
	classAPI.ServeHTTP(billResponse, httptest.NewRequest(http.MethodGet, "/classes/"+classA.ID+"/bill", nil))
	require.Equal(t, http.StatusOK, billResponse.Code)
	var returned struct {
		Data relayguard.ClassroomBill `json:"data"`
	}
	require.NoError(t, common.Unmarshal(billResponse.Body.Bytes(), &returned))
	assert.EqualValues(t, 150, returned.Data.Summary.Quota)
	invalidDateResponse := httptest.NewRecorder()
	classAPI.ServeHTTP(invalidDateResponse, httptest.NewRequest(http.MethodGet, "/classes/"+classA.ID+"/bill?from=bad", nil))
	assert.Equal(t, http.StatusBadRequest, invalidDateResponse.Code)
}

func TestRelayKeepAliveDuringImagePreparation(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	previousCount, previousMedia := constant.CountToken, constant.GetMediaToken
	fetchSettings := system_setting.GetFetchSetting()
	previousFetchSettings := *fetchSettings
	fetchSettings.AllowPrivateIp = true
	fetchSettings.AllowedPorts = []string{"1-65535"}
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
	constant.CountToken, constant.GetMediaToken = true, true
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
		constant.CountToken, constant.GetMediaToken = previousCount, previousMedia
		*fetchSettings = previousFetchSettings
	})
	service.InitHttpClient()

	imageRequested := make(chan struct{})
	releaseImage := make(chan struct{})
	var releaseOnce sync.Once
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(imageRequested)
		<-releaseImage
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer imageServer.Close()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyOriginalModel, "claude-sonnet-4-20250514")
		Relay(c, types.RelayFormatClaude)
	})
	gateway := httptest.NewServer(engine)
	defer gateway.Close()
	defer releaseOnce.Do(func() { close(releaseImage) })

	body := `{"model":"claude-sonnet-4-20250514","max_tokens":16,"stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"` + imageServer.URL + `"}}]}]}`
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader(body))
	require.NoError(t, err, "the gateway must send keepalive while its image download is blocked")
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	reader := bufio.NewReader(response.Body)
	for range 2 {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, ": PING\n", line)
		line, readErr = reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, "\n", line)
	}
	select {
	case <-imageRequested:
	default:
		t.Fatal("the request never reached image preparation")
	}
	releaseOnce.Do(func() { close(releaseImage) })
	remaining, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(remaining), "event: error\n")
	require.Contains(t, string(remaining), `"type":"error"`)
	require.Contains(t, string(remaining), "data: ")
}

func TestRelayKeepAliveAcrossUpstreamRetries(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})
	service.InitHttpClient()
	var attempts atomic.Int32
	retryStarted, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		close(retryStarted)
		select {
		case <-release:
			_, _ = io.WriteString(w, "data: ok\n\n")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		stop := helper.StartStreamKeepAlive(c, 10*time.Millisecond)
		defer stop()
		if !assert.NoError(t, http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(5*time.Second))) {
			return
		}
		info := &relaycommon.RelayInfo{IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
		for range 2 {
			request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
			if !assert.NoError(t, err) {
				return
			}
			response, err := channel.DoRequest(c, request, info)
			if !assert.NoError(t, err) {
				return
			}
			if response.StatusCode == http.StatusTooManyRequests {
				response.Body.Close()
				continue
			}
			_, _ = io.Copy(c.Writer, response.Body)
			response.Body.Close()
		}
	})
	gateway := httptest.NewServer(engine)
	defer gateway.Close()
	defer releaseOnce.Do(func() { close(release) })
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer response.Body.Close()
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("the upstream retry did not start")
	}
	reader := bufio.NewReader(response.Body)
	for range 2 {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, ": PING\n", line)
		line, readErr = reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, "\n", line)
	}
	releaseOnce.Do(func() { close(release) })
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(body), "data: ok\n\n")
	require.Equal(t, int32(2), attempts.Load())
}

func TestRelayKeepAlivePreservesValidationErrors(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled := settings.PingIntervalEnabled
	settings.PingIntervalEnabled = true
	t.Cleanup(func() { settings.PingIntervalEnabled = previousEnabled })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-20250514","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	Relay(c, types.RelayFormatClaude)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, recorder.Body.String(), ": PING")
}

func TestStreamKeepAlivePreservesFastHTTPFailures(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 60
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})
	service.InitHttpClient()
	for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"request rejected"}`)
			}))
			defer upstream.Close()
			engine := gin.New()
			engine.POST("/v1/messages", func(c *gin.Context) {
				stop := helper.StartStreamKeepAlive(c, time.Minute)
				defer stop()
				request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
				if !assert.NoError(t, err) {
					return
				}
				info := &relaycommon.RelayInfo{IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
				response, err := channel.DoRequest(c, request, info)
				if !assert.NoError(t, err) {
					return
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if assert.NoError(t, err) {
					c.Header("Content-Type", "application/json")
					c.Data(response.StatusCode, "application/json", body)
				}
			})
			gateway := httptest.NewServer(engine)
			defer gateway.Close()
			client := &http.Client{Timeout: 4 * time.Second}
			response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, status, response.StatusCode)
			require.Equal(t, "application/json", response.Header.Get("Content-Type"))
			require.JSONEq(t, `{"error":"request rejected"}`, string(body))
		})
	}
}

func TestRelayKeepAliveHonorsStreamSettings(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled := settings.PingIntervalEnabled
	previousCount, previousMedia, previousNonStreamMedia := constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream
	constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream = true, true, true
	t.Cleanup(func() {
		settings.PingIntervalEnabled = previousEnabled
		constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream = previousCount, previousMedia, previousNonStreamMedia
	})
	for _, test := range []struct {
		name    string
		enabled bool
		stream  bool
	}{
		{name: "ping disabled", enabled: false, stream: true},
		{name: "non-streaming", enabled: true, stream: false},
		{name: "fast streaming failure", enabled: true, stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings.PingIntervalEnabled = test.enabled
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := `{"model":"claude-sonnet-4-20250514","max_tokens":16,"stream":` + strconv.FormatBool(test.stream) + `,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"!"}}]}]}`
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "claude-sonnet-4-20250514")
			Relay(c, types.RelayFormatClaude)
			require.Equal(t, http.StatusInternalServerError, recorder.Code)
			require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
			require.NotContains(t, recorder.Body.String(), ": PING")
			var response map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, "error", response["type"])
		})
	}
}

func TestRelayStreamErrorAfterKeepAlive(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(format), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
			helper.SetEventStreamHeaders(c)
			require.NoError(t, helper.PingData(c))
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://provider.example")
			common.SetContextKey(c, constant.ContextKeyUpstreamRequestOrigin, "https://provider.example")
			apiError := types.NewErrorWithStatusCode(errors.New("upstream rejected input at https://provider.example/v1/messages"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
			writeRelayStreamError(c, format, apiError)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
			require.Contains(t, recorder.Body.String(), "upstream rejected input")
			require.NotContains(t, recorder.Body.String(), "provider.example")
			status, ok := common.GetContextKeyType[*relaycommon.StreamStatus](c, constant.ContextKeyResponseStreamStatus)
			require.True(t, ok)
			require.True(t, status.ResponseFailed(), "stream errors must not consume the successful-request rate limit")
			require.Equal(t, http.StatusBadRequest, status.OutcomeSnapshot().ErrorStatus)
			if format == types.RelayFormatClaude {
				require.Contains(t, recorder.Body.String(), "event: error\n")
			} else {
				require.Contains(t, recorder.Body.String(), "data: [DONE]")
			}
		})
	}
}

func TestStreamKeepAliveCancellation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil).WithContext(requestContext)
	stop := helper.StartStreamKeepAlive(c, time.Hour)
	cancel()
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not stop after client cancellation")
	}
	_, err := c.Writer.WriteString("data: done\n\n")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(recorder.Body.String(), "data: done\n\n"))
}
