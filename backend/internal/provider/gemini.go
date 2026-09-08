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
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
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
	var content string
	var toolCalls []ToolCall
	finishReason := "stop"
	if len(resp.Candidates) > 0 {
		cand := resp.Candidates[0]
		for _, part := range cand.Content.Parts {
			content += part.Text
			if part.FunctionCall != nil {
				args, _ := json.Marshal(part.FunctionCall.Args)
				toolCalls = append(toolCalls, ToolCall{
					ID:   fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), len(toolCalls)),
					Type: "function",
					Function: ToolCallFunction{Name: part.FunctionCall.Name, Arguments: string(args)},
				})
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
			{Index: 0, Message: Message{Role: "assistant", Content: content, ToolCalls: toolCalls}, FinishReason: finishReason},
		},
		Usage: Usage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		},
	}
}

func (p *GeminiProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	gr := p.convertRequest(req)
	body, err := json.Marshal(gr)
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

	respBody, err := io.ReadAll(resp.Body)
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
	body, err := json.Marshal(gr)
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
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}

	ch := make(chan ChatChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		decoder := newSSEDecoder(resp.Body)
		var promptT, compT, totalT int
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
			// 采集 usageMetadata（可能出现在最后一个候选为空的帧）
			if gr2.UsageMetadata.TotalTokenCount > 0 {
				promptT = gr2.UsageMetadata.PromptTokenCount
				compT = gr2.UsageMetadata.CandidatesTokenCount
				totalT = gr2.UsageMetadata.TotalTokenCount
			}
			if len(gr2.Candidates) == 0 {
				continue
			}

			cand := gr2.Candidates[0]
			var text string
			for _, part := range cand.Content.Parts {
				text += part.Text
			}
			if text != "" {
				chunk := ChatChunk{
					ID: "chatcmpl-gem", Object: "chat.completion.chunk",
					Created: time.Now().Unix(), Model: req.Model,
					Choices: []ChunkChoice{{Index: 0, Delta: Delta{Content: text}}},
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
		usage := Usage{PromptTokens: promptT, CompletionTokens: compT, TotalTokens: totalT}
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
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		models = append(models, name)
	}
	return models, nil
}
