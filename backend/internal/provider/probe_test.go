package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/routerapi/backend/internal/errkit"
)

// 这三个适配器都实现了 Provider.Models()，而它同时是
//   - 管理端"测试连通性"的依据（handler/vendor.go 的 Test）
//   - 后台健康探测判定供应商能否恢复的唯一信号（health/monitor.go）
//
// 因此"上游鉴权失败必须返回 error"是硬契约：返回 nil 会让一个密钥已死的供应商
// 被探测判定为健康、清掉熔断，然后真实流量再把它熔断，反复抖动。
// Anthropic 的 Models() 一度直接返回硬编码列表且永不报错，Gemini 的漏了状态码检查。

func TestModelsReportsAuthFailure(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		New     func(baseURL string) Provider
		listen  string
		failure string
	}{
		{
			name: "openai", path: "/v1/models",
			New:     func(u string) Provider { return NewOpenAIProvider("t", u, "k", "Authorization", "Bearer ") },
			listen:  `{"data":[{"id":"gpt-4o"}]}`,
			failure: `{"error":{"message":"Incorrect API key provided"}}`,
		},
		{
			name: "anthropic", path: "/v1/models",
			New:     func(u string) Provider { return NewAnthropicProvider("t", u, "k") },
			listen:  `{"data":[{"id":"claude-3-5-sonnet-latest","type":"model"}],"has_more":false}`,
			failure: `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
		},
		{
			name: "gemini", path: "/v1beta/models",
			New:     func(u string) Provider { return NewGeminiProvider("t", u, "k") },
			listen:  `{"models":[{"name":"models/gemini-1.5-pro"}]}`,
			failure: `{"error":{"code":401,"message":"API key not valid"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"_401_必须报错", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(tc.failure))
			}))
			defer srv.Close()

			got, err := tc.New(srv.URL).Models(context.Background())
			if err == nil {
				t.Fatalf("鉴权失败时 Models 返回了 nil error, got models=%v（会让健康探测误判供应商已恢复）", got)
			}
			ae := errkit.FromError(err)
			if ae.StatusCode != http.StatusUnauthorized {
				t.Errorf("错误状态码 = %d, want 401", ae.StatusCode)
			}
			// 401 属于供应商级问题，必须标成 channel 错误以触发换供应商重试。
			if !errkit.IsChannelError(err) {
				t.Errorf("401 未被分类为 channel 错误: %+v", ae)
			}
		})

		t.Run(tc.name+"_200_返回模型列表", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(tc.listen))
			}))
			defer srv.Close()

			got, err := tc.New(srv.URL).Models(context.Background())
			if err != nil {
				t.Fatalf("正常响应报错: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("模型数 = %d, want 1 (%v)", len(got), got)
			}
		})
	}
}

// Anthropic 的 Models() 必须真的发出网络请求，而不是返回内置清单。
// 这条专门钉住"硬编码假列表"那个 bug 不能回来。
func TestAnthropicModelsActuallyCallsUpstream(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if got := r.Header.Get("x-api-key"); got != "secret-1" {
			t.Errorf("x-api-key = %q, want secret-1", got)
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("缺少 anthropic-version 头，Anthropic 会直接 400")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-3-5-sonnet-latest"},{"id":"claude-3-haiku-latest"}]}`))
	}))
	defer srv.Close()

	got, err := NewAnthropicProvider("t", srv.URL, "secret-1").Models(context.Background())
	if err != nil {
		t.Fatalf("Models 失败: %v", err)
	}
	if called != 1 {
		t.Errorf("上游被调用 %d 次, want 1（说明 Models 仍未真实发请求）", called)
	}
	if len(got) != 2 || got[0] != "claude-3-5-sonnet-latest" {
		t.Errorf("模型列表 = %v, want 上游返回的两个 id", got)
	}
}
