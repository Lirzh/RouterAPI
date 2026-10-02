package ir

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestUnmarshalKeepsUnknownFields(t *testing.T) {
	body := `{
		"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],
		"temperature":0.3,
		"response_format":{"type":"json_object"},
		"seed":42,
		"logprobs":true,
		"top_logprobs":3,
		"metadata":{"user_id":"u-9"},
		"service_tier":"priority"
	}`
	var req ChatRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 强类型字段照常解析
	if req.Model != "gpt-4o" || len(req.Messages) != 1 {
		t.Fatalf("强类型字段解析不对: %+v", req)
	}
	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Errorf("temperature = %v, want 0.3", req.Temperature)
	}

	// IR 未声明的字段必须全部留在 Extra 里
	want := []string{"logprobs", "metadata", "response_format", "seed", "service_tier", "top_logprobs"}
	if !reflect.DeepEqual(extraKeyNames(req.Extra), want) {
		t.Errorf("Extra 键 = %v, want %v", extraKeyNames(req.Extra), want)
	}
	// 已声明字段绝不能同时出现在 Extra 里（否则回填会产生重复键）
	for _, known := range []string{"model", "messages", "temperature"} {
		if _, dup := req.Extra[known]; dup {
			t.Errorf("已声明字段 %q 被误当成未识别字段收进 Extra", known)
		}
	}
}

// MarshalWithExtra 必须逐字节保留未识别字段的原始形态，包括嵌套对象与数组。
func TestMarshalWithExtraPreservesNestedShapes(t *testing.T) {
	var req ChatRequest
	if err := json.Unmarshal([]byte(`{
		"model":"m","messages":[{"role":"user","content":"x"}],
		"response_format":{"type":"json_schema","json_schema":{"name":"s","strict":true}},
		"stop":["a"]
	}`), &req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	out, err := MarshalWithExtra(&req, req.Extra)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if string(got["response_format"]) != `{"type":"json_schema","json_schema":{"name":"s","strict":true}}` {
		t.Errorf("嵌套结构未原样保留: %s", got["response_format"])
	}
	// stop 是已声明字段，不应落到 Extra
	if _, leaked := req.Extra["stop"]; leaked {
		t.Error("已声明字段 stop 被收进 Extra")
	}
}

// 已声明字段优先：Extra 不得覆盖被改写过的强类型字段。
// 典型场景是路由把 model 改成上游真实模型名、applyBindingParams 注入默认参数。
func TestDeclaredFieldsWinOverExtra(t *testing.T) {
	req := &ChatRequest{Model: "upstream-real-name"}
	req.Extra = map[string]json.RawMessage{"model": json.RawMessage(`"client-virtual-name"`)}

	out, err := MarshalWithExtra(req, req.Extra)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var got struct{ Model string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Model != "upstream-real-name" {
		t.Errorf("model = %q, want upstream-real-name（Extra 覆盖了路由改写结果）", got.Model)
	}
}

// 词汇表闸门：Extra 只在"上游协议 == 入站协议"时可回填。
func TestReplayableExtraVocabularyGate(t *testing.T) {
	var req ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"m","messages":[],"seed":1}`), &req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	req.Inbound = ProtocolOpenAI
	if got := req.ReplayableExtra(ProtocolOpenAI); len(got) != 1 {
		t.Errorf("同词汇表应可回填, got %d 个字段", len(got))
	}
	// 把 OpenAI 的 seed 塞进 Anthropic 请求体会直接换来 400
	if got := req.ReplayableExtra(ProtocolAnthropic); got != nil {
		t.Errorf("跨词汇表必须拒绝回填, got %v", got)
	}
	if got := req.ReplayableExtra(ProtocolGemini); got != nil {
		t.Errorf("跨词汇表必须拒绝回填, got %v", got)
	}

	// Inbound 缺省按 OpenAI 词汇处理
	bare := ChatRequest{Extra: map[string]json.RawMessage{"seed": json.RawMessage(`1`)}}
	if got := bare.ReplayableExtra(ProtocolOpenAI); len(got) != 1 {
		t.Errorf("Inbound 为空应按 openai 词汇处理, got %d", len(got))
	}
	if got := bare.ReplayableExtra(ProtocolAnthropic); got != nil {
		t.Errorf("Inbound 为空不应回填到 anthropic, got %v", got)
	}

	// 空 Extra 不应凭空造出 map
	empty := ChatRequest{Inbound: ProtocolOpenAI}
	if got := empty.ReplayableExtra(ProtocolOpenAI); got != nil {
		t.Errorf("无 Extra 时应返回 nil, got %v", got)
	}
}

// 已知键集合必须由结构体标签派生：有人加字段时它自动跟上，
// 否则那个新字段会被误判成"未识别字段"进 Extra，再被回填成重复键。
func TestKnownKeysTrackStructFields(t *testing.T) {
	for name := range chatRequestKeys {
		var req ChatRequest
		raw, err := json.Marshal(map[string]json.RawMessage{name: json.RawMessage(`null`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			continue // 类型不匹配的已知字段仍算已知，只要没进 Extra 就行
		}
		if _, dup := req.Extra[name]; dup {
			t.Errorf("已知字段 %q 落在了 Extra 里，chatRequestKeys 与结构体已不同步", name)
		}
	}
}

func TestChatResponseKeepsUnknownFields(t *testing.T) {
	var resp ChatResponse
	if err := json.Unmarshal([]byte(`{
		"id":"cmpl-1","object":"chat.completion","created":1,"model":"gpt-4o",
		"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},
		"system_fingerprint":"fp_abc123"
	}`), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if string(resp.Extra["system_fingerprint"]) != `"fp_abc123"` {
		t.Fatalf("system_fingerprint 未保留: %v", resp.Extra)
	}

	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if string(got["system_fingerprint"]) != `"fp_abc123"` {
		t.Errorf("响应未识别字段未回传给客户端: %s", out)
	}
}

func extraKeyNames(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 简单升序，避免引入 sort 依赖之外的额外断言
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
