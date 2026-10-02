// Package ir 定义网关的内部规范表示（Internal Representation）。
// 采用 OpenAI 形态作为枢纽（hub-and-spoke）：入站协议与上游协议各自只需
// 与 IR 互转一次，避免 N×N 的两两转换组合。
//
// 枢纽式转换有个内建风险：IR 是闭合结构体，客户端发来但 IR 没声明的字段会在
// 反序列化时被静默丢弃，等再序列化发往上游时就消失了。这意味着连"OpenAI 入站
// -> OpenAI 兼容上游"这条最普通的路径都是有损的（丢 response_format / seed /
// logprobs / service_tier 等）。
//
// 为此强类型字段之外还保留 Extra：未识别的原始 JSON 片段。加字段的判据只有一条
// ——跨协议需要语义翻译的进强类型字段（如 reasoning/thinking），否则进 Extra。
// Extra 是有词汇表的（openai 的 response_format 塞进 Anthropic 只会得到 400），
// 所以每个 IR 值都带 Inbound 标记，只有在"上游协议 == 入站协议"时才允许回填。
package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

// ChatRequest 规范聊天请求（OpenAI 兼容超集）。
type ChatRequest struct {
	Model            string         `json:"model"`
	Messages         []Message      `json:"messages"`
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"top_p,omitempty"`
	MaxTokens        *int           `json:"max_tokens,omitempty"`
	Stream           bool           `json:"stream,omitempty"`
	StreamOptions    *StreamOptions `json:"stream_options,omitempty"`
	Tools            []Tool         `json:"tools,omitempty"`
	ToolChoice       interface{}    `json:"tool_choice,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	Stop             []string       `json:"stop,omitempty"`
	N                *int           `json:"n,omitempty"`
	System           string         `json:"-"` // 由 Anthropic/Gemini 顶层 system 提取，OpenAI 走 messages

	// Extra 保留 IR 未声明的入站字段原文，供同词汇表的上游回填。
	Extra map[string]json.RawMessage `json:"-"`
	// Inbound 标记 Extra 来自哪种入站词汇（openai|anthropic|gemini）。空值按 OpenAI 处理。
	Inbound string `json:"-"`
}

// ReplayableExtra 返回可以安全回填到 protocol 上游的未识别字段。
//
// 词汇表不匹配时必须返回 nil：把 OpenAI 的 response_format 塞进 Anthropic 请求体
// 不会"多支持一个参数"，只会让上游直接 400，把一次本可以成功的调用变成失败。
func (r *ChatRequest) ReplayableExtra(protocol string) map[string]json.RawMessage {
	if len(r.Extra) == 0 {
		return nil
	}
	inbound := r.Inbound
	if inbound == "" {
		inbound = ProtocolOpenAI
	}
	if inbound != protocol {
		return nil
	}
	return r.Extra
}

// 入站/上游协议标识。ir 不依赖 model/inbound 包，避免类型成环。
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
	ProtocolGemini    = "gemini"
)

// MarshalWithExtra 序列化 v 并回填 extra 中未识别的字段。
// 无 extra 时走原生序列化，让绝大多数请求不为此付出额外开销。
func MarshalWithExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return json.Marshal(v)
	}
	return mergeExtra(v, extra)
}

// mergeExtra 把 extra 并进 v 的 JSON 对象。已声明字段优先，extra 不得覆盖：
// 强类型字段可能被绑定默认参数等逻辑改写（applyBindingParams），覆盖会丢掉这些决策。
func mergeExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	base, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	for k, raw := range extra {
		if _, exists := obj[k]; exists {
			continue
		}
		obj[k] = raw
	}
	return json.Marshal(obj)
}

// unmarshalKeepingExtra 反序列化到 dst（调用方须传入不带方法的别名类型以规避递归），
// 同时把 dst 未声明的顶层字段收进 extra。
func unmarshalKeepingExtra(data []byte, known map[string]bool, dst any) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}
	return CollectUnknown(data, known)
}

// jsonKeys 反射出结构体的 JSON 字段名集合。
// 从结构体标签派生而不是手写清单：手写的清单在有人加字段时必然忘记同步，
// 而漏掉的那个字段会被误当成"未识别字段"塞进 Extra，再被原样回填，变成重复键。
func jsonKeys(t reflect.Type) map[string]bool {
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // 非导出字段不参与 JSON
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name // 无标签字段按 Go 字段名序列化
		}
		keys[name] = true
	}
	return keys
}

// keySetCache 按类型缓存反射结果。入站解析在请求热路径上，每种 DTO 只反射一次。
var keySetCache sync.Map // reflect.Type -> map[string]bool

// JSONKeys 返回 v 对应结构体的 JSON 字段名集合（带缓存）。
//
// 供入站解析器使用：Anthropic / Gemini 的入站 DTO 有自己的词汇表，
// 判断"哪些字段没被识别"必须拿它自己的键集合，而不是 IR 的。
func JSONKeys(v any) map[string]bool {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	if cached, ok := keySetCache.Load(t); ok {
		return cached.(map[string]bool)
	}
	keys := jsonKeys(t)
	keySetCache.Store(t, keys)
	return keys
}

// CollectUnknown 返回 data 这个 JSON 对象里、不属于 known 的顶层字段原文。
// 用于把入站协议自有词汇表的未识别字段带进 IR.Extra。
func CollectUnknown(data []byte, known map[string]bool) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	var extra map[string]json.RawMessage
	for k, raw := range obj {
		if known[k] {
			continue
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage, len(obj))
		}
		extra[k] = raw
	}
	return extra, nil
}

var (
	chatRequestKeys  = jsonKeys(reflect.TypeOf(ChatRequest{}))
	chatResponseKeys = jsonKeys(reflect.TypeOf(ChatResponse{}))
)

// UnmarshalJSON 在解析强类型字段的同时保留未识别字段。
//
// 故意不提供对应的 MarshalJSON：那会让回填无条件发生，绕过 ReplayableExtra 的
// 词汇表检查。请求侧的回填必须由上游适配器显式经 MarshalWithExtra 走。
func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	type alias ChatRequest // 别名不继承方法，避免无限递归
	var a alias
	extra, err := unmarshalKeepingExtra(data, chatRequestKeys, &a)
	if err != nil {
		return err
	}
	*r = ChatRequest(a)
	r.Extra = extra
	return nil
}

// StreamOptions 流式选项（OpenAI: {"include_usage": true} 让末帧返回用量）。
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Message 聊天消息。Content 可为 string 或 []ContentPart（多模态）。
type Message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`

	// ReasoningContent 思维链正文。各家叫法不同（OpenAI 生态的 reasoning_content、
	// Anthropic 的 thinking block、Gemini 的 part.thought=true），在 IR 里统一成一个字段。
	// 与 Content 分开是必须的：混进 Content 会让客户端没法折叠，也会被算进正文计费口径。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ReasoningSignature 仅 Anthropic 有：思维块的回放签名。
	// 多轮工具调用时必须原样回传，缺它 Anthropic 会直接拒掉带 thinking 的历史消息。
	ReasoningSignature string `json:"-"`
}

// CacheControl 内容块级缓存断点（Anthropic prompt caching；部分兼容网关沿用同一形态）。
// Type 目前只有 "ephemeral"；TTL 为空表示用上游默认（5 分钟）。
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// ContentPart 多模态内容片段。
type ContentPart struct {
	Type     string    `json:"type"` // text | image_url | image
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`

	// CacheControl 是该片段的缓存断点。它必须成为 IR 的一等字段而不是留在 Extra 里：
	// 它在 content 数组内部，顶层 Extra 覆盖不到嵌套结构；而丢掉它等于丢掉整个
	// 提示缓存——客户端标了断点却只有 cached_tokens 一直为 0、按全价计费。
	CacheControl *CacheControl `json:"cache_control,omitempty"`
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

	// Extra 保留上游返回但 IR 未声明的响应字段（system_fingerprint、annotations 等）。
	//
	// 与请求侧不同，这里可以安全地用 MarshalJSON 回填：ChatResponse 整体只在
	// "客户端也是 OpenAI 入站"时被原样序列化（inbound.RenderResponse 的默认分支），
	// 转成 Anthropic/Gemini 时走的是各自 DTO，这些键根本不会被带过去。
	// 流式块 ChatChunk 则会被三种协议的写出器共同消费，故不保留 Extra。
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON 保留上游响应中 IR 未声明的字段。
func (r *ChatResponse) UnmarshalJSON(data []byte) error {
	type alias ChatResponse
	var a alias
	extra, err := unmarshalKeepingExtra(data, chatResponseKeys, &a)
	if err != nil {
		return err
	}
	*r = ChatResponse(a)
	r.Extra = extra
	return nil
}

// MarshalJSON 把未声明字段回填进响应体，交给客户端。
func (r ChatResponse) MarshalJSON() ([]byte, error) {
	type alias ChatResponse
	if len(r.Extra) == 0 {
		return json.Marshal(alias(r))
	}
	return mergeExtra(alias(r), r.Extra)
}

// Choice 响应选项。
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage token 用量。
//
// 计费契约（各上游适配器负责把自家长成这样的值，billing 只读这套字段）：
//   - PromptTokens     = 输入 token 总和，已包含下面两个子集
//   - CachedTokens     ⊆ PromptTokens，命中缓存读取的部分（单价更低）
//   - CacheWriteTokens ⊆ PromptTokens，写入缓存的部分（单价更高）
//   - CompletionTokens = 输出 token 总和，已包含 ReasoningTokens
//   - ReasoningTokens  ⊆ CompletionTokens，仅用于展示与更细定价，不参与求和
//
// 之所以统一成"总和包含子集"，是因为 OpenAI 本身就是这个语义；Anthropic 的
// cache_read/cache_creation 与 input_tokens 是互不包含的三段，Gemini 的
// thoughtsTokenCount 也不在 candidatesTokenCount 里——这三处差异必须在适配器
// 侧抹平，不能让 billing 去猜上游是谁。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	CachedTokens     int `json:"-"`
	CacheWriteTokens int `json:"-"`
	ReasoningTokens  int `json:"-"`

	// OpenAI 形态的用量明细。入站直接反序列化填充，回给 OpenAI 客户端时原样保留。
	PromptDetails     *PromptUsageDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionDetails *CompletionUsageDetails `json:"completion_tokens_details,omitempty"`
}

// PromptUsageDetails OpenAI 的 prompt_tokens_details。
type PromptUsageDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	AudioTokens         int `json:"audio_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
}

// CompletionUsageDetails OpenAI 的 completion_tokens_details。
type CompletionUsageDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
}

// AdoptOpenAIDetails 把 OpenAI 形态的明细字段映射到归一化计费字段。
// OpenAI 系的 prompt_tokens 本身已包含 cached_tokens，故此处只做映射、不做加减。
func (u *Usage) AdoptOpenAIDetails() {
	if u.PromptDetails != nil {
		if u.CachedTokens == 0 {
			u.CachedTokens = u.PromptDetails.CachedTokens
		}
		if u.CacheWriteTokens == 0 {
			u.CacheWriteTokens = u.PromptDetails.CacheCreationTokens
		}
	}
	if u.CompletionDetails != nil && u.ReasoningTokens == 0 {
		u.ReasoningTokens = u.CompletionDetails.ReasoningTokens
	}
}

// UncachedPromptTokens 需要按全价计费的输入部分；下限钳到 0，
// 以防上游返回 cached_tokens > prompt_tokens 的畸形用量把计费算成负数。
func (u Usage) UncachedPromptTokens() int {
	n := u.PromptTokens - u.CachedTokens - u.CacheWriteTokens
	if n < 0 {
		return 0
	}
	return n
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

	// ReasoningContent 思维链增量。DeepSeek-R1 / QwQ / Kimi 等经 OpenAI 兼容接口
	// 都走这个字段；不声明就会被流式解析静默丢掉，客户端表现为"只有答案没有过程"。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ReasoningSignature Anthropic 的 signature_delta，通常在思维块收尾时给出。
	ReasoningSignature string `json:"-"`
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
