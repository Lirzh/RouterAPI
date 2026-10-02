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
	"github.com/routerapi/backend/internal/ir"
	"github.com/sirupsen/logrus"
)

// anthropicVersion 上游 API 版本头。Anthropic 要求每个请求都带，缺失会直接 400。
const anthropicVersion = "2023-06-01"

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

// anthropicContentBlock 内容块：text / image / tool_use / tool_result / thinking
type anthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	Source    *anthropicImageSource `json:"source,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   interface{}           `json:"content,omitempty"`

	// thinking 块的正文与回放签名。签名必须原样回传，否则带 thinking 的历史消息会被拒。
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// 缓存断点。丢它等于丢掉整轮提示缓存：上游不会产生 cache_read_input_tokens。
	CacheControl *ir.CacheControl `json:"cache_control,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicUsage Anthropic 的用量结构。
//
// Anthropic 把 input_tokens / cache_read_input_tokens / cache_creation_input_tokens
// 作为三段互不包含的计数返回，而 IR 契约要求 PromptTokens 是包含两个缓存子集的总和。
// 差异在此处一次抹平，不在 billing 侧判断上游协议。
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// toIR 换算成 IR 归一化用量。
//
// cache_creation 在 Anthropic 侧还分 5 分钟与 1 小时两档单价（后者更贵），
// 这里先合并到一个 CacheWriteTokens：合并只是丢掉一档更细的定价能力，
// 不会算错总量；要按两档分别计价时再扩 anthropicUsage 的子字段。
func (a anthropicUsage) toIR() Usage {
	return Usage{
		PromptTokens:     a.InputTokens + a.CacheReadInputTokens + a.CacheCreationInputTokens,
		CompletionTokens: a.OutputTokens,
		TotalTokens:      a.InputTokens + a.CacheReadInputTokens + a.CacheCreationInputTokens + a.OutputTokens,
		CachedTokens:     a.CacheReadInputTokens,
		CacheWriteTokens: a.CacheCreationInputTokens,
	}
}

// mergeMax 用 o 中更大的字段值合并进来。
//
// Anthropic 流式的用量分散在两个事件里且语义是累计计数：message_start 带
// input/cache 三项（output 只是初值），message_delta 带累计 output（部分模型
// 会重复带上 input/cache）。逐项取最大值对"累计"和"只出现在其一"两种情况都成立，
// 且不会像直接覆盖那样把 message_start 的 input_tokens 清零。
func (a *anthropicUsage) mergeMax(o anthropicUsage) {
	if o.InputTokens > a.InputTokens {
		a.InputTokens = o.InputTokens
	}
	if o.OutputTokens > a.OutputTokens {
		a.OutputTokens = o.OutputTokens
	}
	if o.CacheReadInputTokens > a.CacheReadInputTokens {
		a.CacheReadInputTokens = o.CacheReadInputTokens
	}
	if o.CacheCreationInputTokens > a.CacheCreationInputTokens {
		a.CacheCreationInputTokens = o.CacheCreationInputTokens
	}
}

// anthropicResponse Anthropic API 响应格式
type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
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
			// OpenAI 形态的 content 里也可能带 cache_control（LiteLLM 一类的
			// 多供应商客户端会直接写在这里），所以这条路径同样要透传。
			var cc *ir.CacheControl
			if raw, ok := pm["cache_control"].(map[string]interface{}); ok {
				typ, _ := raw["type"].(string)
				ttl, _ := raw["ttl"].(string)
				if typ != "" {
					cc = &ir.CacheControl{Type: typ, TTL: ttl}
				}
			}
			switch pm["type"] {
			case "text":
				if s, _ := pm["text"].(string); s != "" {
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: s, CacheControl: cc})
				}
			case "image_url":
				if iu, ok := pm["image_url"].(map[string]interface{}); ok {
					if u, _ := iu["url"].(string); u != "" {
						if blk, ok := imageBlockFromDataURL(u); ok {
							blk.CacheControl = cc
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
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: p.Text, CacheControl: p.CacheControl})
			} else if p.Type == "image_url" && p.ImageURL != nil {
				if blk, ok := imageBlockFromDataURL(p.ImageURL.URL); ok {
					blk.CacheControl = p.CacheControl
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
	var text, reasoning, signature string
	var toolCalls []ToolCall
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			text += c.Text
		case "thinking":
			reasoning += c.Thinking
			if c.Signature != "" {
				signature = c.Signature
			}
		case "redacted_thinking":
			// 被安全策略编辑掉的思维块：正文是不可读的 base64，无法还原。
			// 放一条占位进 reasoning_content 而不是直接丢弃，是为了让"这一轮有过思维块"
			// 对客户端可见；没有占位的话它与"模型压根没思考"看起来完全一样。
			reasoning += "[redacted thinking]"
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID: c.ID, Type: "function",
				Function: ToolCallFunction{Name: c.Name, Arguments: string(c.Input)},
			})
		}
	}

	msg := Message{Role: "assistant", Content: text, ToolCalls: toolCalls,
		ReasoningContent: reasoning, ReasoningSignature: signature}
	return &ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []Choice{
			{Index: 0, Message: msg, FinishReason: mapAnthropicStopToOpenAI(resp.StopReason)},
		},
		Usage: resp.Usage.toIR(),
	}
}

func (p *AnthropicProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	ar := p.convertRequest(req)
	body, err := ir.MarshalWithExtra(ar, req.ReplayableExtra(ir.ProtocolAnthropic))
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
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}
	defer resp.Body.Close()

	respBody, err := readAllCapped(resp.Body)
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
	body, err := ir.MarshalWithExtra(ar, req.ReplayableExtra(ir.ProtocolAnthropic))
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
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBodyBytes))
		resp.Body.Close()
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}

	ch := make(chan ChatChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		decoder := newSSEDecoder(resp.Body)
		var model string
		var acc anthropicUsage
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
					Type       string `json:"type"` // text_delta | thinking_delta | signature_delta | input_json_delta
					Text       string `json:"text"`
					Thinking   string `json:"thinking"`
					Signature  string `json:"signature"`
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Message struct {
					Model string         `json:"model"`
					Usage anthropicUsage `json:"usage"`
				} `json:"message"`
				Usage anthropicUsage `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "message_start":
				if event.Message.Model != "" {
					model = event.Message.Model
				}
				acc.mergeMax(event.Message.Usage)
			case "content_block_delta":
				// 按 delta.type 分派。原先只看 delta.text，thinking_delta 与
				// signature_delta 的正文因为在 text 字段上为空而被整块丢掉。
				d := Delta{}
				switch event.Delta.Type {
				case "thinking_delta":
					d.ReasoningContent = event.Delta.Thinking
				case "signature_delta":
					d.ReasoningSignature = event.Delta.Signature
				case "text_delta":
					d.Content = event.Delta.Text
				default:
					// input_json_delta 等工具流式参数尚未支持；未知类型按文本兜底会
					// 把思维链或 JSON 片段混进正文，所以选择跳过。
					continue
				}
				if d.Content == "" && d.ReasoningContent == "" && d.ReasoningSignature == "" {
					continue
				}
				chunk := ChatChunk{
					ID: "chatcmpl-ant", Object: "chat.completion.chunk",
					Created: time.Now().Unix(), Model: model,
					Choices: []ChunkChoice{{Index: 0, Delta: d}},
				}
				select {
				case <-ctx.Done():
					return
				case ch <- chunk:
				}
			case "message_delta":
				if event.Delta.StopReason != "" {
					stopReason = event.Delta.StopReason
				}
				acc.mergeMax(event.Usage)
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
		usage := acc.toIR()
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
	// 真实调用 GET /v1/models。
	//
	// 这里原来返回一个硬编码的 5 个老模型名、且永不报错，导致两个后果：
	// 供应商"测试连通性"对 Anthropic 恒为成功；密钥已死的供应商被健康探测判定为
	// 健康并清除熔断，于是每次真实流量再把它熔断、探测再"复活"，反复抖动。
	url := p.baseURL + "/v1/models"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}
	defer resp.Body.Close()

	body, err := readAllCapped(resp.Body)
	if err != nil {
		return nil, errkit.ChannelError(0, "读取响应失败: "+err.Error(), err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errkit.UpstreamError(http.StatusBadGateway, "解析模型列表失败", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}
