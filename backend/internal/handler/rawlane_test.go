package handler

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/routerapi/backend/internal/balancer"
	"github.com/routerapi/backend/internal/inbound"
	"github.com/routerapi/backend/internal/ir"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/provider"
	"github.com/routerapi/backend/pkg/config"
)

// irLaneBody 走 IR 往返那条路，返回最终发往上游的顶层字段。
func irLaneBody(t *testing.T, clientBody, upstreamModel, params string) map[string]json.RawMessage {
	t.Helper()
	req, err := inbound.Parse(inbound.OpenAI, []byte(clientBody), "")
	if err != nil {
		t.Fatalf("入站解析失败: %v", err)
	}
	req.Model = upstreamModel
	applyBindingParams(req, params)
	sent, err := ir.MarshalWithExtra(req, req.ReplayableExtra(ir.ProtocolOpenAI))
	if err != nil {
		t.Fatalf("IR 序列化失败: %v", err)
	}
	return topLevel(t, sent)
}

func rawLaneBody(t *testing.T, clientBody, upstreamModel, params string) map[string]json.RawMessage {
	t.Helper()
	sent, err := buildRawBody([]byte(clientBody), upstreamModel, params)
	if err != nil {
		t.Fatalf("直通请求体构造失败: %v", err)
	}
	return topLevel(t, sent)
}

func topLevel(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	obj, err := ir.ParseJSONObject(body)
	if err != nil {
		t.Fatalf("响应体不是 JSON 对象: %v\n%s", err, body)
	}
	return obj
}

// jsonEquivalent 按 JSON 语义比对两段原文（42 与 42.0 视为相同，键顺序无关）。
func jsonEquivalent(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb interface{}
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("解析 %s 失败: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("解析 %s 失败: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

// 直通路径的全部意义是"不改客户端的字节"，所以这里断言的是原文相等，不是语义相等。
func TestBuildRawBodyLeavesUnknownFieldsByteIdentical(t *testing.T) {
	const clientBody = `{
		"model":"virtual","messages":[{"role":"user","content":"<hi> & ok"}],
		"prediction":{"type":"content","content":"prefix"},
		"web_search_options":{"search_context_size":"high"},
		"presence_penalty":1e-1,
		"extra_body":{"nested":{"deep":[1,2,3]}}
	}`
	sent, err := buildRawBody([]byte(clientBody), "real-upstream-model", "")
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	obj := topLevel(t, sent)

	for _, key := range []string{"prediction", "web_search_options", "extra_body", "messages"} {
		if _, ok := obj[key]; !ok {
			t.Errorf("字段 %q 没抵达上游", key)
		}
	}
	if string(obj["prediction"]) != `{"type":"content","content":"prefix"}` {
		t.Errorf("prediction 原文被改动: %s", obj["prediction"])
	}
	// 数字保持客户端写法，不被 float64 往返改写成 0.1
	if string(obj["presence_penalty"]) != "1e-1" {
		t.Errorf("presence_penalty = %s, want 1e-1（数字格式被改写就不是直通了）", obj["presence_penalty"])
	}
	// HTML 转义必须关掉：正文里的 < > & 不该变成 \u003c 之类
	if string(obj["messages"]) != `[{"role":"user","content":"<hi> & ok"}]` {
		t.Errorf("messages 被转义改写: %s", obj["messages"])
	}
	if string(obj["model"]) != `"real-upstream-model"` {
		t.Errorf("model = %s, want 上游模型名", obj["model"])
	}
}

// 两条路对绑定默认参数的判定必须一致，否则"换一个供应商"会顺带"换一套参数"。
func TestBindingParamsAgreeOnBothLanes(t *testing.T) {
	const params = `{"temperature":0.7,"top_p":0.9,"max_tokens":2000,"frequency_penalty":0.1,"presence_penalty":0.2,"stop":["END","###"]}`
	cases := []struct {
		name  string
		body  string
		check []string // 期望两条路都取绑定默认值的键
		kept  []string // 期望保留客户端值的键
		keptV map[string]string
	}{
		{
			name:  "客户端什么都没传",
			body:  `{"model":"v","messages":[{"role":"user","content":"hi"}]}`,
			check: []string{"temperature", "top_p", "max_tokens", "frequency_penalty", "presence_penalty", "stop"},
		},
		{
			name:  "客户端显式传 0，不能被默认值盖掉",
			body:  `{"model":"v","messages":[],"temperature":0,"max_tokens":0,"stop":null}`,
			check: []string{"stop"}, // stop:null 与空数组同义，IR 路径会填默认，直通必须跟着填
			kept:  []string{"temperature", "max_tokens"},
			keptV: map[string]string{"temperature": "0", "max_tokens": "0"},
		},
		{
			name:  "客户端传了值",
			body:  `{"model":"v","messages":[],"temperature":0.1,"stop":["x"]}`,
			kept:  []string{"temperature", "stop"},
			keptV: map[string]string{"temperature": "0.1"},
			check: []string{"max_tokens", "top_p"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			irBody := irLaneBody(t, tc.body, "up", params)
			rawBody := rawLaneBody(t, tc.body, "up", params)
			for _, key := range append(append([]string{}, tc.check...), tc.kept...) {
				a, b := irBody[key], rawBody[key]
				if !jsonEquivalent(t, a, b) {
					t.Errorf("键 %q 两条路不一致: IR=%s 直通=%s", key, a, b)
				}
			}
			for key, want := range tc.keptV {
				if !jsonEquivalent(t, irBody[key], []byte(want)) {
					t.Errorf("键 %q 应为客户端原值 %s，实际 IR=%s", key, want, irBody[key])
				}
			}
		})
	}
}

// 绑定配置里写错的类型不该被直通"顺手支持"：IR 路径忽略它，直通也必须忽略。
func TestBindingParamsRawRejectsWrongTypes(t *testing.T) {
	obj, err := ir.ParseJSONObject([]byte(`{"model":"up","messages":[]}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	applyBindingParamsRaw(obj, `{"temperature":"0.7","max_tokens":true,"stop":123}`)
	for _, key := range []string{"temperature", "max_tokens", "stop"} {
		if _, ok := obj[key]; ok {
			t.Errorf("非法类型的绑定默认值 %q 被写进了请求体: %s", key, obj[key])
		}
	}
}

func TestRewriteModelField(t *testing.T) {
	out := inbound.RewriteModelField([]byte(`{"id":"1","model":"upstream-real","usage":{"prompt_tokens":3}}`), "virtual")
	if string(topLevel(t, out)["model"]) != `"virtual"` {
		t.Errorf("model 未换回虚拟名: %s", out)
	}
	// 其余字段一个字节都不动
	if string(topLevel(t, out)["usage"]) != `{"prompt_tokens":3}` {
		t.Errorf("usage 被改动: %s", out)
	}
	// 非对象输入返回原文，不 panic（已成功的请求不该因为改不动字段而变 502）
	if got := string(inbound.RewriteModelField([]byte(`null`), "v")); got != "null" {
		t.Errorf("非对象输入 = %s, want 原样返回", got)
	}
}

// 直通 lane 的准入判定：只有"入站与上游同格式 + 适配器支持 + 开关没关"才走。
func TestRawLaneAdmission(t *testing.T) {
	openaiVendor := &model.Vendor{Name: "oai", Protocol: model.ProtocolOpenAI}
	compatVendor := &model.Vendor{Name: "relay", Protocol: model.ProtocolOpenAICompat}
	antVendor := &model.Vendor{Name: "claude", Protocol: model.ProtocolAnthropic}

	cases := []struct {
		name     string
		proto    inbound.Protocol
		vendor   *model.Vendor
		disabled bool
		want     bool
	}{
		{"OpenAI 入站 + OpenAI 上游", inbound.OpenAI, openaiVendor, false, true},
		{"OpenAI 入站 + 兼容上游", inbound.OpenAI, compatVendor, false, true},
		{"OpenAI 入站 + Anthropic 上游", inbound.OpenAI, antVendor, false, false},
		{"Anthropic 入站 + Anthropic 上游（尚未实现直通）", inbound.Anthropic, antVendor, false, false},
		{"配置关闭", inbound.OpenAI, openaiVendor, true, false},
	}
	body := []byte(`{"model":"v","messages":[]}`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &ProxyHandler{gateway: config.GatewayConfig{RawLaneDisabled: tc.disabled}}
			route := &balancer.RouteResult{
				Vendor:  tc.vendor,
				Binding: &model.ModelBinding{UpstreamModel: "up"},
			}
			prov, err := provider.NewFromVendor(tc.vendor)
			if err != nil {
				t.Fatalf("创建适配器失败: %v", err)
			}
			_, rawBody, ok := h.rawLane(tc.proto, prov, body, route, "test")
			if ok != tc.want {
				t.Fatalf("直通判定 = %v, want %v", ok, tc.want)
			}
			if ok && string(topLevel(t, rawBody)["model"]) != `"up"` {
				t.Errorf("直通请求体未改写 model: %s", rawBody)
			}
		})
	}
}
