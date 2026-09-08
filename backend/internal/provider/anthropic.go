package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/routerapi/backend/internal/errkit"
	"github.com/sirupsen/logrus"
)

// AnthropicProvider 适配 Anthropic Claude API
type AnthropicProvider struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewAnthropicProvider(name, baseURL, apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 300 * time.Second},
	}
}

func (p *AnthropicProvider) Name() string { return p.name }

// anthropicRequest Anthropic API 请求格式
type anthropicRequest struct {
	Model         string             `json:"model"`
	Messages      []anthropicMessage `json:"messages"`
	System        string             `json:"system,omitempty"`
	MaxTokens     int                `json:"max_tokens"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Tools         []anthropicToolDef `json:"tools,omitempty"`
	ToolChoice    interface{}        `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type anthropicToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema,omitempty"`
}

// anthropicContentBlock 内容块：text / image / tool_use / tool_result
type anthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	Source    *anthropicImageSource `json:"source,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   interface{}           `json:"content,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicResponse Anthropic API 响应格式
type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (p *AnthropicProvider) convertRequest(req *ChatRequest) *anthropicRequest {
	ar := &anthropicRequest{Model: req.Model, MaxTokens: 4096}
	if req.MaxTokens != nil {
		ar.MaxTokens = *req.MaxTokens
	}
	ar.Temperature = req.Temperature
	ar.TopP = req.TopP
	ar.StopSequences = req.Stop

	// 工具定义
	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, anthropicToolDef{
			Name: t.Function.Name, Description: t.Function.Description, InputSchema: t.Function.Parameters,
		})
	}
	ar.ToolChoice = convertToolChoiceToAnthropic(req.ToolChoice)

	// 消息：system 合并；tool 结果累积为 user 的 tool_result 块
	var pendingToolResults []anthropicContentBlock
	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			ar.Messages = append(ar.Messages, anthropicMessage{Role: "user", Content: pendingToolResults})
			pendingToolResults = nil
		}
	}
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			flushToolResults()
			if s := msg.TextOf(); s != "" {
				if ar.System != "" {
					ar.System += "\n"
				}
				ar.System += s
			}
		case "tool":
			pendingToolResults = append(pendingToolResults, anthropicContentBlock{
				Type: "tool_result", ToolUseID: msg.ToolCallID, Content: msg.TextOf(),
			})
		default:
			role := "user"
			if msg.Role == "assistant" {
				role = "assistant"
			}
			blocks := buildAnthropicBlocks(msg)
			if role == "user" && len(pendingToolResults) > 0 {
				blocks = append(pendingToolResults, blocks...)
				pendingToolResults = nil
			} else {
				flushToolResults()
			}
			if len(blocks) == 0 {
				blocks = []anthropicContentBlock{{Type: "text", Text: ""}}
			}
			ar.Messages = append(ar.Messages, anthropicMessage{Role: role, Content: blocks})
		}
	}
	flushToolResults()
	return ar
}

// buildAnthropicBlocks 将 IR 消息内容转换为 Anthropic 内容块（text/image/tool_use）。
func buildAnthropicBlocks(msg Message) []anthropicContentBlock {
	blocks := make([]anthropicContentBlock, 0)
	appendParts := func(parts []interface{}) {
		for _, item := range parts {
			pm, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text":
				if s, _ := pm["text"].(string); s != "" {
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: s})
				}
			case "image_url":
				if iu, ok := pm["image_url"].(map[string]interface{}); ok {
					if u, _ := iu["url"].(string); u != "" {
						if blk, ok := imageBlockFromDataURL(u); ok {
							blocks = append(blocks, blk)
						}
					}
				}
			}
		}
	}
	switch c := msg.Content.(type) {
	case string:
		if c != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: c})
		}
	case []ContentPart:
		for _, p := range c {
			if p.Type == "text" && p.Text != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: p.Text})
			} else if p.Type == "image_url" && p.ImageURL != nil {
				if blk, ok := imageBlockFromDataURL(p.ImageURL.URL); ok {
					blocks = append(blocks, blk)
				}
			}
		}
	case []interface{}:
		appendParts(c)
	}
	// assistant 的 tool_calls → tool_use 块
	for _, tc := range msg.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		blocks = append(blocks, anthropicContentBlock{
			Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args),
		})
	}
	return blocks
}

// imageBlockFromDataURL 将 data URI 或 http(s) URL 转为 Anthropic image 块。
func imageBlockFromDataURL(url string) (anthropicContentBlock, bool) {
	if strings.HasPrefix(url, "data:") {
		// data:<mediaType>;base64,<data>
		comma := strings.Index(url, ",")
		semi := strings.Index(url, ";")
		if comma > 0 && semi > 5 && semi < comma {
			mediaType := url[5:semi]
			data := url[comma+1:]
			return anthropicContentBlock{
				Type:   "image",
				Source: &anthropicImageSource{Type: "base64", MediaType: mediaType, Data: data},
			}, true
		}
		return anthropicContentBlock{}, false
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return anthropicContentBlock{
			Type:   "image",
			Source: &anthropicImageSource{Type: "url", URL: url},
		}, true
	}
	return anthropicContentBlock{}, false
}

// convertToolChoiceToAnthropic 将 IR/OpenAI tool_choice 转为 Anthropic 形态。
func convertToolChoiceToAnthropic(tc interface{}) interface{} {
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]interface{}{"type": "auto"}
		case "required":
			return map[string]interface{}{"type": "any"}
		case "none":
			return nil
		}
	case map[string]interface{}:
		if fn, ok := v["function"].(map[string]interface{}); ok {
			if name, _ := fn["name"].(string); name != "" {
				return map[string]interface{}{"type": "tool", "name": name}
			}
		}
	}
	return nil
}

func (p *AnthropicProvider) convertResponse(resp *anthropicResponse) *ChatResponse {
	var text string
	var toolCalls []ToolCall
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			text += c.Text
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID: c.ID, Type: "function",
				Function: ToolCallFunction{Name: c.Name, Arguments: string(c.Input)},
			})
		}
	}

	msg := Message{Role: "assistant", Content: text, ToolCalls: toolCalls}
	return &ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []Choice{
			{Index: 0, Message: msg, FinishReason: mapAnthropicStopToOpenAI(resp.StopReason)},
		},
		Usage: Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}

func (p *AnthropicProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	ar := p.convertRequest(req)
	body, err := json.Marshal(ar)
	if err != nil {
		return nil, err
	}

	url := p.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errkit.ChannelError(0, "读取响应失败: "+err.Error(), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}

	var arResp anthropicResponse
	if err := json.Unmarshal(respBody, &arResp); err != nil {
		return nil, err
	}

	return p.convertResponse(&arResp), nil
}

func (p *AnthropicProvider) ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	ar := p.convertRequest(req)
	ar.Stream = true
	body, err := json.Marshal(ar)
	if err != nil {
		return nil, err
	}

	url := p.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}

	ch := make(chan ChatChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		decoder := newSSEDecoder(resp.Body)
		var model string
		var inputTokens, outputTokens int
		stopReason := ""
		stopReceived := false
		for {
			data, err := decoder.Next()
			if err != nil {
				if err != io.EOF {
					logrus.Debugf("Anthropic SSE 结束: %v", err)
				}
				break
			}

			var event struct {
				Type  string `json:"type"`
				Delta struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Message struct {
					Model string `json:"model"`
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "message_start":
				if event.Message.Model != "" {
					model = event.Message.Model
				}
				inputTokens = event.Message.Usage.InputTokens
				outputTokens = event.Message.Usage.OutputTokens
			case "content_block_delta":
				if event.Delta.Text != "" {
					chunk := ChatChunk{
						ID: "chatcmpl-ant", Object: "chat.completion.chunk",
						Created: time.Now().Unix(), Model: model,
						Choices: []ChunkChoice{{Index: 0, Delta: Delta{Content: event.Delta.Text}}},
					}
					select {
					case <-ctx.Done():
						return
					case ch <- chunk:
					}
				}
			case "message_delta":
				if event.Delta.StopReason != "" {
					stopReason = event.Delta.StopReason
				}
				if event.Usage.OutputTokens > 0 {
					outputTokens = event.Usage.OutputTokens
				}
			case "message_stop":
				stopReceived = true
			}
			if stopReceived {
				break
			}
		}

		// 统一发送 finish_reason 帧 + 带 usage 的末帧（供下游 StreamWriter 采集用量）
		finish := mapAnthropicStopToOpenAI(stopReason)
		select {
		case <-ctx.Done():
			return
		case ch <- ChatChunk{
			ID: "chatcmpl-ant", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
			Choices: []ChunkChoice{{Index: 0, FinishReason: &finish}},
		}:
		}
		usage := Usage{PromptTokens: inputTokens, CompletionTokens: outputTokens, TotalTokens: inputTokens + outputTokens}
		select {
		case <-ctx.Done():
			return
		case ch <- ChatChunk{
			ID: "chatcmpl-ant", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
			Choices: []ChunkChoice{}, Usage: &usage,
		}:
		}
	}()

	return ch, nil
}

// mapAnthropicStopToOpenAI 将 Anthropic stop_reason 映射为 OpenAI/IR finish_reason。
func mapAnthropicStopToOpenAI(stop string) string {
	switch stop {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

func (p *AnthropicProvider) Models(ctx context.Context) ([]string, error) {
	// Anthropic 没有标准的 /models 端点，返回已知模型
	return []string{
		"claude-3-5-sonnet-20241022",
		"claude-3-5-haiku-20241022",
		"claude-3-opus-20240229",
		"claude-3-sonnet-20240229",
		"claude-3-haiku-20240307",
	}, nil
}
