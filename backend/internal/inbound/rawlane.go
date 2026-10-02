package inbound

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/routerapi/backend/internal/ir"
)

// 同格式直通的出站侧：把上游 SSE 原文逐帧转给客户端。
//
// 与 StreamWriter 的分工是"谁改动字节"：这里只改两处——
//  1. model 字段换回客户端调用的虚拟名；
//  2. 上游没给 usage 时补一个 usage 末帧（与 finishOpenAI 行为对齐）。
//
// 其余一律原样透传，包括帧顺序、注释行和 IR 没声明的响应字段。

// dataLinePrefix 是 SSE 数据行的前缀（规范允许其后跟一个可选空格）。
var dataLinePrefix = []byte("data:")

// RewriteModelField 把一段 JSON 对象原文里的 model 字段换成 virtualModel。
//
// 上游响应与流式块带的是真实上游模型名，原样回给客户端等于把"这个 Key 实际在用
// 哪家哪个型号"这件事公开出去——转售平台上这是商业信息，不是可选优化。
//
// 解析失败时返回原文：一条已经成功的请求不该因为改不动一个字段而变成 502。
// 走到这里说明响应已经被 provider 当作合法 JSON 对象解析过一次，正常不会失败。
func RewriteModelField(body []byte, virtualModel string) []byte {
	obj, err := ir.ParseJSONObject(body)
	if err != nil {
		return body
	}
	if cur, ok := obj["model"]; ok && string(cur) == string(ir.JSONString(virtualModel)) {
		return body // 已经是虚拟名，一个字节都不动
	}
	obj["model"] = ir.JSONString(virtualModel)
	out, err := ir.MarshalJSONObject(obj)
	if err != nil {
		return body
	}
	return out
}

// RawStreamWriter 以 OpenAI 系形态逐帧透传上游流式响应，并窥探计费用量。
//
// 只服务 OpenAI 族：Anthropic/Gemini 的帧带 event: 行、用量分散在 message_start 与
// message_delta 两处，直通时要各自处理，不能靠这个实现"顺手支持"。
type RawStreamWriter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	model    string // 虚拟模型名
	rewrites bool   // 上游名与虚拟名不同才需要改写帧

	usage    ir.Usage
	text     strings.Builder
	gotUsage bool
	// 终止帧先扣下：上游没给 usage 时 Finish 还要在 [DONE] 之前插一帧，
	// 而 [DONE] 之后的内容客户端根本读不到。
	deferredDone []byte
}

// NewRawStreamWriter 创建直通写出器。upstreamModel 为路由改写后的真实上游模型名。
func NewRawStreamWriter(w http.ResponseWriter, flusher http.Flusher, virtualModel, upstreamModel string) *RawStreamWriter {
	return &RawStreamWriter{
		w:        w,
		flusher:  flusher,
		model:    virtualModel,
		rewrites: upstreamModel != "" && upstreamModel != virtualModel,
	}
}

// Forward 写出一帧。raw 为上游帧原文，data 为其 data 载荷（无则 nil）。
func (sw *RawStreamWriter) Forward(raw, data []byte) {
	if len(data) > 0 {
		if isDonePayload(data) {
			sw.deferredDone = raw
			return
		}
		sw.peek(data)
	}
	out := raw
	if sw.rewrites && len(data) > 0 {
		// 只在载荷确实是一行 data 时改写，并且是就地替换那段字节：
		// 帧的其余部分（event 名、注释行、CRLF 行尾）连字节一起保留。
		// OpenAI 系的每个 chunk 就是单行 JSON；多行 data 帧不在此路径的形态里，
		// 遇到就整帧原样放行，不拿"猜一种封装方式"去换一次改写。
		if payload, start, end, ok := dataPayloadSpan(raw); ok {
			if rewritten := RewriteModelField(payload, sw.model); !bytes.Equal(rewritten, payload) {
				out = make([]byte, 0, len(raw)-(end-start)+len(rewritten))
				out = append(out, raw[:start]...)
				out = append(out, rewritten...)
				out = append(out, raw[end:]...)
			}
		}
	}
	sw.write(out)
}

// Usage 返回窥探到的用量；上游没给 usage 时用累计文本做兜底估算。
// 兜底是必需的：兼容网关里确实有忽略 stream_options 的，没有它就等于这一单免费。
func (sw *RawStreamWriter) Usage() ir.Usage {
	u := sw.usage
	if u.CompletionTokens == 0 && sw.text.Len() > 0 {
		u.CompletionTokens = EstimateTokens(sw.text.String())
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

// Finish 补齐 usage 末帧与终止帧，让客户端看到的形态与 IR 路径一致。
func (sw *RawStreamWriter) Finish() {
	if !sw.gotUsage {
		u := sw.Usage()
		b, _ := json.Marshal(ir.ChatChunk{
			ID:      "chatcmpl-" + shortID(),
			Object:  "chat.completion.chunk",
			Created: nowUnix(),
			Model:   sw.model,
			Choices: []ir.ChunkChoice{},
			Usage:   &u,
		})
		sw.write(append(append([]byte("data: "), b...), '\n', '\n'))
	}
	done := sw.deferredDone
	if done == nil {
		// 上游没发终止帧就直接关了连接：必须补一个，否则客户端会一直等。
		done = []byte("data: [DONE]\n\n")
	}
	sw.write(done)
}

func (sw *RawStreamWriter) write(b []byte) {
	_, _ = sw.w.Write(b)
	sw.flusher.Flush()
}

// peek 只读计费需要的两样东西：末帧用量与增量正文（后者是 usage 缺失时的估算依据）。
// 解析失败静默跳过——窥探是附加动作，不能因为它让转发失败。
func (sw *RawStreamWriter) peek(data []byte) {
	var parsed struct {
		Usage   *ir.Usage `json:"usage"`
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}
	// 全零的 usage 帧按"没给"处理：有些网关每帧都带一个空 usage。
	if u := parsed.Usage; u != nil && (u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0) {
		u.AdoptOpenAIDetails()
		sw.usage = *u
		sw.gotUsage = true
	}
	for _, ch := range parsed.Choices {
		if ch.Delta.Content != "" {
			sw.text.WriteString(ch.Delta.Content)
		}
	}
}

// isDonePayload 判断 data 载荷是否为流终止标记。
func isDonePayload(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]"))
}

// dataPayloadSpan 返回这一帧唯一那行 data 的载荷，以及它在 raw 中的字节区间
// [start, end)。不是"恰好一行 data"时返回 ok=false。
//
// 返回区间而不是只有载荷，是为了让改写能就地替换那几个字节——重建整帧会顺手改掉
// 行尾风格（CRLF -> LF）并丢掉 event:/id:/注释行，那就不是直通了。
func dataPayloadSpan(raw []byte) (payload []byte, start, end int, ok bool) {
	count := 0
	offset := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		trimmed := line
		if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\r' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if bytes.HasPrefix(trimmed, dataLinePrefix) {
			count++
			start = offset + len(dataLinePrefix)
			if start < len(raw) && raw[start] == ' ' {
				start++ // 规范允许 "data:" 后跟一个可选空格，只裁这一个
			}
			end = offset + len(trimmed)
			payload = raw[start:end]
		}
		offset += len(line) + 1 // 补回 Split 掉的那个 \n
	}
	if count != 1 {
		return nil, 0, 0, false
	}
	return payload, start, end, true
}
