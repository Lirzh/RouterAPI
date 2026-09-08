package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/balancer"
	"github.com/routerapi/backend/internal/billing"
	"github.com/routerapi/backend/internal/errkit"
	"github.com/routerapi/backend/internal/inbound"
	"github.com/routerapi/backend/internal/middleware"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/provider"
	"github.com/routerapi/backend/internal/ratelimit"
	"github.com/routerapi/backend/internal/stats"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ProxyHandler 代理转发：支持 OpenAI / Anthropic / Gemini 三种入站协议（hub-and-spoke 转换）。
type ProxyHandler struct {
	db             *gorm.DB
	balancer       *balancer.Balancer
	limiter        *ratelimit.Limiter
	retryTimes     int
	billingEnabled bool // 积分钱包开关：启用后做余额门槛 + 请求后结算扣减
}

func NewProxyHandler(db *gorm.DB, bal *balancer.Balancer, limiter *ratelimit.Limiter, retryTimes int, billingEnabled bool) *ProxyHandler {
	if retryTimes < 0 {
		retryTimes = 0
	}
	if limiter == nil {
		limiter = ratelimit.New()
	}
	return &ProxyHandler{db: db, balancer: bal, limiter: limiter, retryTimes: retryTimes, billingEnabled: billingEnabled}
}

// ChatCompletions /v1/chat/completions（OpenAI 入站）。
func (h *ProxyHandler) ChatCompletions(c *gin.Context) {
	h.handleInbound(c, inbound.OpenAI, "", nil)
}

// Messages /v1/messages（Anthropic 入站）。
func (h *ProxyHandler) Messages(c *gin.Context) {
	h.handleInbound(c, inbound.Anthropic, "", nil)
}

// GeminiGenerate /v1beta/models/*path（Gemini 入站；模型名与动作在路径中）。
func (h *ProxyHandler) GeminiGenerate(c *gin.Context) {
	pathModel, stream := parseGeminiPath(c.Param("path"), c.Query("alt"))
	h.handleInbound(c, inbound.Gemini, pathModel, &stream)
}

// parseGeminiPath 从 "/{model}:generateContent" 或 "/{model}:streamGenerateContent" 解析模型名与是否流式。
func parseGeminiPath(rawPath, alt string) (string, bool) {
	p := strings.TrimPrefix(rawPath, "/")
	stream := alt == "sse"
	model := p
	if idx := strings.Index(p, ":"); idx >= 0 {
		model = p[:idx]
		action := p[idx+1:]
		if strings.Contains(strings.ToLower(action), "stream") {
			stream = true
		}
	}
	// 去掉可能残留的 query
	if idx := strings.Index(model, "?"); idx >= 0 {
		model = model[:idx]
	}
	return model, stream
}

// applyBindingParams 将绑定的默认参数（JSON）并入请求，仅在客户端未显式设置对应字段时生效。
// 作用于每次尝试的请求副本，不同绑定的参数互不污染。
func applyBindingParams(req *provider.ChatRequest, paramsJSON string) {
	paramsJSON = strings.TrimSpace(paramsJSON)
	if paramsJSON == "" {
		return
	}
	var p map[string]interface{}
	if err := json.Unmarshal([]byte(paramsJSON), &p); err != nil {
		return
	}
	if req.Temperature == nil {
		if f, ok := jsonFloat(p["temperature"]); ok {
			req.Temperature = &f
		}
	}
	if req.TopP == nil {
		if f, ok := jsonFloat(p["top_p"]); ok {
			req.TopP = &f
		}
	}
	if req.MaxTokens == nil {
		if i, ok := jsonInt(p["max_tokens"]); ok {
			req.MaxTokens = &i
		}
	}
	if req.FrequencyPenalty == nil {
		if f, ok := jsonFloat(p["frequency_penalty"]); ok {
			req.FrequencyPenalty = &f
		}
	}
	if req.PresencePenalty == nil {
		if f, ok := jsonFloat(p["presence_penalty"]); ok {
			req.PresencePenalty = &f
		}
	}
	if len(req.Stop) == 0 {
		switch s := p["stop"].(type) {
		case []interface{}:
			for _, item := range s {
				if str, ok := item.(string); ok {
					req.Stop = append(req.Stop, str)
				}
			}
		case string:
			if s != "" {
				req.Stop = append(req.Stop, s)
			}
		}
	}
}

// jsonFloat 宽松提取 JSON 数值为 float64。
func jsonFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

// jsonInt 宽松提取 JSON 数值为 int。
func jsonInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// authenticateAPIKey 通过 API Key 认证调用方（支持 Authorization: Bearer 与 x-api-key）。
func (h *ProxyHandler) authenticateAPIKey(c *gin.Context) (*model.APIKey, *errkit.APIError) {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		auth = c.GetHeader("X-Api-Key")
	}
	key := strings.TrimPrefix(auth, "Bearer ")
	key = strings.TrimSpace(key)
	// Gemini 亦支持 ?key=
	if key == "" {
		key = c.Query("key")
	}
	if key == "" {
		return nil, errkit.ClientError(http.StatusUnauthorized, "未提供 API Key")
	}

	var apiKey model.APIKey
	if err := h.db.Where("key = ? AND status = 1", key).First(&apiKey).Error; err != nil {
		return nil, errkit.ClientError(http.StatusUnauthorized, "API Key 无效")
	}
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return nil, errkit.ClientError(http.StatusUnauthorized, "API Key 已过期")
	}
	if !KeyAllowsIP(&apiKey, c.ClientIP()) {
		return nil, errkit.ClientError(http.StatusForbidden, "来源 IP 不在该 Key 的白名单内")
	}

	h.db.Model(&apiKey).Update("last_used_at", time.Now())
	return &apiKey, nil
}

// resolveModel 解析虚拟模型及其启用绑定的供应商 ID 列表（用于授权判定）。
func (h *ProxyHandler) resolveModel(name string) (*model.Model, []uint, error) {
	var m model.Model
	if err := h.db.Where("name = ? AND enabled = ?", name, true).First(&m).Error; err != nil {
		return nil, nil, err
	}
	var bindings []model.ModelBinding
	h.db.Where("model_id = ? AND enabled = ?", m.ID, true).Find(&bindings)
	vendorIDs := make([]uint, 0, len(bindings))
	for _, b := range bindings {
		vendorIDs = append(vendorIDs, b.VendorID)
	}
	return &m, vendorIDs, nil
}

// handleInbound 统一入站处理：解析 -> 授权 -> 限流 -> 路由重试 -> 协议转换 -> 记录。
func (h *ProxyHandler) handleInbound(c *gin.Context, protocol inbound.Protocol, pathModel string, forceStream *bool) {
	startTime := time.Now()
	requestID := middleware.GetRequestID(c)
	clientIP := c.ClientIP()
	userAgent := middleware.GetUserAgent(c)
	protoStr := string(protocol)

	apiKey, authErr := h.authenticateAPIKey(c)
	if authErr != nil {
		h.respondError(c, authErr, requestID, protocol)
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.respondError(c, errkit.ClientError(http.StatusBadRequest, "读取请求体失败"), requestID, protocol)
		return
	}

	irReq, perr := inbound.Parse(protocol, body, pathModel)
	if perr != nil {
		h.respondError(c, errkit.ClientError(http.StatusBadRequest, "请求格式错误: "+perr.Error()), requestID, protocol)
		return
	}
	if forceStream != nil {
		irReq.Stream = *forceStream
	}

	virtualModel := strings.TrimSpace(irReq.Model)
	if virtualModel == "" {
		h.respondError(c, errkit.ClientError(http.StatusBadRequest, "未指定模型"), requestID, protocol)
		return
	}

	// 解析模型 + 授权判定
	_, vendorIDs, err := h.resolveModel(virtualModel)
	if err != nil {
		ae := errkit.New(http.StatusNotFound, errkit.TypeClient, "model_not_found", "模型不存在或已停用: "+virtualModel, err).WithSkipRetry()
		h.respondError(c, ae, requestID, protocol)
		h.logFailure(requestID, apiKey, nil, virtualModel, "", ae, 0, startTime, clientIP, userAgent, protoStr)
		return
	}
	if !KeyAllowsModel(apiKey, virtualModel, vendorIDs) {
		ae := errkit.ClientError(http.StatusForbidden, "API Key 无权访问模型: "+virtualModel)
		h.respondError(c, ae, requestID, protocol)
		h.logFailure(requestID, apiKey, nil, virtualModel, "", ae, 0, startTime, clientIP, userAgent, protoStr)
		return
	}

	// 余额门槛：启用钱包且 Key 有归属人时，余额 <=0 直接拒绝（不发起上游，不计费）
	if h.billingEnabled && apiKey.OwnerUserID > 0 {
		if balance, berr := billing.Balance(h.db, apiKey.OwnerUserID); berr == nil && balance <= 0 {
			ae := errkit.New(http.StatusPaymentRequired, errkit.TypeClient, "insufficient_credits", "积分余额不足，请联系管理员充值", nil).WithSkipRetry()
			h.respondError(c, ae, requestID, protocol)
			h.logFailure(requestID, apiKey, nil, virtualModel, "", ae, 0, startTime, clientIP, userAgent, protoStr)
			return
		}
	}

	// 每 Key 花费上限：累计消耗达到 CreditsCap 则拒绝（0=不限）
	if h.billingEnabled && apiKey.CreditsCap > 0 {
		var spent struct{ Spent int64 }
		h.db.Model(&model.RequestLog{}).Where("api_key_id = ?", apiKey.ID).
			Select("COALESCE(SUM(credits_cost), 0) AS spent").Scan(&spent)
		if spent.Spent >= apiKey.CreditsCap {
			ae := errkit.New(http.StatusForbidden, errkit.TypeClient, "credits_cap_exceeded", "该 API Key 已达花费上限", nil).WithSkipRetry()
			h.respondError(c, ae, requestID, protocol)
			h.logFailure(requestID, apiKey, nil, virtualModel, "", ae, 0, startTime, clientIP, userAgent, protoStr)
			return
		}
	}

	// 每 Key 限流（fail-open：未配置则不限）
	keyLimitKey := "key:" + strconv.FormatUint(uint64(apiKey.ID), 10)
	if !h.limiter.Allow(keyLimitKey, apiKey.RateLimitRPM) {
		c.Header("Retry-After", strconv.Itoa(h.limiter.RetryAfter(keyLimitKey)))
		ae := errkit.New(http.StatusTooManyRequests, errkit.TypeClient, "rate_limited", "API Key 请求过于频繁，请稍后重试", nil).WithSkipRetry()
		h.respondError(c, ae, requestID, protocol)
		h.logFailure(requestID, apiKey, nil, virtualModel, "", ae, 0, startTime, clientIP, userAgent, protoStr)
		return
	}

	// 跨供应商重试闭环
	excluded := make(map[uint]bool)
	var lastErr *errkit.APIError
	var lastRoute *balancer.RouteResult

	for attempt := 0; attempt <= h.retryTimes; attempt++ {
		route, rerr := h.balancer.Route(virtualModel, attempt, excluded)
		if rerr != nil {
			lastErr = errkit.New(http.StatusServiceUnavailable, errkit.TypeChannel, "channel:no_route", rerr.Error(), rerr)
			break
		}
		lastRoute = route
		excluded[route.Binding.ID] = true

		// 供应商级限流（我方主动限流，不计入供应商错误；超限则换下一个供应商）
		vendorLimitKey := "vendor:" + strconv.FormatUint(uint64(route.Vendor.ID), 10)
		if !h.limiter.Allow(vendorLimitKey, route.Vendor.MaxRPM) {
			lastErr = errkit.New(http.StatusTooManyRequests, errkit.TypeChannel, "channel:rate_limited", "供应商达到限流阈值", nil)
			if attempt < h.retryTimes {
				continue
			}
			break
		}

		prov, perr := provider.NewFromVendor(route.Vendor)
		if perr != nil {
			lastErr = errkit.InternalError("创建适配器失败: "+perr.Error(), perr)
			h.balancer.MarkVendorError(route.Vendor.ID, perr.Error())
			if attempt < h.retryTimes {
				continue
			}
			break
		}

		// 复制请求并改写为上游模型名（复用已解析 IR，无需 raw body 重放）
		attemptReq := *irReq
		attemptReq.Model = route.Binding.UpstreamModel
		applyBindingParams(&attemptReq, route.Binding.Params)
		h.balancer.MarkVendorUsed(route.Vendor.ID)

		logrus.Debugf("[%s] 尝试 %d: %s(%s) -> 供应商[%s] 上游模型[%s]",
			requestID, attempt, virtualModel, protoStr, route.Vendor.Name, attemptReq.Model)

		if irReq.Stream {
			ch, cerr := prov.ChatStream(c.Request.Context(), &attemptReq)
			if cerr == nil {
				// 已成功建立上游流（此前未向客户端写出任何字节）→ 提交，不再重试
				h.balancer.MarkVendorOK(route.Vendor.ID)
				h.pumpStream(c, protocol, requestID, apiKey, route, virtualModel, ch, startTime, clientIP, userAgent)
				return
			}
			lastErr = errkit.FromError(cerr)
			h.balancer.MarkVendorError(route.Vendor.ID, cerr.Error())
		} else {
			resp, cerr := prov.Chat(c.Request.Context(), &attemptReq)
			if cerr == nil {
				h.balancer.MarkVendorOK(route.Vendor.ID)
				c.JSON(http.StatusOK, inbound.RenderResponse(protocol, resp, virtualModel))
				h.logSuccess(requestID, apiKey, route, virtualModel, resp.Usage, false, startTime, clientIP, userAgent, protoStr)
				return
			}
			lastErr = errkit.FromError(cerr)
			h.balancer.MarkVendorError(route.Vendor.ID, cerr.Error())
		}

		// 上游调用失败且尚未写出字节 → 判断是否换供应商重试
		if !errkit.ShouldRetry(lastErr) || attempt >= h.retryTimes {
			break
		}
	}

	if lastErr == nil {
		lastErr = errkit.InternalError("请求失败", nil)
	}
	upstream := ""
	if lastRoute != nil {
		upstream = lastRoute.Binding.UpstreamModel
	}
	h.respondError(c, lastErr, requestID, protocol)
	h.logFailure(requestID, apiKey, lastRoute, virtualModel, upstream, lastErr, 0, startTime, clientIP, userAgent, protoStr)
}

// pumpStream 通过 inbound.StreamWriter 按入站协议转发上游流，并记录日志。
func (h *ProxyHandler) pumpStream(c *gin.Context, protocol inbound.Protocol, requestID string, apiKey *model.APIKey, route *balancer.RouteResult, virtualModel string, ch <-chan provider.ChatChunk, startTime time.Time, clientIP, userAgent string) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		ae := errkit.InternalError("不支持流式响应", nil)
		c.JSON(http.StatusInternalServerError, ae.Render(string(protocol)))
		h.logFailure(requestID, apiKey, route, virtualModel, route.Binding.UpstreamModel, ae, 0, startTime, clientIP, userAgent, string(protocol))
		return
	}

	sw := inbound.NewStreamWriter(protocol, c.Writer, flusher, virtualModel)
	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			// 客户端断开：停止转发（上游 body 由 provider 的 ctx 监听关闭），按已累积计费/记录
			logrus.Infof("[%s] 客户端断开，终止流式转发", requestID)
			h.logSuccess(requestID, apiKey, route, virtualModel, sw.Usage(), true, startTime, clientIP, userAgent, string(protocol))
			return
		case chunk, ok := <-ch:
			if !ok {
				sw.Finish()
				h.logSuccess(requestID, apiKey, route, virtualModel, sw.Usage(), true, startTime, clientIP, userAgent, string(protocol))
				return
			}
			sw.WriteChunk(chunk)
		}
	}
}

// ListModels /v1/models — 返回该 Key 有权访问的虚拟模型（OpenAI 格式）。
func (h *ProxyHandler) ListModels(c *gin.Context) {
	apiKey, authErr := h.authenticateAPIKey(c)
	if authErr != nil {
		h.respondError(c, authErr, middleware.GetRequestID(c), inbound.OpenAI)
		return
	}

	var models []model.Model
	h.db.Where("enabled = ?", true).Order("name ASC").Find(&models)

	ids := make([]uint, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	vendorsByModel := make(map[uint][]uint)
	if len(ids) > 0 {
		var bindings []model.ModelBinding
		h.db.Where("model_id IN ? AND enabled = ?", ids, true).Find(&bindings)
		for _, b := range bindings {
			vendorsByModel[b.ModelID] = append(vendorsByModel[b.ModelID], b.VendorID)
		}
	}

	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		if !KeyAllowsModel(apiKey, m.Name, vendorsByModel[m.ID]) {
			continue
		}
		data = append(data, gin.H{"id": m.Name, "object": "model", "owned_by": "routerapi"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// ============ 错误响应与日志 ============

// respondError 按入站协议渲染错误响应。
func (h *ProxyHandler) respondError(c *gin.Context, ae *errkit.APIError, requestID string, protocol inbound.Protocol) {
	status := ae.StatusCode
	if status < 100 || status > 599 {
		status = http.StatusBadGateway
	}
	c.JSON(status, ae.Render(string(protocol)))
}

// logSuccess 记录成功调用，并按模型定价结算积分。
func (h *ProxyHandler) logSuccess(requestID string, apiKey *model.APIKey, route *balancer.RouteResult, virtualModel string, usage provider.Usage, isStream bool, startTime time.Time, clientIP, userAgent, inboundProtocol string) {
	entry := h.baseLog(requestID, apiKey, route, virtualModel, isStream, startTime, clientIP, userAgent, inboundProtocol)
	entry.Type = model.LogTypeConsume
	entry.TokenPrompt = usage.PromptTokens
	entry.TokenCompletion = usage.CompletionTokens
	entry.TokenTotal = usage.TotalTokens
	if entry.TokenTotal == 0 {
		entry.TokenTotal = usage.PromptTokens + usage.CompletionTokens
	}
	entry.StatusCode = http.StatusOK
	entry.Success = true
	// 结算：按模型定价计算积分并原子扣减（未启用/无归属/无定价则为 0）
	entry.CreditsCost = h.settle(requestID, apiKey, route, virtualModel, usage)
	h.saveLog(entry)
}

// settle 依据模型定价与用量计算积分并扣减归属人余额，返回本次消耗积分。
// 失败不计费（仅成功路径调用）；流式断开按已累积用量计费（调用方传入 sw.Usage()）。
func (h *ProxyHandler) settle(requestID string, apiKey *model.APIKey, route *balancer.RouteResult, virtualModel string, usage provider.Usage) int64 {
	if !h.billingEnabled || apiKey == nil || apiKey.OwnerUserID == 0 || route == nil || route.Model == nil {
		return 0
	}
	credits := billing.CreditsForUsage(usage.PromptTokens, usage.CompletionTokens, route.Model.PromptCreditsPer1M, route.Model.CompletionCreditsPer1M)
	if credits <= 0 {
		return 0
	}
	if _, err := billing.Deduct(h.db, apiKey.OwnerUserID, credits, requestID, virtualModel); err != nil {
		logrus.Errorf("[%s] 扣减积分失败(user=%d, credits=%d): %v", requestID, apiKey.OwnerUserID, credits, err)
	}
	return credits
}

// logFailure 记录失败调用。
func (h *ProxyHandler) logFailure(requestID string, apiKey *model.APIKey, route *balancer.RouteResult, virtualModel, upstreamModel string, ae *errkit.APIError, creditsCost int64, startTime time.Time, clientIP, userAgent, inboundProtocol string) {
	entry := h.baseLog(requestID, apiKey, route, virtualModel, false, startTime, clientIP, userAgent, inboundProtocol)
	entry.Type = model.LogTypeError
	if upstreamModel != "" {
		entry.UpstreamModel = upstreamModel
	}
	entry.StatusCode = ae.StatusCode
	entry.Success = false
	entry.ErrorMessage = ae.Error()
	entry.CreditsCost = creditsCost
	h.saveLog(entry)
}

// baseLog 组装日志公共字段。
func (h *ProxyHandler) baseLog(requestID string, apiKey *model.APIKey, route *balancer.RouteResult, virtualModel string, isStream bool, startTime time.Time, clientIP, userAgent, inboundProtocol string) model.RequestLog {
	entry := model.RequestLog{
		RequestID:       requestID,
		Model:           virtualModel,
		InboundProtocol: inboundProtocol,
		IsStream:        isStream,
		LatencyMs:       time.Since(startTime).Milliseconds(),
		ClientIP:        clientIP,
		UserAgent:       userAgent,
	}
	if apiKey != nil {
		entry.APIKeyID = apiKey.ID
		entry.APIKeyName = apiKey.Name
		entry.UserID = apiKey.OwnerUserID
	}
	if route != nil {
		entry.VendorID = route.Vendor.ID
		entry.VendorName = route.Vendor.Name
		entry.ProtocolName = route.Vendor.Protocol
		entry.UpstreamModel = route.Binding.UpstreamModel
	}
	return entry
}

func (h *ProxyHandler) saveLog(entry model.RequestLog) {
	if err := h.db.Create(&entry).Error; err != nil {
		logrus.Errorf("记录调用日志失败: %v", err)
		return
	}
	// 并入按小时预聚合（看板查询走小表）
	stats.Record(h.db, entry)
}
