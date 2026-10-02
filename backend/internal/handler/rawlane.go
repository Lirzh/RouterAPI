package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/balancer"
	"github.com/routerapi/backend/internal/errkit"
	"github.com/routerapi/backend/internal/inbound"
	"github.com/routerapi/backend/internal/ir"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/provider"
	"github.com/sirupsen/logrus"
)

// bindingNumericParams 是绑定默认参数里按数值处理的键。
// 必须与 applyBindingParams 覆盖的键集合一致，否则同一个绑定在两条路上给出不同请求
// （见 rawlane_test.go 的对照测试）。
var bindingNumericParams = []string{"temperature", "top_p", "max_tokens", "frequency_penalty", "presence_penalty"}

// rawLane 判定本次尝试能否走同格式直通，并构造出直通的请求体。
//
// 返回 false 时调用方按原来的 IR 往返走：直通是加速与保真的加购项，不是新的必选依赖，
// 任何构造上的不确定都退回老路，而不是把客户端请求打成错误。
func (h *ProxyHandler) rawLane(protocol inbound.Protocol, prov provider.Provider, body []byte, route *balancer.RouteResult, requestID string) (provider.RawProvider, []byte, bool) {
	if !h.gateway.RawLaneEnabled() {
		return nil, nil, false
	}
	// 判据用"上游词汇表 == 入站协议"而不是"供应商协议字符串相等"：
	// openai-compat 与 custom 说的本来就是同一种话。
	if protocol != inbound.OpenAI || provider.Vocab(route.Vendor.Protocol) != ir.ProtocolOpenAI {
		return nil, nil, false
	}
	rp, ok := prov.(provider.RawProvider)
	if !ok {
		return nil, nil, false
	}
	raw, err := buildRawBody(body, route.Binding.UpstreamModel, route.Binding.Params)
	if err != nil {
		logrus.Debugf("[%s] 直通请求体构造失败，退回 IR 往返: %v", requestID, err)
		return nil, nil, false
	}
	return rp, raw, true
}

// buildRawBody 在入站原始 body 上只做两件事：换成上游模型名、补绑定默认参数。
//
// 其余字节一个不动，所以客户端传了但网关不认识的参数（response_format、seed、
// 各家兼容网关的私有字段……）天然抵达上游。这正是待办 3 用 Extra 抢救的那件事，
// 在"入站与上游同格式"这一最常见组合上的彻底版本。
func buildRawBody(body []byte, upstreamModel, paramsJSON string) ([]byte, error) {
	obj, err := ir.ParseJSONObject(body)
	if err != nil {
		return nil, err
	}
	obj["model"] = ir.JSONString(upstreamModel)
	applyBindingParamsRaw(obj, paramsJSON)
	return ir.MarshalJSONObject(obj)
}

// applyBindingParamsRaw 是 applyBindingParams 的原文版：只在客户端没说话时填默认值。
func applyBindingParamsRaw(obj map[string]json.RawMessage, paramsJSON string) {
	paramsJSON = strings.TrimSpace(paramsJSON)
	if paramsJSON == "" {
		return
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		return
	}
	for _, key := range bindingNumericParams {
		raw, ok := params[key]
		if !ok || !rawParamAbsent(obj[key]) || !isJSONNumber(raw) {
			continue
		}
		obj[key] = raw
	}
	if raw, ok := params["stop"]; ok && rawStopUnset(obj["stop"]) {
		if c := firstJSONByte(raw); c == '"' || c == '[' {
			obj["stop"] = raw
		}
	}
}

// rawParamAbsent 判断客户端对某个数值键是否"没说话"。显式的 0 是说了话——
// "temperature": 0 与不传是完全不同的采样行为，不能被绑定默认值盖掉。
func rawParamAbsent(cur json.RawMessage) bool {
	return len(cur) == 0 || strings.TrimSpace(string(cur)) == "null"
}

// rawStopUnset 与 IR 路径对齐：那边把 null、[]、"" 都解析成"没有停止词"，
// 于是绑定默认值会覆盖上去，直通必须做出同一判定，否则换条路参数就变了。
func rawStopUnset(cur json.RawMessage) bool {
	if rawParamAbsent(cur) {
		return true
	}
	switch strings.TrimSpace(string(cur)) {
	case "[]", `""`:
		return true
	}
	return false
}

func isJSONNumber(raw []byte) bool {
	c := firstJSONByte(raw)
	return c == '-' || (c >= '0' && c <= '9')
}

func firstJSONByte(raw []byte) byte {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return 0
	}
	return t[0]
}

// pumpRawStream 逐帧把上游 SSE 原文转给客户端，同时窥探 usage 用于结算。
// 与 pumpStream 一样：建立流之前不会向客户端写出任何字节，返回是否已完成结算。
func (h *ProxyHandler) pumpRawStream(c *gin.Context, protocol inbound.Protocol, requestID string, apiKey *model.APIKey, route *balancer.RouteResult, virtualModel string, stream *provider.RawStream, startTime time.Time, clientIP, userAgent string, reserved bool) bool {
	c.Writer.Header().Set("Content-Type", stream.ContentType)
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		ae := errkit.InternalError("不支持流式响应", nil)
		c.JSON(http.StatusInternalServerError, ae.Render(string(protocol)))
		h.logFailure(requestID, apiKey, route, virtualModel, route.Binding.UpstreamModel, ae, 0, startTime, clientIP, userAgent, string(protocol))
		return false
	}

	sw := inbound.NewRawStreamWriter(c.Writer, flusher, virtualModel, route.Binding.UpstreamModel)
	ctx := c.Request.Context()
	protoStr := string(protocol)
	for {
		select {
		case <-ctx.Done():
			// 客户端断开：已窥探到的用量照常结算（上游已经花了钱，这部分不能免）。
			logrus.Infof("[%s] 客户端断开，终止直通流式转发", requestID)
			return h.logSuccess(requestID, apiKey, route, virtualModel, sw.Usage(), true, startTime, clientIP, userAgent, protoStr, reserved)
		case frame, ok := <-stream.Frames:
			if !ok {
				sw.Finish()
				return h.logSuccess(requestID, apiKey, route, virtualModel, sw.Usage(), true, startTime, clientIP, userAgent, protoStr, reserved)
			}
			sw.Forward(frame.Raw, frame.Data)
		}
	}
}
