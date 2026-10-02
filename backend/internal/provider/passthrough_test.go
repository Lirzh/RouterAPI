package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/routerapi/backend/internal/inbound"
	"github.com/routerapi/backend/internal/ir"
)

// captureUpstream 起一个把收到的请求体原样记录下来的上游，返回一个能正常解析的最小响应。
func captureUpstream(t *testing.T, respBody string) (baseURL string, got func() map[string]json.RawMessage) {
	t.Helper()
	var received map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() map[string]json.RawMessage { return received }
}

const openaiOK = `{"id":"c1","object":"chat.completion","created":1,"model":"up",
	"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func clientRequest(t *testing.T) *ir.ChatRequest {
	t.Helper()
	var req ir.ChatRequest
	body := `{
		"model":"virtual-name","messages":[{"role":"user","content":"hi"}],
		"response_format":{"type":"json_object"},
		"seed":42,"logprobs":true,
		"metadata":{"user_id":"u-9"},
		"web_search_options":{"search_context_size":"high"}
	}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("构造入站请求失败: %v", err)
	}
	req.Inbound = ir.ProtocolOpenAI
	// 路由会把 model 改写成上游真实模型名，模拟这一步
	req.Model = "up"
	return &req
}

// 这是本修复的核心断言：OpenAI 入站 -> OpenAI 兼容上游，客户端参数必须原样抵达线上。
// 修复前 IR 是闭合结构体，response_format / seed / logprobs / metadata 在这一跳被静默丢弃，
// 表现为"我传了 json_object 但模型不吐 JSON"。
func TestOpenAIUpstreamReceivesUnrecognizedFields(t *testing.T) {
	url, got := captureUpstream(t, openaiOK)
	p := NewOpenAIProvider("t", url, "k", "Authorization", "Bearer ")

	if _, err := p.Chat(context.Background(), clientRequest(t)); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}

	sent := got()
	for _, k := range []string{"response_format", "seed", "logprobs", "metadata", "web_search_options"} {
		if _, ok := sent[k]; !ok {
			t.Errorf("字段 %q 没发到上游（被 IR 吃掉了）", k)
		}
	}
	if string(sent["response_format"]) != `{"type":"json_object"}` {
		t.Errorf("response_format 形态变了: %s", sent["response_format"])
	}
	if string(sent["seed"]) != "42" {
		t.Errorf("seed = %s, want 42", sent["seed"])
	}
	// 改写后的上游模型名不能被 Extra 覆盖回客户端的虚拟名
	if string(sent["model"]) != `"up"` {
		t.Errorf("model = %s, want \"up\"（路由改写结果被覆盖会让请求打到错误的上游模型）", sent["model"])
	}
}

// 跨词汇表不得回填：把 OpenAI 的 response_format 塞进 Anthropic 请求体不会
// "多支持一个参数"，只会让上游直接 400，把一次能成功的调用变成失败。
func TestCrossVocabularyFieldsAreNotLeaked(t *testing.T) {
	req := clientRequest(t)

	t.Run("anthropic", func(t *testing.T) {
		url, got := captureUpstream(t, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","model":"up","usage":{"input_tokens":1,"output_tokens":1}}`)
		p := NewAnthropicProvider("t", url, "k")
		_, _ = p.Chat(context.Background(), req)

		sent := got()
		for _, k := range []string{"response_format", "seed", "logprobs", "metadata", "web_search_options"} {
			if _, leaked := sent[k]; leaked {
				t.Errorf("OpenAI 词汇字段 %q 泄漏进了 Anthropic 请求体", k)
			}
		}
		if string(sent["model"]) != `"up"` {
			t.Errorf("anthropic 请求 model = %s, want \"up\"", sent["model"])
		}
	})

	t.Run("gemini", func(t *testing.T) {
		url, got := captureUpstream(t, `{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		p := NewGeminiProvider("t", url, "k")
		_, _ = p.Chat(context.Background(), req)

		sent := got()
		for _, k := range []string{"response_format", "seed", "logprobs", "metadata", "web_search_options"} {
			if _, leaked := sent[k]; leaked {
				t.Errorf("OpenAI 词汇字段 %q 泄漏进了 Gemini 请求体", k)
			}
		}
	})
}

// 流式路径同样要回填，并且不能因为回填丢掉 include_usage
// （末帧 usage 是精确计费的前提，丢了它就只能退回字符估算）。
func TestOpenAIStreamReceivesUnrecognizedFieldsAndUsageOptIn(t *testing.T) {
	url, got := captureUpstream(t, "data: [DONE]\n\n")
	p := NewOpenAIProvider("t", url, "k", "Authorization", "Bearer ")

	req := clientRequest(t)
	req.Stream = true
	ch, err := p.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}
	for range ch { // 排空，让 goroutine 正常结束
	}

	sent := got()
	if _, ok := sent["response_format"]; !ok {
		t.Error("流式请求未回填 response_format")
	}
	if string(sent["stream"]) != "true" {
		t.Errorf("stream = %s, want true", sent["stream"])
	}
	var so struct {
		IncludeUsage bool `json:"include_usage"`
	}
	raw, ok := sent["stream_options"]
	if !ok {
		t.Fatal("stream_options 丢失，末帧不会再带 usage")
	}
	if err := json.Unmarshal(raw, &so); err != nil {
		t.Fatalf("解析 stream_options 失败: %v", err)
	}
	if !so.IncludeUsage {
		t.Errorf("include_usage = %v, want true", so.IncludeUsage)
	}
}

// 客户端自己带了 stream_options 时，它属于已声明字段，不该又出现在 Extra 里造成重复键。
func TestClientStreamOptionsNotDuplicated(t *testing.T) {
	var req ir.ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"up","messages":[],"stream_options":{"include_usage":true},"seed":7}`), &req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if _, dup := req.Extra["stream_options"]; dup {
		t.Error("已声明字段 stream_options 被误收进 Extra")
	}
	if len(req.Extra) != 1 {
		t.Errorf("Extra = %v, want 只剩 seed", req.Extra)
	}
}

// Anthropic 入站 -> Anthropic 上游：Claude 客户端的自有字段必须原样抵达上游。
//
// 走真实的 inbound.Parse -> provider 链路，而不是手工构造 IR：
// 早先的漏洞正是"入站解析器从自己的 DTO 建 IR"，未识别字段在进入 IR 之前就已丢失，
// 只测 provider 回填是发现不了的。
func TestAnthropicInboundReplaysToAnthropicUpstream(t *testing.T) {
	const clientBody = `{
		"model":"claude-sonnet-4-5","max_tokens":2000,
		"messages":[{"role":"user","content":"hi"}],
		"system":"be brief",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"service_tier":"max",
		"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]}
	}`
	req, err := inbound.Parse(inbound.Anthropic, []byte(clientBody), "")
	if err != nil {
		t.Fatalf("入站解析失败: %v", err)
	}
	for _, k := range []string{"thinking", "service_tier", "context_management"} {
		if _, ok := req.Extra[k]; !ok {
			t.Fatalf("字段 %q 未被入站捕获（Extra=%v）", k, extraNames(req.Extra))
		}
	}
	// 已声明字段不得混进 Extra，否则回填会产出重复键
	for _, known := range []string{"model", "messages", "system", "max_tokens"} {
		if _, dup := req.Extra[known]; dup {
			t.Errorf("已声明字段 %q 被误收进 Extra", known)
		}
	}

	url, got := captureUpstream(t, `{"id":"msg_1","type":"message","role":"assistant",
		"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","model":"claude",
		"usage":{"input_tokens":1,"output_tokens":1}}`)
	req.Model = "claude-upstream-name"
	if _, err := NewAnthropicProvider("t", url, "k").Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}

	sent := got()
	if string(sent["thinking"]) != `{"type":"enabled","budget_tokens":1024}` {
		t.Errorf("thinking 未原样回填: %s", sent["thinking"])
	}
	if _, ok := sent["context_management"]; !ok {
		t.Error("context_management 未回填")
	}
	if _, ok := sent["service_tier"]; !ok {
		t.Error("service_tier 未回填")
	}
	// system 是已声明字段，由 IR 重组，不应因 Extra 出现第二次
	if string(sent["model"]) != `"claude-upstream-name"` {
		t.Errorf("model = %s, want 上游改写后的名字", sent["model"])
	}
}

// 同一个 Anthropic 请求打 OpenAI 兼容上游时，Anthropic 词汇的字段必须被挡下——
// 把 thinking 塞进 /v1/chat/completions 只会换来一个 400。
func TestAnthropicInboundNotLeakedToOpenAIUpstream(t *testing.T) {
	req, err := inbound.Parse(inbound.Anthropic, []byte(`{
		"model":"claude-sonnet-4-5","max_tokens":100,
		"messages":[{"role":"user","content":"hi"}],
		"thinking":{"type":"enabled","budget_tokens":512}
	}`), "")
	if err != nil {
		t.Fatalf("入站解析失败: %v", err)
	}

	url, got := captureUpstream(t, openaiOK)
	if _, err := NewOpenAIProvider("t", url, "k", "Authorization", "Bearer ").Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if _, leaked := got()["thinking"]; leaked {
		t.Error("Anthropic 词汇字段 thinking 泄漏进了 OpenAI 请求体")
	}
}

// Gemini 入站同理：toolConfig 这类字段应回到 Gemini 上游。
func TestGeminiInboundReplaysToGeminiUpstream(t *testing.T) {
	req, err := inbound.Parse(inbound.Gemini, []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig":{"temperature":0.2,"maxOutputTokens":64},
		"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}
	}`), "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("入站解析失败: %v", err)
	}
	if _, ok := req.Extra["toolConfig"]; !ok {
		t.Fatalf("toolConfig 未被入站捕获（Extra=%v）", extraNames(req.Extra))
	}

	url, got := captureUpstream(t, `{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	if _, err := NewGeminiProvider("t", url, "k").Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if _, ok := got()["toolConfig"]; !ok {
		t.Error("toolConfig 未回填给 Gemini 上游")
	}
}

func extraNames(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 块级 cache_control 必须能走完 Claude 入站 -> Claude 上游。
//
// 它在 content 数组内部，顶层 Extra 覆盖不到嵌套结构，所以只能作为 IR 一等字段。
// 丢它的后果不是"少一个参数"而是整轮提示缓存失效：上游不会产生
// cache_read_input_tokens，于是缓存折扣定价永远命中不了，客户端标了断点仍按全价计费。
func TestCacheControlRoundTripsToAnthropic(t *testing.T) {
	req, err := inbound.Parse(inbound.Anthropic, []byte(`{
		"model":"claude-sonnet-4-5","max_tokens":500,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"big shared prefix","cache_control":{"type":"ephemeral","ttl":"1h"}},
			{"type":"text","text":"question"}
		]}]
	}`), "")
	if err != nil {
		t.Fatalf("入站解析失败: %v", err)
	}
	parts, ok := req.Messages[0].Content.([]ir.ContentPart)
	if !ok {
		t.Fatalf("content 形态 = %T, want []ir.ContentPart", req.Messages[0].Content)
	}
	if parts[0].CacheControl == nil || parts[0].CacheControl.Type != "ephemeral" || parts[0].CacheControl.TTL != "1h" {
		t.Fatalf("入站未捕获 cache_control: %+v", parts[0])
	}
	if parts[1].CacheControl != nil {
		t.Errorf("不该给未标记的块凭空造断点: %+v", parts[1].CacheControl)
	}

	url, got := captureUpstream(t, `{"id":"msg_1","type":"message","role":"assistant",
		"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","model":"claude",
		"usage":{"input_tokens":10,"output_tokens":2}}`)
	if _, err := NewAnthropicProvider("t", url, "k").Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}

	sent := got()
	var msgs []struct {
		Content []map[string]interface{} `json:"content"`
	}
	if err := json.Unmarshal(sent["messages"], &msgs); err != nil {
		t.Fatalf("解析上游 messages 失败: %v", err)
	}
	if len(msgs) == 0 || len(msgs[0].Content) != 2 {
		t.Fatalf("上游收到的内容块不对: %s", sent["messages"])
	}
	content := msgs[0].Content
	cc, _ := content[0]["cache_control"].(map[string]interface{})
	if cc == nil || cc["type"] != "ephemeral" || cc["ttl"] != "1h" {
		t.Errorf("cache_control 未抵达上游: %+v", content[0])
	}
	if _, exists := content[1]["cache_control"]; exists {
		t.Errorf("未标记的块被凭空加了断点: %+v", content[1])
	}
}

// 反向：OpenAI 客户端往 content 里写 cache_control（LiteLLM 一类的多供应商客户端）
// 打到 Anthropic 上游时也要带上，否则它以为开了缓存其实没开。
func TestCacheControlFromOpenAIStyleContent(t *testing.T) {
	var req ir.ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"claude-x","messages":[{"role":"user","content":[
		{"type":"text","text":"prefix","cache_control":{"type":"ephemeral"}}]}]}`), &req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	req.Inbound = ir.ProtocolOpenAI

	url, got := captureUpstream(t, `{"id":"msg_1","type":"message","role":"assistant",
		"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","model":"claude-x",
		"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, err := NewAnthropicProvider("t", url, "k").Chat(context.Background(), &req); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}

	sent := got()
	if !strings.Contains(string(sent["messages"]), "cache_control") {
		t.Errorf("OpenAI 形态的 cache_control 未透传给 Anthropic 上游: %s", sent["messages"])
	}
}
