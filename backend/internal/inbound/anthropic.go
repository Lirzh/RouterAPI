package inbound

import (
	"encoding/json"

	"github.com/routerapi/backend/internal/ir"
)

// ============ Anthropic 入站请求结构 ============

type anthropicRequest struct {
	Model         string             `json:"model"`
	Messages      []anthropicMessage `json:"messages"`
	System        interface{}        `json:"system"` // string 或 [{type:text,text}]
	MaxTokens     *int               `json:"max_tokens"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	StopSequences []string           `json:"stop_sequences"`
	Stream        bool               `json:"stream"`
	Tools         []anthropicTool    `json:"tools"`
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
	Type     string          `json:"type"` // text | image | tool_use | tool_result
	Text     string          `json:"text"`
	Source   *anthropicImage `json:"source"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	Content  interface{}     `json:"content"`
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
	req := &ir.ChatRequest{
		Model:       ar.Model,
		Temperature: ar.Temperature,
		TopP:        ar.TopP,
		MaxTokens:   ar.MaxTokens,
		Stream:      ar.Stream,
		Stop:        ar.StopSequences,
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
			parts = append(parts, ir.ContentPart{Type: "text", Text: b.Text})
			singleText += b.Text
		case "image":
			allText = false
			if b.Source != nil {
				url := b.Source.URL
				if b.Source.Type == "base64" && b.Source.Data != "" {
					url = "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				}
				parts = append(parts, ir.ContentPart{Type: "image_url", ImageURL: &ir.ImageURL{URL: url}})
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
		if txt := choice.Message.TextOf(); txt != "" {
			content = append(content, map[string]interface{}{"type": "text", "text": txt})
		}
		for _, tc := range choice.Message.ToolCalls {
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
		"id":      id,
		"type":    "message",
		"role":    "assistant",
		"model":   virtualModel,
		"content": content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		},
	}
}

// ============ Anthropic 流式事件 ============

func (sw *StreamWriter) writeAnthropicChunk(chunk ir.ChatChunk) {
	if !sw.antStarted {
		// message_start
		sw.writeEvent("message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id": sw.antID, "type": "message", "role": "assistant", "model": sw.model,
				"content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]interface{}{"input_tokens": sw.usage.PromptTokens, "output_tokens": 0},
			},
		})
		// content_block_start
		sw.writeEvent("content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]interface{}{"type": "text", "text": ""},
		})
		sw.antStarted = true
		sw.antBlockOpen = true
	}
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != "" {
			sw.writeEvent("content_block_delta", map[string]interface{}{
				"type": "content_block_delta", "index": 0,
				"delta": map[string]interface{}{"type": "text_delta", "text": ch.Delta.Content},
			})
		}
	}
}

func (sw *StreamWriter) finishAnthropic() {
	if !sw.antStarted {
		// 无任何块时也产出合法的 message_start，保证客户端状态机完整
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
	if sw.antBlockOpen {
		sw.writeEvent("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
		sw.antBlockOpen = false
	}
	u := sw.Usage()
	sw.writeEvent("message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": mapFinishToAnthropic(sw.antStop), "stop_sequence": nil},
		"usage": map[string]interface{}{"output_tokens": u.CompletionTokens},
	})
	sw.writeEvent("message_stop", map[string]interface{}{"type": "message_stop"})
}
