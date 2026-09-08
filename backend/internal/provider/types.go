package provider

import (
	"context"

	"github.com/routerapi/backend/internal/ir"
)

// 规范类型别名：provider（上游适配器）以 ir 为枢纽类型。
// 保留这些别名可让各上游适配器沿用原有类型名，同时统一到 IR。
type (
	ChatRequest      = ir.ChatRequest
	ChatResponse     = ir.ChatResponse
	ChatChunk        = ir.ChatChunk
	Message          = ir.Message
	ContentPart      = ir.ContentPart
	ImageURL         = ir.ImageURL
	Tool             = ir.Tool
	Function         = ir.Function
	ToolCall         = ir.ToolCall
	ToolCallFunction = ir.ToolCallFunction
	Choice           = ir.Choice
	Usage            = ir.Usage
	ChunkChoice      = ir.ChunkChoice
	Delta            = ir.Delta
	StreamOptions    = ir.StreamOptions
)

// Provider 上游适配器接口：以 IR 为输入输出，屏蔽各上游协议差异。
type Provider interface {
	// Chat 非流式聊天补全
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// ChatStream 流式聊天补全
	ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error)
	// Models 获取可用模型列表
	Models(ctx context.Context) ([]string, error)
	// Name 供应商名称
	Name() string
}
