package provider

import (
	"bufio"
	"bytes"
	"context"
	"io"

	"github.com/routerapi/backend/internal/ir"
)

// RawFrame 上游 SSE 的一帧原文。
type RawFrame struct {
	// Raw 是这一帧的完整字节，含 event:/id: 行、注释行与结尾空行。
	// 转发时按原样写出，不重新封装——这是直通路径相对 IR 路径的全部意义。
	Raw []byte
	// Data 是该帧 data: 行合并后的载荷（SSE 规范：多个 data 行以 \n 拼接）。
	// 仅用于窥探用量与终止帧，不参与转发。无 data 行时为 nil。
	Data []byte
}

// RawResult 非流式直通结果。
type RawResult struct {
	Body        []byte   // 上游响应原文（model 仍是上游名，回给客户端前要换回虚拟名）
	ContentType string   // 上游 Content-Type，供网关原样回写
	Usage       ir.Usage // 归一化用量，计费口径与 IR 路径完全一致
}

// RawStream 流式直通结果。
type RawStream struct {
	Frames      <-chan RawFrame
	ContentType string
}

// RawProvider 由"能与同格式入站请求字节级对齐"的上游适配器实现。
//
// IR 往返有保真度天花板：Extra 只能救回顶层未识别字段，改不了嵌套结构，响应侧
// 同样存在"IR 没声明的上游字段"。入站与上游格式一致时，最忠实的转换就是不转换——
// 原字节进、原字节出，只从中间读出计费需要的 usage 和需要改写的模型名。
type RawProvider interface {
	// RawChat 以原始请求体发起非流式补全。body 已是发往上游的最终字节。
	RawChat(ctx context.Context, body []byte) (*RawResult, error)
	// RawChatStream 以原始请求体发起流式补全。
	RawChatStream(ctx context.Context, body []byte) (*RawStream, error)
}

var _ RawProvider = (*OpenAIProvider)(nil)

// ============ SSE 帧读取 ============

// sseFrameReader 按帧切分 SSE 流：一帧 = 到下一个空行为止的全部字节。
//
// 与 sseDecoder 的区别是它保留整帧原文。直通路径要的是原样转发，不是解析。
type sseFrameReader struct {
	r   *bufio.Reader
	buf []byte
}

var dataLinePrefix = []byte("data:")

func newSSEFrameReader(r io.Reader) *sseFrameReader {
	return &sseFrameReader{r: bufio.NewReader(r)}
}

// next 返回下一帧。帧读完后返回 io.EOF；若流末尾还有未以空行收尾的残帧，
// 会先把它作为最后一帧返回，下一次调用才返回 EOF。
func (fr *sseFrameReader) next() (RawFrame, error) {
	chunk := make([]byte, 8192)
	for {
		if end := frameEnd(fr.buf); end >= 0 {
			raw := append([]byte(nil), fr.buf[:end]...)
			fr.buf = fr.buf[end:]
			return buildFrame(raw), nil
		}
		n, err := fr.r.Read(chunk)
		if n > 0 {
			fr.buf = append(fr.buf, chunk[:n]...)
			continue
		}
		if err != nil {
			if len(fr.buf) == 0 {
				return RawFrame{}, err
			}
			raw := append([]byte(nil), fr.buf...)
			fr.buf = fr.buf[:0]
			return buildFrame(raw), nil
		}
	}
}

// frameEnd 返回下一帧的起点（即结束空行之后）；找不到返回 -1。
func frameEnd(b []byte) int {
	start := 0
	for start < len(b) {
		nl := bytes.IndexByte(b[start:], '\n')
		if nl < 0 {
			return -1
		}
		line := b[start : start+nl]
		lineEnd := start + nl + 1
		// 空行：要么真的为空，要么是 CRLF 行尾留下的那个孤立 \r。
		if len(line) == 0 || (len(line) == 1 && line[0] == '\r') {
			return lineEnd
		}
		start = lineEnd
	}
	return -1
}

func buildFrame(raw []byte) RawFrame {
	return RawFrame{Raw: raw, Data: frameData(raw)}
}

// frameData 提取一帧里所有 data: 行的载荷并按 SSE 规范拼接。
func frameData(raw []byte) []byte {
	var out []byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, dataLinePrefix) {
			continue
		}
		payload := bytes.TrimPrefix(line, dataLinePrefix)
		// 规范允许 "data:" 后跟一个可选空格，只裁一个，不能 TrimSpace。
		if len(payload) > 0 && payload[0] == ' ' {
			payload = payload[1:]
		}
		if len(out) > 0 {
			out = append(out, '\n')
		}
		out = append(out, payload...)
	}
	return out
}

// isDoneFrame 判断是否为 OpenAI 系的流终止帧。
func isDoneFrame(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]"))
}
