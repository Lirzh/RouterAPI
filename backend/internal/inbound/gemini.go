package inbound

import (
	"encoding/json"

	"github.com/routerapi/backend/internal/ir"
)

// ============ Gemini 入站请求结构 ============

type geminiRequest struct {
	Contents          []geminiContent   `json:"contents"`
	SystemInstruction *geminiContent    `json:"systemInstruction"`
	GenerationConfig  *geminiGenConfig  `json:"generationConfig"`
	Tools             []geminiTool      `json:"tools"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *geminiInlineData `json:"inlineData,omitempty"`
	FunctionCall     *geminiFuncCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResp   `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFuncCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type geminiFuncResp struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature"`
	TopP            *float64 `json:"topP"`
	MaxOutputTokens *int     `json:"maxOutputTokens"`
	StopSequences   []string `json:"stopSequences"`
	CandidateCount  *int     `json:"candidateCount"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiFuncDecl struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// parseGemini 将 Gemini generateContent 请求转换为 IR；模型名来自 URL 路径。
func parseGemini(body []byte, pathModel string) (*ir.ChatRequest, error) {
	var gr geminiRequest
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, err
	}
	req := &ir.ChatRequest{Model: pathModel}
	if gc := gr.GenerationConfig; gc != nil {
		req.Temperature = gc.Temperature
		req.TopP = gc.TopP
		req.MaxTokens = gc.MaxOutputTokens
		req.Stop = gc.StopSequences
		req.N = gc.CandidateCount
	}
	if gr.SystemInstruction != nil {
		if txt := geminiPartsText(gr.SystemInstruction.Parts); txt != "" {
			req.Messages = append(req.Messages, ir.Message{Role: "system", Content: txt})
			req.System = txt
		}
	}
	for _, c := range gr.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		msg := ir.Message{Role: role}
		var parts []ir.ContentPart
		text := ""
		hasNonText := false
		for _, p := range c.Parts {
			switch {
			case p.Text != "":
				parts = append(parts, ir.ContentPart{Type: "text", Text: p.Text})
				text += p.Text
			case p.InlineData != nil:
				hasNonText = true
				url := "data:" + p.InlineData.MimeType + ";base64," + p.InlineData.Data
				parts = append(parts, ir.ContentPart{Type: "image_url", ImageURL: &ir.ImageURL{URL: url}})
			case p.FunctionCall != nil:
				hasNonText = true
				args, _ := json.Marshal(p.FunctionCall.Args)
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
					ID: "call_" + shortID(), Type: "function",
					Function: ir.ToolCallFunction{Name: p.FunctionCall.Name, Arguments: string(args)},
				})
			case p.FunctionResponse != nil:
				hasNonText = true
				respJSON, _ := json.Marshal(p.FunctionResponse.Response)
				req.Messages = append(req.Messages, ir.Message{
					Role: "tool", Name: p.FunctionResponse.Name, Content: string(respJSON),
				})
			}
		}
		if !hasNonText {
			msg.Content = text
		} else if len(parts) > 0 {
			msg.Content = parts
		} else {
			msg.Content = text
		}
		if msg.Content != nil || len(msg.ToolCalls) > 0 {
			req.Messages = append(req.Messages, msg)
		}
	}
	for _, t := range gr.Tools {
		for _, fd := range t.FunctionDeclarations {
			req.Tools = append(req.Tools, ir.Tool{
				Type: "function",
				Function: ir.Function{Name: fd.Name, Description: fd.Description, Parameters: fd.Parameters},
			})
		}
	}
	return req, nil
}

func geminiPartsText(parts []geminiPart) string {
	out := ""
	for _, p := range parts {
		out += p.Text
	}
	return out
}

// ============ Gemini 出站响应 ============

func renderGeminiResponse(resp *ir.ChatResponse, virtualModel string) interface{} {
	candidates := make([]map[string]interface{}, 0)
	for _, choice := range resp.Choices {
		parts := make([]map[string]interface{}, 0)
		if txt := choice.Message.TextOf(); txt != "" {
			parts = append(parts, map[string]interface{}{"text": txt})
		}
		for _, tc := range choice.Message.ToolCalls {
			var args interface{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			parts = append(parts, map[string]interface{}{
				"functionCall": map[string]interface{}{"name": tc.Function.Name, "args": args},
			})
		}
		if len(parts) == 0 {
			parts = append(parts, map[string]interface{}{"text": ""})
		}
		candidates = append(candidates, map[string]interface{}{
			"content":      map[string]interface{}{"parts": parts, "role": "model"},
			"finishReason": mapFinishToGemini(choice.FinishReason),
			"index":        choice.Index,
		})
	}
	return map[string]interface{}{
		"candidates": candidates,
		"usageMetadata": map[string]interface{}{
			"promptTokenCount":     resp.Usage.PromptTokens,
			"candidatesTokenCount": resp.Usage.CompletionTokens,
			"totalTokenCount":      resp.Usage.TotalTokens,
		},
		"modelVersion": virtualModel,
	}
}

// ============ Gemini 流式 ============

func (sw *StreamWriter) writeGeminiChunk(chunk ir.ChatChunk) {
	for _, ch := range chunk.Choices {
		if ch.Delta.Content == "" {
			continue
		}
		sw.writeData(map[string]interface{}{
			"candidates": []map[string]interface{}{
				{
					"content": map[string]interface{}{
						"parts": []map[string]interface{}{{"text": ch.Delta.Content}},
						"role":  "model",
					},
					"index": 0,
				},
			},
		})
	}
}

func (sw *StreamWriter) finishGemini() {
	if sw.gemFinishSent {
		return
	}
	u := sw.Usage()
	finish := sw.gemFinish
	if finish == "" {
		finish = "STOP"
	}
	// 末帧携带 finishReason + usageMetadata
	sw.writeData(map[string]interface{}{
		"candidates": []map[string]interface{}{
			{"content": map[string]interface{}{"parts": []map[string]interface{}{}, "role": "model"}, "finishReason": finish, "index": 0},
		},
		"usageMetadata": map[string]interface{}{
			"promptTokenCount":     u.PromptTokens,
			"candidatesTokenCount": u.CompletionTokens,
			"totalTokenCount":      u.TotalTokens,
		},
	})
	sw.gemFinishSent = true
}
