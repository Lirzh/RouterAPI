// Package inbound 实现"入站协议 -> IR -> 入站协议"的转换（hub-and-spoke 的入站侧）。
// 支持客户端以 OpenAI / Anthropic / Gemini 三种协议调用网关，网关内部统一为 IR，
// 再由上游适配器转换为对应供应商协议；响应与流式事件按入站协议回渲染。
package inbound

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/routerapi/backend/internal/ir"
)

// Protocol 入站协议标识。
type Protocol string

const (
	OpenAI    Protocol = "openai"
	Anthropic Protocol = "anthropic"
	Gemini    Protocol = "gemini"
)

// Parse 将入站请求体解析为 IR。pathModel 用于 Gemini（模型名在 URL 路径中）。
func Parse(protocol Protocol, body []byte, pathModel string) (*ir.ChatRequest, error) {
	switch protocol {
	case Anthropic:
		return parseAnthropic(body)
	case Gemini:
		return parseGemini(body, pathModel)
	default:
		return parseOpenAI(body)
	}
}

// RenderResponse 将 IR 响应渲染为入站协议的非流式响应体（可直接 c.JSON）。
func RenderResponse(protocol Protocol, resp *ir.ChatResponse, virtualModel string) interface{} {
	resp.Model = virtualModel
	switch protocol {
	case Anthropic:
		return renderAnthropicResponse(resp, virtualModel)
	case Gemini:
		return renderGeminiResponse(resp, virtualModel)
	default:
		return resp
	}
}

// StreamWriter 按入站协议将 IR 流式块写出为对应 SSE 事件，并累计用量与文本。
type StreamWriter struct {
	protocol Protocol
	w        http.ResponseWriter
	flusher  http.Flusher
	model    string // 虚拟模型名

	usage ir.Usage
	text  strings.Builder

	// Anthropic 流式状态
	antStarted   bool
	antBlockOpen bool
	antID        string
	antStop      string

	// Gemini 流式状态
	gemFinishSent bool
	gemFinish     string

	// OpenAI 流式：是否已从上游拿到 usage
	oaiGotUsage bool
}

// NewStreamWriter 创建流式写出器。
func NewStreamWriter(protocol Protocol, w http.ResponseWriter, flusher http.Flusher, virtualModel string) *StreamWriter {
	return &StreamWriter{protocol: protocol, w: w, flusher: flusher, model: virtualModel, antID: "msg_" + shortID()}
}

// WriteChunk 写出一个 IR 流式块（按协议渲染）。
func (sw *StreamWriter) WriteChunk(chunk ir.ChatChunk) {
	// 累计用量（上游若在流中返回 usage 则采信）
	if chunk.Usage != nil {
		sw.usage = *chunk.Usage
		sw.oaiGotUsage = true
	}
	// 累计文本与结束原因
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != "" {
			sw.text.WriteString(ch.Delta.Content)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			sw.antStop = *ch.FinishReason
			sw.gemFinish = mapFinishToGemini(*ch.FinishReason)
		}
	}

	switch sw.protocol {
	case Anthropic:
		sw.writeAnthropicChunk(chunk)
	case Gemini:
		sw.writeGeminiChunk(chunk)
	default:
		sw.writeOpenAIChunk(chunk)
	}
}

// Finish 写出流结束事件。
func (sw *StreamWriter) Finish() {
	switch sw.protocol {
	case Anthropic:
		sw.finishAnthropic()
	case Gemini:
		sw.finishGemini()
	default:
		sw.finishOpenAI()
	}
}

// Usage 返回累计用量；若上游未提供，用累计文本做兜底估算。
func (sw *StreamWriter) Usage() ir.Usage {
	u := sw.usage
	if u.CompletionTokens == 0 && sw.text.Len() > 0 {
		u.CompletionTokens = EstimateTokens(sw.text.String())
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

// Text 返回累计的助手输出文本（用于计费兜底/日志）。
func (sw *StreamWriter) Text() string { return sw.text.String() }

// writeRaw 写出一行 SSE data（并 flush）。
func (sw *StreamWriter) writeRaw(s string) {
	_, _ = sw.w.Write([]byte(s))
	sw.flusher.Flush()
}

// writeEvent 写出 Anthropic 风格 event+data 两行。
func (sw *StreamWriter) writeEvent(event string, data interface{}) {
	b, _ := json.Marshal(data)
	sw.writeRaw("event: " + event + "\ndata: " + string(b) + "\n\n")
}

// writeData 写出 data 行。
func (sw *StreamWriter) writeData(data interface{}) {
	b, _ := json.Marshal(data)
	sw.writeRaw("data: " + string(b) + "\n\n")
}

// EstimateTokens 轻量 token 估算：英文约 4 字符/token，CJK 约 1 字/token。
// 仅用于上游未返回 usage 时的兜底（正式计费以上游 usage 为准）。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	cjk := 0
	other := 0
	for _, r := range text {
		if r >= 0x2E80 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF || r >= 0xFF00 && r <= 0xFFEF {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4
}
