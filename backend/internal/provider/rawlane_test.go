package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/routerapi/backend/internal/errkit"
)

// captureRawUpstream 记录上游收到的**原始字节**。
// 不解析成 map：直通路径要断言的就是字节本身。
func captureRawUpstream(t *testing.T, status int, respBody, contentType string) (baseURL string, got func() []byte) {
	t.Helper()
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received = raw
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []byte { return received }
}

const rawUpstreamOK = `{"id":"c1","object":"chat.completion","created":1,"model":"up",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020,` +
	`"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":8}},` +
	`"service_tier":"default","vendor_debug":{"trace":"abc"}}`

// 直通的第一性断言：上游收到的字节等于网关发出的字节，
// 网关收回的字节等于上游发出的字节——连 IR 没声明的 vendor_debug 都还在。
func TestRawChatIsByteFidelityBothWays(t *testing.T) {
	const sentBody = `{"model":"up","messages":[{"role":"user","content":"<hi> & 你好"}],` +
		`"response_format":{"type":"json_object"},"seed":42,"presence_penalty":1e-1}`
	url, got := captureRawUpstream(t, http.StatusOK, rawUpstreamOK, "application/json")
	p := NewOpenAIProvider("t", url, "k", "Authorization", "Bearer ")

	res, err := p.RawChat(context.Background(), []byte(sentBody))
	if err != nil {
		t.Fatalf("RawChat 失败: %v", err)
	}
	if string(got()) != sentBody {
		t.Errorf("上游收到的字节被改动了:\n got=%s\nwant=%s", got(), sentBody)
	}
	if string(res.Body) != rawUpstreamOK {
		t.Errorf("响应体没有原样回收:\n got=%s\nwant=%s", res.Body, rawUpstreamOK)
	}
	if res.ContentType != "application/json" {
		t.Errorf("ContentType = %q", res.ContentType)
	}
	// 用量归一化必须与 IR 路径同源，否则缓存/推理折扣两条路算出两个价
	if res.Usage.PromptTokens != 1000 || res.Usage.CachedTokens != 800 ||
		res.Usage.ReasoningTokens != 8 || res.Usage.CompletionTokens != 20 {
		t.Errorf("用量归一化不对: %+v", res.Usage)
	}
}

func TestRawChatUpstreamErrorIsRetryableChannelError(t *testing.T) {
	url, _ := captureRawUpstream(t, http.StatusUnauthorized, `{"error":{"message":"bad key"}}`, "application/json")
	p := NewOpenAIProvider("t", url, "k", "Authorization", "Bearer ")
	_, err := p.RawChat(context.Background(), []byte(`{"model":"up","messages":[]}`))
	if err == nil {
		t.Fatal("401 应当报错")
	}
	if !errkit.IsChannelError(errkit.FromError(err)) {
		t.Errorf("鉴权失败应判为供应商级错误（要换供应商重试），got %v", err)
	}
}

func TestRawChatStreamForcesUsageOptInAndKeepsOthers(t *testing.T) {
	url, got := captureRawUpstream(t, http.StatusOK, "data: [DONE]\n\n", "text/event-stream")
	p := NewOpenAIProvider("t", url, "k", "Authorization", "Bearer ")
	body := []byte(`{"model":"up","messages":[],"stream":false,"stream_options":{"include_usage":false,"fake_field":7}}`)

	st, err := p.RawChatStream(context.Background(), body)
	if err != nil {
		t.Fatalf("RawChatStream 失败: %v", err)
	}
	for range st.Frames {
	}

	obj := mustTopLevel(t, got())
	if string(obj["stream"]) != "true" {
		t.Errorf("stream = %s, want true", obj["stream"])
	}
	so := mustTopLevel(t, obj["stream_options"])
	if string(so["include_usage"]) != "true" {
		t.Errorf("include_usage = %s, want true（没有末帧 usage 就没法精确计费）", so["include_usage"])
	}
	if string(so["fake_field"]) != "7" {
		t.Errorf("客户端 stream_options 里的其它键被丢了: %s", obj["stream_options"])
	}
}

// 直通流式的核心承诺：帧的封装方式一个字节都不改。
// 注释心跳行、CRLF 行尾、被 TCP 拆开的帧，都要完整复原。
func TestRawChatStreamForwardsFramesVerbatim(t *testing.T) {
	const stream = ": keepalive\n\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\r\n\r\n" +
		"data: {\"id\":\"2\",\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: {\"id\":\"3\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		// 故意以 7 字节为单位切开写，逼帧读取器跨多次 Read 重组
		for i := 0; i < len(stream); i += 7 {
			end := i + 7
			if end > len(stream) {
				end = len(stream)
			}
			_, _ = w.Write([]byte(stream[i:end]))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	p := NewOpenAIProvider("t", srv.URL, "k", "Authorization", "Bearer ")
	st, err := p.RawChatStream(context.Background(), []byte(`{"model":"up","messages":[],"stream":true}`))
	if err != nil {
		t.Fatalf("RawChatStream 失败: %v", err)
	}
	var assembled []byte
	var payloads []string
	for f := range st.Frames {
		assembled = append(assembled, f.Raw...)
		payloads = append(payloads, string(f.Data))
	}
	if string(assembled) != stream {
		t.Errorf("帧原文拼接后与上游不一致:\n got=%q\nwant=%q", assembled, stream)
	}
	if len(payloads) != 5 {
		t.Fatalf("帧数 = %d, want 5（含注释帧）: %q", len(payloads), payloads)
	}
	if payloads[0] != "" {
		t.Errorf("注释帧不该有 data 载荷: %q", payloads[0])
	}
	if payloads[4] != "[DONE]" {
		t.Errorf("最后一帧 = %q, want [DONE]", payloads[4])
	}
	if st.ContentType != "text/event-stream" {
		t.Errorf("ContentType = %q", st.ContentType)
	}
}

// 上游不以空行收尾时，残帧也要交给客户端，不能吞掉。
func TestSSEFrameReaderFlushesTrailingPartialFrame(t *testing.T) {
	fr := newSSEFrameReader(strings.NewReader("data: a\n\ndata: tail-no-blank-line"))
	var got []string
	for {
		f, err := fr.next()
		if len(f.Raw) > 0 {
			got = append(got, string(f.Raw))
		}
		if err != nil {
			break
		}
	}
	if len(got) != 2 {
		t.Fatalf("帧数 = %d, want 2: %q", len(got), got)
	}
	if string(frameData([]byte(got[1]))) != "tail-no-blank-line" {
		t.Errorf("残帧内容丢失: %q", got[1])
	}
}

func mustTopLevel(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("不是 JSON 对象: %v\n%s", err, raw)
	}
	return obj
}
