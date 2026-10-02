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
//
// 解析后统一打上 Inbound 词汇表标记：Extra 里的未识别字段属于哪种协议，
// 决定了上游适配器能不能把它们原样回填。
func Parse(protocol Protocol, body []byte, pathModel string) (*ir.ChatRequest, error) {
	var req *ir.ChatRequest
	var err error
	switch protocol {
	case Anthropic:
		req, err = parseAnthropic(body)
	case Gemini:
		req, err = parseGemini(body, pathModel)
	default:
		req, err = parseOpenAI(body)
	}
	if err != nil {
		return nil, err
	}
	req.Inbound = string(protocol)
	return req, nil
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
	antStarted bool
	// Anthropic 的响应是有序内容块序列（thinking 块必须排在 text 块之前，
	// 索引随块递增，签名 delta 必须落在思维块关闭之前）。所以不能像原先那样
	// 只记"有没有开块"，要记住开的是哪一块。
	antBlockIdx   int    // 当前打开块的索引；-1 表示当前没有打开的块
	antBlockCount int    // 已开过的块数，用于给下一块分配递增索引
	antBlockKind  string // thinking | text
	antPendingSig string // 思维块签名，在其关闭前补发
	antID         string
	antStop       string

	// Gemini 流式状态
	gemFinishSent bool
	gemFinish     string

	// OpenAI 流式：是否已从上游拿到 usage
	oaiGotUsage bool
}

// NewStreamWriter 创建流式写出器。
func NewStreamWriter(protocol Protocol, w http.ResponseWriter, flusher http.Flusher, virtualModel string) *StreamWriter {
	return &StreamWriter{
		protocol:    protocol,
		w:           w,
		flusher:     flusher,
		model:       virtualModel,
		antID:       "msg_" + shortID(),
		antBlockIdx: -1, // 起始为"无打开块"，首个 ensureBlock 会开出 index 0
	}
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

// estimateImageTokens 单张图片按固定额度计入输入估算。
// 真实值取决于图片尺寸与上游分块策略（OpenAI 高低细节差好几倍），估算阶段取个不低的定值。
const estimateImageTokens = 1000

// EstimatePromptTokens 估算一次请求的输入 token 上界，供额度预留判断"够不够放行"。
//
// 只要上界，不要精确：实际收费始终按上游返回的真实用量结算。宁可估高——估低了
// 实际用量会超过预留额、被封顶成少收；估高了只是多占一点在途额度、结算时就还回去。
//
// 覆盖三部分：消息文本（含 tool 调用的参数）、工具声明 JSON、图片固定额度。
// safetyPercent 是对字符启发式的上浮系数（代码与 JSON 的实际 token 密度明显高于 4 字符/token）。
func EstimatePromptTokens(req *ir.ChatRequest, safetyPercent int) int {
	var b strings.Builder
	for _, msg := range req.Messages {
		b.WriteString(msg.TextOf())
		b.WriteByte('\n')
		for _, tc := range msg.ToolCalls {
			b.WriteString(tc.Function.Name)
			b.WriteString(tc.Function.Arguments)
			b.WriteByte('\n')
		}
	}
	if req.System != "" {
		b.WriteString(req.System)
		b.WriteByte('\n')
	}
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			b.Write(raw)
		}
	}

	n := EstimateTokens(b.String()) + countImages(req)*estimateImageTokens
	if safetyPercent > 0 {
		n += n * safetyPercent / 100
	}
	return n
}

// countImages 统计请求里的图片片段数。
// Content 有三种形态：OpenAI 入站解析后是 []interface{}，Anthropic/Gemini 入站构造为
// []ir.ContentPart，纯文本则是 string——三种都得走，否则多模态请求的预留会系统性低估。
func countImages(req *ir.ChatRequest) int {
	count := 0
	for _, msg := range req.Messages {
		switch c := msg.Content.(type) {
		case []ir.ContentPart:
			for _, p := range c {
				if p.Type == "image_url" || p.Type == "image" {
					count++
				}
			}
		case []interface{}:
			for _, item := range c {
				pm, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				if t, _ := pm["type"].(string); t == "image_url" || t == "image" {
					count++
				}
			}
		}
	}
	return count
}
