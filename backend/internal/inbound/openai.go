package inbound

import (
	"encoding/json"

	"github.com/routerapi/backend/internal/ir"
)

// parseOpenAI OpenAI 入站：请求体本身即 IR 形态，直接反序列化。
func parseOpenAI(body []byte) (*ir.ChatRequest, error) {
	var req ir.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// writeOpenAIChunk OpenAI 流式：原样输出 chat.completion.chunk。
func (sw *StreamWriter) writeOpenAIChunk(chunk ir.ChatChunk) {
	chunk.Model = sw.model
	if chunk.Object == "" {
		chunk.Object = "chat.completion.chunk"
	}
	if chunk.ID == "" {
		chunk.ID = "chatcmpl-" + shortID()
	}
	if chunk.Created == 0 {
		chunk.Created = nowUnix()
	}
	sw.writeData(chunk)
}

// finishOpenAI OpenAI 流式结束：若上游未给 usage，补一个仅含 usage 的末帧，再发 [DONE]。
func (sw *StreamWriter) finishOpenAI() {
	if !sw.oaiGotUsage {
		u := sw.Usage()
		sw.writeData(ir.ChatChunk{
			ID:      "chatcmpl-" + shortID(),
			Object:  "chat.completion.chunk",
			Created: nowUnix(),
			Model:   sw.model,
			Choices: []ir.ChunkChoice{},
			Usage:   &u,
		})
	}
	sw.writeRaw("data: [DONE]\n\n")
}
