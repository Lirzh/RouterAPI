package provider

import (
	"fmt"
	"io"

	"github.com/routerapi/backend/internal/errkit"
)

// 上游响应体的读取上限。
//
// 供应商是管理员配的，算半可信，但这不能变成我方的内存放大器：一个 200 之后
// 持续写数据的兼容网关，等于让每家供应商各自握住一个无界分配点。
// 32MiB 足够装下最长补全与图片模型的 base64 输出，正常响应在 KB 量级。
const maxUpstreamBodyBytes int64 = 32 << 20

// maxUpstreamErrorBodyBytes 是读非 2xx 响应体的上限。那份字节只用来抽错误信息，
// 截断无害，所以给得比正常响应小得多——否则一个巨大的错误页就能白吃 32MiB。
const maxUpstreamErrorBodyBytes int64 = 1 << 20

// readAllCapped 读完上游响应体；超限直接报错，而不是截断后当合法响应去解析。
//
// 截断解析是更糟的失败方式：JSON 会被判定为"解析响应失败"（内部错误、不换供应商），
// 而"这家上游回了个畸形巨响应"恰恰是应该记账并换一家的供应商级故障。
func readAllCapped(r io.Reader) ([]byte, error) {
	// 多读 1 字节用来区分"正好到上限"和"超了"
	body, err := io.ReadAll(io.LimitReader(r, maxUpstreamBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxUpstreamBodyBytes {
		return nil, errkit.ChannelError(0, fmt.Sprintf("上游响应体超过 %d 字节上限", maxUpstreamBodyBytes), nil)
	}
	return body, nil
}
