package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/routerapi/backend/internal/errkit"
)

// nBytesReader 造一个恰好 n 字节的可读流，不一次性分配整块内存。
func nBytesReader(n int) io.Reader {
	chunk := strings.Repeat("a", 1<<20)
	parts := make([]io.Reader, 0, n/len(chunk)+1)
	for remaining := n; remaining > 0; remaining -= len(chunk) {
		if remaining < len(chunk) {
			parts = append(parts, strings.NewReader(chunk[:remaining]))
			break
		}
		parts = append(parts, strings.NewReader(chunk))
	}
	return io.MultiReader(parts...)
}

func TestReadAllCappedBoundary(t *testing.T) {
	// 正好到上限必须放行：图片模型的 base64 输出本来就可能有几十 MB，
	// 把边界判成超限等于把正常响应打成错误。
	exact := int(maxUpstreamBodyBytes)
	got, err := readAllCapped(nBytesReader(exact))
	if err != nil {
		t.Fatalf("恰好到上限的响应被拒了: %v", err)
	}
	if len(got) != exact {
		t.Errorf("读回 %d 字节, want %d", len(got), exact)
	}

	if _, err := readAllCapped(nBytesReader(exact + 1)); err == nil {
		t.Error("超 1 字节的响应被放行了，上限形同虚设")
	}
}

// 上游响应超限必须报成"供应商级错误"：这样才会换一家重试，并被熔断计数记到头上。
// 若只是普通内部错误，客户端会一直对一个其实只是被这家上游坑了的模型重试同一份结果。
func TestChatRejectsOversizedUpstreamResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, nBytesReader(int(maxUpstreamBodyBytes)+4096))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("t", srv.URL, "k", "Authorization", "Bearer ")
	_, err := p.Chat(context.Background(), &ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("超大上游响应应报错")
	}
	if !errkit.IsChannelError(errkit.FromError(err)) {
		t.Errorf("应判为供应商级错误（要换供应商重试）, got %v", err)
	}
}

// 三家适配器都得真的走带上限的读取。这里用 Models() 覆盖 anthropic / gemini 两条，
// openai 的 Chat 与 RawChat 各在别处覆盖。
func TestModelsRejectsOversizedUpstreamResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, nBytesReader(int(maxUpstreamBodyBytes)+4096))
	}))
	defer srv.Close()

	cases := []struct {
		name string
		prov Provider
	}{
		{"anthropic", NewAnthropicProvider("t", srv.URL, "k")},
		{"gemini", NewGeminiProvider("t", srv.URL, "k")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.prov.Models(context.Background()); err == nil {
				t.Fatal("超大响应未被拦下")
			} else if !errkit.IsChannelError(errkit.FromError(err)) {
				t.Errorf("应判为供应商级错误, got %v", err)
			}
		})
	}
}
