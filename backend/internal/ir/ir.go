// Package ir 定义网关的内部规范表示（Internal Representation）。
// 采用 OpenAI 形态作为枢纽（hub-and-spoke）：入站协议与上游协议各自只需
// 与 IR 互转一次，避免 N×N 的两两转换组合。
package ir

// ChatRequest 规范聊天请求（OpenAI 兼容超集）。
type ChatRequest struct {
	Model            string      `json:"model"`
	Messages         []Message   `json:"messages"`
	Temperature      *float64    `json:"temperature,omitempty"`
	TopP             *float64    `json:"top_p,omitempty"`
	MaxTokens        *int        `json:"max_tokens,omitempty"`
	Stream           bool        `json:"stream,omitempty"`
	StreamOptions    *StreamOptions `json:"stream_options,omitempty"`
	Tools            []Tool      `json:"tools,omitempty"`
	ToolChoice       interface{} `json:"tool_choice,omitempty"`
	FrequencyPenalty *float64    `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64    `json:"presence_penalty,omitempty"`
	Stop             []string    `json:"stop,omitempty"`
	N                *int        `json:"n,omitempty"`
	System           string      `json:"-"` // 由 Anthropic/Gemini 顶层 system 提取，OpenAI 走 messages
}

// StreamOptions 流式选项（OpenAI: {"include_usage": true} 让末帧返回用量）。
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Message 聊天消息。Content 可为 string 或 []ContentPart（多模态）。
type Message struct {
	Role       string        `json:"role"`
	Content    interface{}   `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// ContentPart 多模态内容片段。
type ContentPart struct {
	Type     string    `json:"type"`               // text | image_url | image
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL 图片内容（OpenAI 形态：url 可为 http(s) 或 data:base64）。
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// Tool 工具定义（OpenAI function 形态）。
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function 工具函数。
type Function struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// ToolCall 工具调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 工具调用函数体。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponse 规范聊天响应。
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice 响应选项。
type Choice struct {
	Index        int      `json:"index"`
	Message      Message  `json:"message"`
	FinishReason string   `json:"finish_reason"`
}

// Usage token 用量（含缓存细分，供计费使用）。
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	CachedTokens        int `json:"-"` // prompt 中命中缓存的 token（若上游返回）
}

// ChatChunk 流式响应块。
type ChatChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"` // 末帧用量（include_usage 时）
}

// ChunkChoice 流式选项。
type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// Delta 流式增量。
type Delta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// TextOf 提取消息的纯文本内容（string 或多模态中的 text 片段拼接）。
func (m Message) TextOf() string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []ContentPart:
		out := ""
		for _, p := range c {
			if p.Type == "text" {
				out += p.Text
			}
		}
		return out
	case []interface{}:
		out := ""
		for _, item := range c {
			if pm, ok := item.(map[string]interface{}); ok {
				if t, _ := pm["text"].(string); t != "" {
					out += t
				}
			}
		}
		return out
	}
	return ""
}
