package inbound

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// shortID 生成 12 位十六进制短 ID（用于响应/消息 ID）。
func shortID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowUnix() int64 { return time.Now().Unix() }

// mapFinishToAnthropic 将 IR/OpenAI 的 finish_reason 映射为 Anthropic stop_reason。
func mapFinishToAnthropic(finish string) string {
	switch finish {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "stop_sequence"
	case "":
		return "end_turn"
	default:
		return finish
	}
}

// mapFinishToGemini 将 IR/OpenAI 的 finish_reason 映射为 Gemini finishReason。
func mapFinishToGemini(finish string) string {
	switch finish {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	case "tool_calls", "function_call":
		return "STOP"
	case "":
		return "STOP"
	default:
		return "STOP"
	}
}
