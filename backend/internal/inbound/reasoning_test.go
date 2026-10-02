package inbound

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/routerapi/backend/internal/ir"
)

// respWith 构造一个带思维链的 IR 非流式响应。
func respWith(text, reasoning, sig string) *ir.ChatResponse {
	return &ir.ChatResponse{
		ID: "c1", Object: "chat.completion", Created: 1, Model: "up",
		Choices: []ir.Choice{{Index: 0, FinishReason: "stop",
			Message: ir.Message{Role: "assistant", Content: text,
				ReasoningContent: reasoning, ReasoningSignature: sig}}},
		Usage: ir.Usage{PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140, ReasoningTokens: 25},
	}
}

// 思维链必须在每种出站协议里都保住自己的位置：
// Anthropic 是排在正文前的 thinking 块（含签名），Gemini 是 thought:true 的 part，
// OpenAI 生态是 reasoning_content。混进正文会让客户端无法折叠思考过程。
func TestRenderAnthropicKeepsThinkingBlockFirst(t *testing.T) {
	out := renderAnthropicResponse(respWith("the answer", "let me think", "sig-abc"), "virt-model").(map[string]interface{})

	content, ok := out["content"].([]map[string]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("content 块数不对: %+v", out["content"])
	}
	if content[0]["type"] != "thinking" || content[0]["thinking"] != "let me think" {
		t.Errorf("第 0 块应为 thinking: %+v", content[0])
	}
	if content[0]["signature"] != "sig-abc" {
		t.Errorf("签名未随思维块输出: %+v", content[0])
	}
	if content[1]["type"] != "text" || content[1]["text"] != "the answer" {
		t.Errorf("第 1 块应为 text: %+v", content[1])
	}
}

func TestRenderGeminiMarksThoughtParts(t *testing.T) {
	out := renderGeminiResponse(respWith("the answer", "let me think", "sig-1"), "virt").(map[string]interface{})
	cands := out["candidates"].([]map[string]interface{})
	parts := cands[0]["content"].(map[string]interface{})["parts"].([]map[string]interface{})

	if len(parts) != 2 {
		t.Fatalf("parts 数 = %d, want 2 (%+v)", len(parts), parts)
	}
	if parts[0]["thought"] != true || parts[0]["text"] != "let me think" {
		t.Errorf("第 0 个 part 应为 thought: %+v", parts[0])
	}
	if parts[0]["thoughtSignature"] != "sig-1" {
		t.Errorf("thoughtSignature 未输出: %+v", parts[0])
	}
	if _, isThought := parts[1]["thought"]; isThought {
		t.Errorf("正文 part 不该带 thought 标记: %+v", parts[1])
	}
	if parts[1]["text"] != "the answer" {
		t.Errorf("正文不对: %+v", parts[1])
	}

	// candidatesTokenCount 在 Gemini 语义里不含思考，IR 却把 reasoning 算在
	// CompletionTokens 内，出站必须减回去，否则客户端看到的分项之和会大于 total。
	um := out["usageMetadata"].(map[string]interface{})
	if um["thoughtsTokenCount"] != 25 {
		t.Errorf("thoughtsTokenCount = %v, want 25", um["thoughtsTokenCount"])
	}
	if um["candidatesTokenCount"] != 15 {
		t.Errorf("candidatesTokenCount = %v, want 15（40 总输出 - 25 思考）", um["candidatesTokenCount"])
	}
	if sum := um["promptTokenCount"].(int) + um["candidatesTokenCount"].(int) + um["thoughtsTokenCount"].(int); sum != um["totalTokenCount"].(int) {
		t.Errorf("分项之和 %d != totalTokenCount %d", sum, um["totalTokenCount"])
	}
}

// OpenAI 出站靠结构体标签自动带出 reasoning_content，这里钉住它不被改名或漏掉。
func TestRenderOpenAIEmitsReasoningContent(t *testing.T) {
	raw, err := json.Marshal(respWith("ans", "thinking hard", "sig"))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var got struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Choices[0].Message.ReasoningContent != "thinking hard" {
		t.Errorf("reasoning_content 未随响应输出: %s", raw)
	}
	if got.Choices[0].Message.Content != "ans" {
		t.Errorf("正文与思维链混在一起了: %s", raw)
	}
	// 签名是 Anthropic 专属概念，不该漏进 OpenAI 响应体
	if strings.Contains(string(raw), `"signature"`) {
		t.Errorf("Anthropic 签名泄漏进 OpenAI 响应: %s", raw)
	}
}

// 流式：Anthropic 的块序列必须 start/stop 成对、索引递增、thinking 在 text 前、
// 签名落在思维块关闭之前。顺序错一个客户端状态机就会崩。
func TestStreamAnthropicBlockOrdering(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(Anthropic, rec, rec, "virt")

	sw.WriteChunk(ir.ChatChunk{Choices: []ir.ChunkChoice{{Index: 0,
		Delta: ir.Delta{ReasoningContent: "think-1"}}}})
	sw.WriteChunk(ir.ChatChunk{Choices: []ir.ChunkChoice{{Index: 0,
		Delta: ir.Delta{ReasoningContent: "think-2"}}}})
	sw.WriteChunk(ir.ChatChunk{Choices: []ir.ChunkChoice{{Index: 0,
		Delta: ir.Delta{ReasoningSignature: "sig-9"}}}})
	sw.WriteChunk(ir.ChatChunk{Choices: []ir.ChunkChoice{{Index: 0,
		Delta: ir.Delta{Content: "hello"}}}})
	sw.WriteChunk(ir.ChatChunk{Choices: []ir.ChunkChoice{{Index: 0, FinishReason: strPtr("stop")}}})
	sw.Finish()

	events := parseSSEEvents(rec.Body.String())

	var kinds []string
	thinkingIdx, textIdx := -1, -1
	sigInBlock := map[int]int{} // block index -> 该块内出现 signature_delta 的次数
	starts, stops := 0, 0
	for _, e := range events {
		switch e.typeName {
		case "content_block_start":
			starts++
			var m map[string]interface{}
			_ = json.Unmarshal([]byte(e.data), &m)
			cb, _ := m["content_block"].(map[string]interface{})
			kinds = append(kinds, cb["type"].(string))
		case "content_block_stop":
			stops++
		case "content_block_delta":
			var m map[string]interface{}
			_ = json.Unmarshal([]byte(e.data), &m)
			d, _ := m["delta"].(map[string]interface{})
			idx, _ := m["index"].(float64)
			if d["type"] == "signature_delta" {
				sigInBlock[int(idx)]++
			}
			if d["type"] == "thinking_delta" {
				if thinkingIdx < 0 {
					thinkingIdx = int(idx)
				}
				if d["thinking"] == "" {
					t.Error("thinking_delta 载荷为空")
				}
			}
			if d["type"] == "text_delta" && textIdx < 0 {
				textIdx = int(idx)
			}
		}
	}

	if got := strings.Join(kinds, ","); got != "thinking,text" {
		t.Errorf("块类型顺序 = %s, want thinking,text", got)
	}
	if thinkingIdx >= textIdx {
		t.Errorf("思维块索引 %d 应小于正文块 %d", thinkingIdx, textIdx)
	}
	if starts != stops {
		t.Errorf("start %d != stop %d，块不成对关闭", starts, stops)
	}
	if sigInBlock[thinkingIdx] != 1 {
		t.Errorf("签名应在思维块内发出一次, got %d 次（块索引=%d）", sigInBlock[thinkingIdx], thinkingIdx)
	}
	// message_start 必须最先、message_stop 必须最后
	if events[0].typeName != "message_start" {
		t.Errorf("首事件 = %s, want message_start", events[0].typeName)
	}
	if events[len(events)-1].typeName != "message_stop" {
		t.Errorf("末事件 = %s, want message_stop", events[len(events)-1].typeName)
	}
}

// 流式 Gemini：thought part 与正文 part 分开，且各自只出现一次。
func TestStreamGeminiThoughtParts(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(Gemini, rec, rec, "virt")
	sw.WriteChunk(ir.ChatChunk{Choices: []ir.ChunkChoice{{Index: 0,
		Delta: ir.Delta{ReasoningContent: "deep thought", Content: "final"}}}})
	sw.Finish()

	var sawThought, sawText bool
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			continue
		}
		cands, _ := m["candidates"].([]interface{})
		if len(cands) == 0 {
			continue
		}
		content, _ := cands[0].(map[string]interface{})["content"].(map[string]interface{})
		parts, _ := content["parts"].([]interface{})
		for _, p := range parts {
			pm, _ := p.(map[string]interface{})
			if pm["thought"] == true {
				sawThought = true
			} else if pm["text"] == "final" {
				sawText = true
			}
		}
	}
	if !sawThought {
		t.Error("流式思维链未带 thought 标记")
	}
	if !sawText {
		t.Error("流式正文未独立成 part")
	}
}

type sseEvent struct {
	typeName string
	data     string
}

// parseSSEEvents 按 event/data 成对解析出站 SSE。
func parseSSEEvents(raw string) []sseEvent {
	var out []sseEvent
	var pending string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event: "):
			pending = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			out = append(out, sseEvent{typeName: pending, data: strings.TrimPrefix(line, "data: ")})
			pending = ""
		}
	}
	return out
}

func strPtr(s string) *string { return &s }
