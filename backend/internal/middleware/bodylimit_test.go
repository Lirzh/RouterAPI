package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// limitProbe 把 BodyLimit 挂进一条真实链路，返回引擎。
// 读方复刻代理路由的做法：整体读 body，然后区分"超限"与"其它读错误"。
func limitProbe(maxBytes int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(maxBytes))
	r.POST("/probe", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		switch {
		case err != nil:
			var over *http.MaxBytesError
			if errors.As(err, &over) {
				c.String(http.StatusRequestEntityTooLarge, "too large")
				return
			}
			c.String(http.StatusBadRequest, "read error")
		default:
			c.String(http.StatusOK, "%d", len(body))
		}
	})
	return r
}

type bodyCase struct {
	// 真实要发的 body
	body string
	// 请求里声明的 Content-Length；-1 表示不声明（chunked 由 ServeHTTP 侧决定）
	declared int64
}

func post(t *testing.T, r *gin.Engine, c bodyCase) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/probe", strings.NewReader(c.body))
	req.Header.Set("Content-Type", "application/json")
	switch {
	case c.declared >= 0:
		req.ContentLength = c.declared
	default:
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const (
	body50 = "aaaaaaaaaa" + "aaaaaaaaaa" + "aaaaaaaaaa" + "aaaaaaaaaa" + "aaaaaaaaaa"
	body30 = "bbbbbbbbbb" + "bbbbbbbbbb" + "bbbbbbbbbb"
)

func TestBodyLimitRejectsOversizedBody(t *testing.T) {
	rec := post(t, limitProbe(20), bodyCase{body: body50, declared: 50})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("状态码 = %d, want 413；响应 %q", rec.Code, rec.Body.String())
	}
	// 关键是"报错"而不是"截断后当合法请求继续处理"
	if strings.Contains(rec.Body.String(), "20") {
		t.Errorf("body 被静默截断成上限长度了: %q", rec.Body.String())
	}
}

// 客户端可以自称 5 字节然后一直喂，所以只信声明不够。
// 这条断的是"上限不依赖 Content-Length"，也是代理路由里那道预检之外的另一半。
func TestBodyLimitIgnoresUndeclaredContentLength(t *testing.T) {
	rec := post(t, limitProbe(20), bodyCase{body: body50, declared: 5})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("自称小 body 的请求未被拦下: 状态码 = %d, 响应 %q", rec.Code, rec.Body.String())
	}
	rec = post(t, limitProbe(20), bodyCase{body: body50, declared: -1})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("未声明长度的请求未被拦下: 状态码 = %d", rec.Code)
	}
}

func TestBodyLimitPassesUnderLimitThrough(t *testing.T) {
	rec := post(t, limitProbe(1<<20), bodyCase{body: body30, declared: 30})
	if rec.Code != http.StatusOK || rec.Body.String() != "30" {
		t.Errorf("未超限的请求被影响了: %d %q", rec.Code, rec.Body.String())
	}
}

// 上限非正数时不包一层：http.MaxBytesReader 传 0 会把所有 body 判成超限。
func TestBodyLimitDisabledLeavesBodyAlone(t *testing.T) {
	rec := post(t, limitProbe(0), bodyCase{body: body50, declared: 50})
	if rec.Code != http.StatusOK {
		t.Errorf("关闭上限时不该拦截: %d %q", rec.Code, rec.Body.String())
	}
}
