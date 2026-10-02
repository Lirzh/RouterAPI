package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/routerapi/backend/internal/errkit"
	"github.com/routerapi/backend/internal/ir"
	"github.com/sirupsen/logrus"
)

// GeminiProvider 适配 Google Gemini API
type GeminiProvider struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewGeminiProvider(name, baseURL, apiKey string) *GeminiProvider {
	return &GeminiProvider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 300 * time.Second},
	}
}

func (p *GeminiProvider) Name() string { return p.name }

// geminiRequest Gemini API 请求格式
type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
	Tools             []geminiToolDef  `json:"tools,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string              `json:"text,omitempty"`
	InlineData       *geminiInlineData   `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResp `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args,omitempty"`
}

type geminiFunctionResp struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response,omitempty"`
}

type geminiToolDef struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations,omitempty"`
}

type geminiFuncDecl struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// geminiResponse Gemini API 响应格式
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string              `json:"text,omitempty"`
				FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
				// Thought 标记该 part 是思维链而非正文；ThoughtSignature 是它的回放签名
				// （多轮工具调用时需要带回去，否则思维链历史会被拒）。
				Thought          bool   `json:"thought,omitempty"`
				ThoughtSignature string `json:"thoughtSignature,omitempty"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata geminiUsage `json:"usageMetadata"`
}

// geminiUsage Gemini 的 usageMetadata。
//
// 与 IR 计费契约的对应关系：cachedContentTokenCount 本身就是 promptTokenCount 的
// 子集（直接符合契约）；thoughtsTokenCount 却不包含在 candidatesTokenCount 里，
// 而 OpenAI 的 completion_tokens 是含推理 token 的，所以这里把 thoughts 并进输出侧。
// Gemini 自己的 totalTokenCount 已经算了 thoughts，故 Total 直接取用即可。
type geminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
}

func (g geminiUsage) toIR() Usage {
	total := g.TotalTokenCount
	if total == 0 {
		total = g.PromptTokenCount + g.CandidatesTokenCount + g.ThoughtsTokenCount
	}
	return Usage{
		PromptTokens:     g.PromptTokenCount,
		CompletionTokens: g.CandidatesTokenCount + g.ThoughtsTokenCount,
		TotalTokens:      total,
		CachedTokens:     g.CachedContentTokenCount,
		ReasoningTokens:  g.ThoughtsTokenCount,
	}
}

func (p *GeminiProvider) convertRequest(req *ChatRequest) *geminiRequest {
	gr := &geminiRequest{}

	// 工具声明
	if len(req.Tools) > 0 {
		decls := make([]geminiFuncDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, geminiFuncDecl{
				Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters,
			})
		}
		gr.Tools = []geminiToolDef{{FunctionDeclarations: decls}}
	}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			if s := msg.TextOf(); s != "" {
				gr.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: s}}}
			}
		case "tool":
			var respMap map[string]interface{}
			if err := json.Unmarshal([]byte(msg.TextOf()), &respMap); err != nil || respMap == nil {
				respMap = map[string]interface{}{"result": msg.TextOf()}
			}
			gr.Contents = append(gr.Contents, geminiContent{
				Role:  "function",
				Parts: []geminiPart{{FunctionResponse: &geminiFunctionResp{Name: msg.Name, Response: respMap}}},
			})
		case "assistant":
			parts := buildGeminiParts(msg)
			for _, tc := range msg.ToolCalls {
				var args map[string]interface{}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: tc.Function.Name, Args: args}})
			}
			if len(parts) == 0 {
				parts = []geminiPart{{Text: ""}}
			}
			gr.Contents = append(gr.Contents, geminiContent{Role: "model", Parts: parts})
		default: // user
			parts := buildGeminiParts(msg)
			if len(parts) == 0 {
				parts = []geminiPart{{Text: ""}}
			}
			gr.Contents = append(gr.Contents, geminiContent{Role: "user", Parts: parts})
		}
	}

	if req.Temperature != nil || req.TopP != nil || req.MaxTokens != nil || req.Stop != nil {
		gc := &geminiGenConfig{
			Temperature:   req.Temperature,
			TopP:          req.TopP,
			StopSequences: req.Stop,
		}
		if req.MaxTokens != nil {
			gc.MaxOutputTokens = req.MaxTokens
		}
		gr.GenerationConfig = gc
	}
	return gr
}

// buildGeminiParts 将 IR 消息内容转为 Gemini parts（text + inlineData 图片）。
func buildGeminiParts(msg Message) []geminiPart {
	parts := make([]geminiPart, 0)
	addImage := func(url string) {
		if id, ok := geminiInlineDataFromURL(url); ok {
			parts = append(parts, geminiPart{InlineData: id})
		}
	}
	switch c := msg.Content.(type) {
	case string:
		if c != "" {
			parts = append(parts, geminiPart{Text: c})
		}
	case []ContentPart:
		for _, p := range c {
			if p.Type == "text" && p.Text != "" {
				parts = append(parts, geminiPart{Text: p.Text})
			} else if p.Type == "image_url" && p.ImageURL != nil {
				addImage(p.ImageURL.URL)
			}
		}
	case []interface{}:
		for _, item := range c {
			pm, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text":
				if s, _ := pm["text"].(string); s != "" {
					parts = append(parts, geminiPart{Text: s})
				}
			case "image_url":
				if iu, ok := pm["image_url"].(map[string]interface{}); ok {
					if u, _ := iu["url"].(string); u != "" {
						addImage(u)
					}
				}
			}
		}
	}
	return parts
}

// geminiInlineDataFromURL 仅支持 data URI（base64）；http(s) 图片需先下载，暂不处理。
func geminiInlineDataFromURL(url string) (*geminiInlineData, bool) {
	if strings.HasPrefix(url, "data:") {
		comma := strings.Index(url, ",")
		semi := strings.Index(url, ";")
		if comma > 0 && semi > 5 && semi < comma {
			return &geminiInlineData{MimeType: url[5:semi], Data: url[comma+1:]}, true
		}
	}
	return nil, false
}

func (p *GeminiProvider) convertResponse(resp *geminiResponse, model string) *ChatResponse {
	var content, reasoning, signature string
	var toolCalls []ToolCall
	finishReason := "stop"
	if len(resp.Candidates) > 0 {
		cand := resp.Candidates[0]
		for _, part := range cand.Content.Parts {
			// thought=true 的 part 是思维链，必须与正文分开：混进 content 会让
			// 客户端无法折叠思考过程，也会把思维链当成答案文本计入兜底 token 估算。
			switch {
			case part.FunctionCall != nil:
				args, _ := json.Marshal(part.FunctionCall.Args)
				toolCalls = append(toolCalls, ToolCall{
					ID:       fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), len(toolCalls)),
					Type:     "function",
					Function: ToolCallFunction{Name: part.FunctionCall.Name, Arguments: string(args)},
				})
			case part.Thought:
				reasoning += part.Text
				if part.ThoughtSignature != "" {
					signature = part.ThoughtSignature
				}
			default:
				content += part.Text
			}
		}
		if cand.FinishReason != "" {
			finishReason = mapGeminiFinishToOpenAI(cand.FinishReason)
		}
	}

	return &ChatResponse{
		ID:      "chatcmpl-gem",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []Choice{
			{Index: 0, Message: Message{Role: "assistant", Content: content, ToolCalls: toolCalls,
				ReasoningContent: reasoning, ReasoningSignature: signature}, FinishReason: finishReason},
		},
		Usage: resp.UsageMetadata.toIR(),
	}
}

func (p *GeminiProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	gr := p.convertRequest(req)
	body, err := ir.MarshalWithExtra(gr, req.ReplayableExtra(ir.ProtocolGemini))
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

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

	var gr2 geminiResponse
	if err := json.Unmarshal(respBody, &gr2); err != nil {
		return nil, err
	}

	return p.convertResponse(&gr2, req.Model), nil
}

func (p *GeminiProvider) ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	gr := p.convertRequest(req)
	body, err := ir.MarshalWithExtra(gr, req.ReplayableExtra(ir.ProtocolGemini))
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

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
		var acc geminiUsage
		finishSeen := ""
		for {
			data, err := decoder.Next()
			if err != nil {
				if err != io.EOF {
					logrus.Debugf("Gemini SSE 结束: %v", err)
				}
				break
			}

			var gr2 geminiResponse
			if err := json.Unmarshal([]byte(data), &gr2); err != nil {
				continue
			}
			// 采集 usageMetadata（可能出现在最后一个候选为空的帧）；取最新的一份完整计数。
			if gr2.UsageMetadata.TotalTokenCount > 0 {
				acc = gr2.UsageMetadata
			}
			if len(gr2.Candidates) == 0 {
				continue
			}

			cand := gr2.Candidates[0]
			var text, reasoning string
			for _, part := range cand.Content.Parts {
				// 与非流式一致地按 thought 标记分流；一律累加进 text 会把思维链
				// 混进正文，客户端既无法折叠、兜底 token 估算也会把思考算成答案。
				if part.Thought {
					reasoning += part.Text
					continue
				}
				text += part.Text
			}
			if text != "" || reasoning != "" {
				chunk := ChatChunk{
					ID: "chatcmpl-gem", Object: "chat.completion.chunk",
					Created: time.Now().Unix(), Model: req.Model,
					Choices: []ChunkChoice{{Index: 0, Delta: Delta{Content: text, ReasoningContent: reasoning}}},
				}
				select {
				case <-ctx.Done():
					return
				case ch <- chunk:
				}
			}
			if cand.FinishReason != "" {
				finishSeen = cand.FinishReason
			}
		}

		// finish_reason 帧
		finish := mapGeminiFinishToOpenAI(finishSeen)
		select {
		case <-ctx.Done():
			return
		case ch <- ChatChunk{
			ID: "chatcmpl-gem", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: req.Model,
			Choices: []ChunkChoice{{Index: 0, FinishReason: &finish}},
		}:
		}
		// usage 末帧
		usage := acc.toIR()
		select {
		case <-ctx.Done():
			return
		case ch <- ChatChunk{
			ID: "chatcmpl-gem", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: req.Model,
			Choices: []ChunkChoice{}, Usage: &usage,
		}:
		}
	}()

	return ch, nil
}

// mapGeminiFinishToOpenAI 将 Gemini finishReason 映射为 OpenAI/IR finish_reason。
func mapGeminiFinishToOpenAI(finish string) string {
	switch strings.ToUpper(finish) {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	default:
		return "stop"
	}
}

func (p *GeminiProvider) Models(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("%s/v1beta/models?key=%s", p.baseURL, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}
	defer resp.Body.Close()

	body, err := readAllCapped(resp.Body)
	if err != nil {
		return nil, errkit.ChannelError(0, "读取响应失败: "+err.Error(), err)
	}
	// 必须检查状态码：401/403 的错误体 {"error":{...}} 也能被正常解码成"models 字段缺失"，
	// 早先不检查时会返回空列表 + nil error，让健康探测把密钥已死的供应商判为健康并清除熔断。
	if resp.StatusCode != http.StatusOK {
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(body))
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errkit.UpstreamError(http.StatusBadGateway, "解析模型列表失败", err)
	}

	models := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		models = append(models, name)
	}
	return models, nil
}
