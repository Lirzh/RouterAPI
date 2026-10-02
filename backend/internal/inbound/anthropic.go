package inbound

import (
	"encoding/json"

	"github.com/routerapi/backend/internal/ir"
)

// ============ Anthropic 入站请求结构 ============

type anthropicRequest struct {
	Model         string               `json:"model"`
	Messages      []anthropicMessage   `json:"messages"`
	System        interface{}          `json:"system"` // string 或 [{type:text,text}]
	MaxTokens     *int                 `json:"max_tokens"`
	Temperature   *float64             `json:"temperature"`
	TopP          *float64             `json:"top_p"`
	StopSequences []string             `json:"stop_sequences"`
	Stream        bool                 `json:"stream"`
	Tools         []anthropicTool      `json:"tools"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string 或 []block
}

type anthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"` // auto | any | tool
	Name string `json:"name"`
}

// content block（解析用）
type anthropicBlock struct {
	Type      string          `json:"type"` // text | image | tool_use | tool_result | thinking
	Text      string          `json:"text"`
	Source    *anthropicImage `json:"source"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   interface{}     `json:"content"`
	// 缓存断点。不带它的话上游永远不会产生 cache_read_input_tokens，
	// 缓存折扣定价就成了永远命中不了的空设置。
	CacheControl *ir.CacheControl `json:"cache_control"`
}

type anthropicImage struct {
	Type      string `json:"type"` // base64 | url
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

// parseAnthropic 将 Anthropic /v1/messages 请求转换为 IR。
func parseAnthropic(body []byte) (*ir.ChatRequest, error) {
	var ar anthropicRequest
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, err
	}
	// 按 Anthropic 自己的词汇表挑出 IR/DTO 都没声明的字段（thinking、context_management 等）。
	// 判定必须用 anthropicRequest 的键集合而不是 IR 的：否则 model/messages 这些
	// 两边都有的字段会被误判成"未识别"，回填时变成重复键。
	extra, err := ir.CollectUnknown(body, ir.JSONKeys(ar))
	if err != nil {
		return nil, err
	}
	req := &ir.ChatRequest{
		Model:       ar.Model,
		Temperature: ar.Temperature,
		TopP:        ar.TopP,
		MaxTokens:   ar.MaxTokens,
		Stream:      ar.Stream,
		Stop:        ar.StopSequences,
		Extra:       extra,
	}

	// system：字符串或文本块数组 → 前置 system 消息
	if sys := anthropicSystemText(ar.System); sys != "" {
		req.Messages = append(req.Messages, ir.Message{Role: "system", Content: sys})
		req.System = sys
	}

	// messages
	for _, m := range ar.Messages {
		req.Messages = append(req.Messages, convertAnthropicMessage(m)...)
	}

	// tools → OpenAI function 形态
	for _, t := range ar.Tools {
		req.Tools = append(req.Tools, ir.Tool{
			Type: "function",
			Function: ir.Function{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	if ar.ToolChoice != nil {
		switch ar.ToolChoice.Type {
		case "any":
			req.ToolChoice = "required"
		case "tool":
			req.ToolChoice = map[string]interface{}{
				"type":     "function",
				"function": map[string]interface{}{"name": ar.ToolChoice.Name},
			}
		default:
			req.ToolChoice = "auto"
		}
	}
	return req, nil
}

func anthropicSystemText(sys interface{}) string {
	switch s := sys.(type) {
	case string:
		return s
	case []interface{}:
		out := ""
		for _, item := range s {
			if bm, ok := item.(map[string]interface{}); ok {
				if t, _ := bm["text"].(string); t != "" {
					out += t
				}
			}
		}
		return out
	}
	return ""
}

// convertAnthropicMessage 将一条 Anthropic 消息转换为一条或多条 IR 消息
// （tool_result 块会拆成独立的 role=tool 消息）。
func convertAnthropicMessage(m anthropicMessage) []ir.Message {
	role := m.Role
	if role != "assistant" {
		role = "user"
	}
	// content 为字符串
	if s, ok := m.Content.(string); ok {
		return []ir.Message{{Role: role, Content: s}}
	}
	arr, ok := m.Content.([]interface{})
	if !ok {
		return []ir.Message{{Role: role, Content: ""}}
	}

	var parts []ir.ContentPart
	var toolCalls []ir.ToolCall
	var toolResults []ir.Message
	singleText := ""
	allText := true

	for _, item := range arr {
		raw, _ := json.Marshal(item)
		var b anthropicBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			continue
		}
		switch b.Type {
		case "text":
			parts = append(parts, ir.ContentPart{Type: "text", Text: b.Text, CacheControl: b.CacheControl})
			singleText += b.Text
			if b.CacheControl != nil {
				// 块级断点依赖 content 数组的结构。压平成字符串会连块带断点一起丢掉，
				// 而这是纯文本消息的常见形态（长 system/prefix + 一句提问），所以
				// 不修就等于所有文本消息都拿不到缓存。
				allText = false
			}
		case "image":
			allText = false
			if b.Source != nil {
				url := b.Source.URL
				if b.Source.Type == "base64" && b.Source.Data != "" {
					url = "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				}
				parts = append(parts, ir.ContentPart{Type: "image_url", ImageURL: &ir.ImageURL{URL: url}, CacheControl: b.CacheControl})
			}
		case "tool_use":
			allText = false
			toolCalls = append(toolCalls, ir.ToolCall{
				ID:   b.ID,
				Type: "function",
				Function: ir.ToolCallFunction{
					Name:      b.Name,
					Arguments: string(b.Input),
				},
			})
		case "tool_result":
			allText = false
			toolResults = append(toolResults, ir.Message{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    anthropicResultText(b.Content),
			})
		}
	}

	msg := ir.Message{Role: role, ToolCalls: toolCalls}
	if allText && len(toolCalls) == 0 {
		msg.Content = singleText
	} else if len(parts) > 0 {
		msg.Content = parts
	} else if len(toolCalls) > 0 {
		msg.Content = ""
	} else {
		msg.Content = singleText
	}

	out := []ir.Message{}
	// tool_result 通常出现在 user 轮，拆到前面（OpenAI 期望 role=tool 独立消息）
	out = append(out, toolResults...)
	if len(toolCalls) > 0 || len(parts) > 0 || singleText != "" {
		out = append(out, msg)
	}
	if len(out) == 0 {
		out = append(out, ir.Message{Role: role, Content: ""})
	}
	return out
}

func anthropicResultText(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		out := ""
		for _, item := range c {
			if bm, ok := item.(map[string]interface{}); ok {
				if t, _ := bm["text"].(string); t != "" {
					out += t
				}
			}
		}
		return out
	}
	return ""
}

// ============ Anthropic 出站响应 ============

// renderAnthropicResponse 将 IR 响应渲染为 Anthropic message 结构。
func renderAnthropicResponse(resp *ir.ChatResponse, virtualModel string) interface{} {
	content := make([]map[string]interface{}, 0)
	stopReason := "end_turn"
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		msg := choice.Message
		// thinking 块必须排在 text 块之前，且带签名——这是 Anthropic 对回传历史的要求。
		if msg.ReasoningContent != "" {
			blk := map[string]interface{}{"type": "thinking", "thinking": msg.ReasoningContent}
			if msg.ReasoningSignature != "" {
				blk["signature"] = msg.ReasoningSignature
			}
			content = append(content, blk)
		}
		if txt := msg.TextOf(); txt != "" {
			content = append(content, map[string]interface{}{"type": "text", "text": txt})
		}
		for _, tc := range msg.ToolCalls {
			var input interface{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			content = append(content, map[string]interface{}{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": input,
			})
		}
		stopReason = mapFinishToAnthropic(choice.FinishReason)
	}
	if len(content) == 0 {
		content = append(content, map[string]interface{}{"type": "text", "text": ""})
	}
	id := resp.ID
	if id == "" {
		id = "msg_" + shortID()
	}
	return map[string]interface{}{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         virtualModel,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		},
	}
}

// ============ Anthropic 流式事件 ============

// startAnthropicMessage 发出且只发出一次 message_start。
func (sw *StreamWriter) startAnthropicMessage() {
	if sw.antStarted {
		return
	}
	sw.writeEvent("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": sw.antID, "type": "message", "role": "assistant", "model": sw.model,
			"content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]interface{}{"input_tokens": sw.usage.PromptTokens, "output_tokens": 0},
		},
	})
	sw.antStarted = true
}

// ensureAnthropicBlock 保证当前打开的是指定类型的内容块，必要时先关闭上一块，
// 返回该块索引。Anthropic 客户端按 index 累积内容，索引必须单调递增、
// start/stop 必须成对，且 thinking 块只能出现在 text 块之前。
func (sw *StreamWriter) ensureAnthropicBlock(kind string) int {
	if sw.antBlockKind == kind {
		return sw.antBlockIdx
	}
	sw.closeAnthropicBlock()
	// 索引必须全局递增：关闭一块只是让它停止接收 delta，不能把计数器退回去，
	// 否则第二个块会复用 index 0，客户端会把正文 delta 追加进思维块。
	sw.antBlockIdx = sw.antBlockCount
	sw.antBlockCount++
	sw.antBlockKind = kind
	block := map[string]interface{}{"type": kind}
	if kind == "thinking" {
		block["thinking"] = ""
	} else {
		block["text"] = ""
	}
	sw.writeEvent("content_block_start", map[string]interface{}{
		"type": "content_block_start", "index": sw.antBlockIdx, "content_block": block,
	})
	return sw.antBlockIdx
}

// closeAnthropicBlock 关闭当前内容块。思维块关闭前必须补发签名，
// 否则客户端拿到的历史缺签名、回传时会被 Anthropic 拒收。
func (sw *StreamWriter) closeAnthropicBlock() {
	if sw.antBlockIdx < 0 {
		return
	}
	if sw.antBlockKind == "thinking" && sw.antPendingSig != "" {
		sw.writeEvent("content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": sw.antBlockIdx,
			"delta": map[string]interface{}{"type": "signature_delta", "signature": sw.antPendingSig},
		})
		sw.antPendingSig = ""
	}
	sw.writeEvent("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": sw.antBlockIdx})
	sw.antBlockIdx = -1
	sw.antBlockKind = ""
}

func (sw *StreamWriter) writeAnthropicChunk(chunk ir.ChatChunk) {
	sw.startAnthropicMessage()
	for _, ch := range chunk.Choices {
		if ch.Delta.ReasoningContent != "" {
			idx := sw.ensureAnthropicBlock("thinking")
			sw.writeEvent("content_block_delta", map[string]interface{}{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]interface{}{"type": "thinking_delta", "thinking": ch.Delta.ReasoningContent},
			})
		}
		if ch.Delta.ReasoningSignature != "" {
			if sw.antBlockKind == "thinking" {
				sw.writeEvent("content_block_delta", map[string]interface{}{
					"type": "content_block_delta", "index": sw.antBlockIdx,
					"delta": map[string]interface{}{"type": "signature_delta", "signature": ch.Delta.ReasoningSignature},
				})
			} else {
				sw.antPendingSig += ch.Delta.ReasoningSignature
			}
		}
		if ch.Delta.Content != "" {
			idx := sw.ensureAnthropicBlock("text")
			sw.writeEvent("content_block_delta", map[string]interface{}{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]interface{}{"type": "text_delta", "text": ch.Delta.Content},
			})
		}
	}
}

func (sw *StreamWriter) finishAnthropic() {
	sw.startAnthropicMessage() // 无任何块时也产出合法的 message_start，保证客户端状态机完整
	sw.closeAnthropicBlock()
	u := sw.Usage()
	sw.writeEvent("message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": mapFinishToAnthropic(sw.antStop), "stop_sequence": nil},
		"usage": map[string]interface{}{"output_tokens": u.CompletionTokens},
	})
	sw.writeEvent("message_stop", map[string]interface{}{"type": "message_stop"})
}
