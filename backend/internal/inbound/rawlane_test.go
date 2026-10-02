package inbound

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// chunkFrame 按 OpenAI 系形态拼一帧 SSE。
func chunkFrame(t *testing.T, payload interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("构造帧失败: %v", err)
	}
	return []byte("data: " + string(b) + "\n\n")
}

func chunkPayload(t *testing.T, frame []byte) map[string]interface{} {
	t.Helper()
	payload, _, _, ok := dataPayloadSpan(frame)
	if !ok {
		t.Fatalf("帧形态不是单行 data: %q", frame)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("解析帧载荷失败: %v\n%s", err, payload)
	}
	return m
}

func deltaFrame(id, model, content string) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "object": "chat.completion.chunk", "model": model,
		"choices": []interface{}{map[string]interface{}{
			"index": 0,
			"delta": map[string]interface{}{"content": content},
		}},
	}
}

// 上游模型名与虚拟名不同时，帧里的 model 必须被换回来，其余字节一个都不动——
// 包括 event: 行、CRLF 行尾和 IR 没声明的字段。
// 转售平台上漏出去的不只是"用了哪个型号"，而是整条上游链路的身份信息。
func TestRawStreamRewritesModelKeepsRestVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewRawStreamWriter(rec, rec, "virt-model", "upstream-real")

	frame := []byte("event: message\r\ndata: {\"id\":\"c1\",\"model\":\"upstream-real\",\"presence_penalty\":1e-1," +
		"\"content\":\"<ok> & 好\",\"vendor_ext\":{\"trace\":\"abc\"}}\r\n\r\n")
	sw.Forward(frame, mustData(t, frame))

	got := rec.Body.String()
	if !strings.HasPrefix(got, "event: message\r\ndata: {") {
		t.Errorf("帧头被改动: %q", got)
	}
	if !strings.HasSuffix(got, "\r\n\r\n") {
		t.Errorf("CRLF 帧尾被规范化成了 LF: %q", got)
	}
	for _, keep := range []string{`"id":"c1"`, `"presence_penalty":1e-1`, `"content":"<ok> & 好"`, `"vendor_ext":{"trace":"abc"}`} {
		if !strings.Contains(got, keep) {
			t.Errorf("原字节被改动 %s:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "upstream-real") {
		t.Errorf("上游模型名泄露给客户端:\n%s", got)
	}
	if out := chunkPayload(t, rec.Body.Bytes()); out["model"] != "virt-model" {
		t.Errorf("model = %v, want virt-model", out["model"])
	}
}

// 同名时不该做任何改写动作——直通路径最理想的结果是一个字节都不碰。
func TestRawStreamForwardsUntouchedWhenModelMatches(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewRawStreamWriter(rec, rec, "same", "same")
	frame := []byte(": ping\n\ndata: {\"model\":\"same\",\"x\":1e1}\n\n")
	sw.Forward(frame, mustData(t, frame))
	if got := rec.Body.Bytes(); string(got) != string(frame) {
		t.Errorf("字节被改动了:\n got=%q\nwant=%q", got, frame)
	}
}

// usage 末帧必须被窥到：它是精确计费的依据，也决定还要不要补一个合成帧。
func TestRawStreamPeeksUsageAndText(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewRawStreamWriter(rec, rec, "v", "v")

	for _, f := range [][]byte{
		chunkFrame(t, deltaFrame("1", "v", "hel")),
		chunkFrame(t, deltaFrame("2", "v", "lo")),
		chunkFrame(t, map[string]interface{}{
			"id": "3", "object": "chat.completion.chunk", "model": "v", "choices": []interface{}{},
			"usage": map[string]interface{}{
				"prompt_tokens": 120, "completion_tokens": 34, "total_tokens": 154,
				"prompt_tokens_details": map[string]interface{}{"cached_tokens": 100},
			},
		}),
		doneFrame,
	} {
		sw.Forward(f, mustData(t, f))
	}
	sw.Finish()

	u := sw.Usage()
	if u.PromptTokens != 120 || u.CompletionTokens != 34 || u.CachedTokens != 100 {
		t.Errorf("用量窥探失败: %+v", u)
	}
	body := rec.Body.String()
	if strings.Count(body, "data:") != 4 {
		t.Errorf("已有真实 usage 时不该再补合成帧（2 内容帧 + 1 usage 帧 + 1 终止帧），实际:\n%s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Error("[DONE] 帧必须原样转发")
	}
}

// 兼容网关里确实有忽略 stream_options 的：拿不到 usage 时要同时补上
// 合成 usage 帧（保持客户端可见形态一致）和字符估算的计费兜底（否则这一单免费）。
func TestRawStreamSynthesizesUsageFrameWhenUpstreamOmitsIt(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewRawStreamWriter(rec, rec, "virt", "virt")

	for _, f := range [][]byte{
		chunkFrame(t, deltaFrame("1", "virt", "hello")),
		doneFrame,
	} {
		sw.Forward(f, mustData(t, f))
	}
	sw.Finish()

	frames := strings.Split(strings.TrimSuffix(rec.Body.String(), "\n\n"), "\n\n")
	if len(frames) != 3 {
		t.Fatalf("应补一个 usage 合成帧，实际 %d 帧:\n%s", len(frames), rec.Body.String())
	}
	last := chunkPayload(t, []byte(frames[1]))
	usage, ok := last["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("第二帧应为 usage 帧: %v", last)
	}
	if _, hasChoices := last["choices"]; !hasChoices {
		t.Error("合成 usage 帧应带空 choices 数组（OpenAI 形态）")
	}
	if usage["prompt_tokens"].(float64) != 0 {
		t.Errorf("无来源时输入用量不该凭空捏造: %v", usage)
	}
	if usage["completion_tokens"].(float64) == 0 {
		t.Error("输出用量应回退到文本估算，为 0 意味着这一单白送")
	}
	if frames[2] != "data: [DONE]" {
		t.Errorf("终止帧应在合成帧之后: %q", frames[2])
	}
	// 兜底估算值要与 Usage() 报给计费的那个一致
	if u := sw.Usage(); u.CompletionTokens == 0 {
		t.Error("Usage() 未回退到文本估算")
	}
}

// 上游直接关连接、没发 [DONE] 时，网关必须替它补上，否则客户端会一直等。
func TestRawStreamAppendsDoneWhenUpstreamForgets(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewRawStreamWriter(rec, rec, "v", "v")
	f := chunkFrame(t, deltaFrame("1", "v", "x"))
	sw.Forward(f, mustData(t, f))
	sw.Finish()

	if !strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n") {
		t.Errorf("缺终止帧:\n%s", rec.Body.String())
	}
}

// 每帧都带一个全零 usage 的网关不该把 gotUsage 置真：
// 那会让真实的文本估算兜底失效，账单直接归零。
func TestRawStreamIgnoresAllZeroUsageFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewRawStreamWriter(rec, rec, "v", "v")
	for _, f := range [][]byte{
		chunkFrame(t, deltaFrame("1", "v", "hello")),
		chunkFrame(t, map[string]interface{}{
			"id": "2", "model": "v", "choices": []interface{}{},
			"usage": map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
		}),
	} {
		sw.Forward(f, mustData(t, f))
	}
	sw.Finish()
	if u := sw.Usage(); u.CompletionTokens == 0 {
		t.Errorf("全零 usage 帧被当成了真实用量: %+v", u)
	}
}

// 载荷不是 JSON 对象（[DONE]、心跳、上游发的怪东西）时窥探必须安静跳过，不能影响转发。
func TestRawStreamPeekSurvivesNonObjectPayload(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewRawStreamWriter(rec, rec, "v", "up")
	for _, payload := range []string{"[DONE]", "not json", "null", `"a string"`} {
		f := []byte("data: " + payload + "\n\n")
		sw.Forward(f, mustData(t, f))
	}
	sw.Finish()
	if !strings.Contains(rec.Body.String(), "not json") {
		t.Errorf("非 JSON 帧也该原样转发:\n%s", rec.Body.String())
	}
}

// doneFrame 是上游的流终止帧。它不是 JSON，所以不经 chunkFrame 构造。
var doneFrame = []byte("data: [DONE]\n\n")

// mustData 模拟 provider 交给写出器的 data 载荷。
func mustData(t *testing.T, frame []byte) []byte {
	t.Helper()
	payload, _, _, ok := dataPayloadSpan(frame)
	if !ok {
		return nil
	}
	return payload
}
