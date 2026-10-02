package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/routerapi/backend/internal/errkit"
	"github.com/routerapi/backend/internal/ir"
	"github.com/sirupsen/logrus"
)

// OpenAIProvider 适配 OpenAI / Azure / 任何 OpenAI 兼容的 API
type OpenAIProvider struct {
	name       string
	baseURL    string
	apiKey     string
	authHeader string
	authPrefix string
	client     *http.Client
}

func NewOpenAIProvider(name, baseURL, apiKey, authHeader, authPrefix string) *OpenAIProvider {
	return &OpenAIProvider{
		name:       name,
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		authHeader: authHeader,
		authPrefix: authPrefix,
		client: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

func (p *OpenAIProvider) Name() string { return p.name }

func (p *OpenAIProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	body, err := ir.MarshalWithExtra(req, req.ReplayableExtra(ir.ProtocolOpenAI))
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	url := p.baseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(p.authHeader, p.authPrefix+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}
	defer resp.Body.Close()

	respBody, err := readAllCapped(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	chatResp.Usage.AdoptOpenAIDetails()
	return &chatResp, nil
}

func (p *OpenAIProvider) ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	req.Stream = true
	// 请求上游在流式末帧返回 usage，供精确计费（OpenAI stream_options.include_usage）
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	body, err := ir.MarshalWithExtra(req, req.ReplayableExtra(ir.ProtocolOpenAI))
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	url := p.baseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(p.authHeader, p.authPrefix+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBodyBytes))
		resp.Body.Close()
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}

	ch := make(chan ChatChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		decoder := newSSEDecoder(resp.Body)
		for {
			data, err := decoder.Next()
			if err != nil {
				if err != io.EOF {
					logrus.Debugf("SSE 读取结束: %v", err)
				}
				return
			}
			if data == "[DONE]" {
				return
			}
			var chunk ChatChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				logrus.Debugf("解析 SSE chunk 失败: %v", err)
				continue
			}
			if chunk.Usage != nil {
				chunk.Usage.AdoptOpenAIDetails()
			}
			select {
			case <-ctx.Done():
				return
			case ch <- chunk:
			}
		}
	}()

	return ch, nil
}

// ============ 同格式直通 ============

// rawPost 发起一次原始请求体的 POST。调用方负责关闭 resp.Body。
func (p *OpenAIProvider) rawPost(ctx context.Context, path string, body []byte, accept string) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(p.authHeader, p.authPrefix+p.apiKey)
	if accept != "" {
		httpReq.Header.Set("Accept", accept)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}
	return resp, nil
}

// RawChat 实现 RawProvider：请求体原样上行，响应体原样回收，只读出计费用量。
func (p *OpenAIProvider) RawChat(ctx context.Context, body []byte) (*RawResult, error) {
	resp, err := p.rawPost(ctx, "/v1/chat/completions", body, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := readAllCapped(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}
	usage, err := rawUsageOf(respBody)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	return &RawResult{
		Body:        respBody,
		ContentType: contentTypeOr(resp, "application/json"),
		Usage:       usage,
	}, nil
}

// RawChatStream 实现 RawProvider：逐帧透传上游 SSE，不重新封装。
func (p *OpenAIProvider) RawChatStream(ctx context.Context, body []byte) (*RawStream, error) {
	body, err := rawForceStreamUsage(body)
	if err != nil {
		return nil, err
	}
	resp, err := p.rawPost(ctx, "/v1/chat/completions", body, "text/event-stream")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBodyBytes))
		resp.Body.Close()
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(respBody))
	}

	ct := contentTypeOr(resp, "text/event-stream")
	ch := make(chan RawFrame, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		fr := newSSEFrameReader(resp.Body)
		for {
			frame, err := fr.next()
			if len(frame.Raw) > 0 {
				select {
				case <-ctx.Done():
					return
				case ch <- frame:
				}
				// [DONE] 之后不必再等上游关连接：兼容网关常有 keep-alive 语义。
				if isDoneFrame(frame.Data) {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					logrus.Debugf("SSE 读取结束: %v", err)
				}
				return
			}
		}
	}()
	return &RawStream{Frames: ch, ContentType: ct}, nil
}

// rawUsageOf 从上游响应原文里只读出 usage 段，其余字段留给客户端。
// 归一化必须与 IR 路径同源（AdoptOpenAIDetails），否则缓存/推理折扣在两条路上会算出两个价。
func rawUsageOf(body []byte) (Usage, error) {
	var parsed struct {
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Usage{}, err
	}
	parsed.Usage.AdoptOpenAIDetails()
	return parsed.Usage, nil
}

// rawForceStreamUsage 补上 stream 与 stream_options.include_usage。
//
// 没有末帧 usage 就没法精确计费。客户端显式传了 false 时同样强制打开——
// 这与 IR 路径的既有行为一致（多一个只带 usage 的末帧，各家 SDK 都能忽略）。
// 客户端 stream_options 里的其他键原样保留。
func rawForceStreamUsage(body []byte) ([]byte, error) {
	obj, err := ir.ParseJSONObject(body)
	if err != nil {
		return nil, err
	}
	obj["stream"] = json.RawMessage("true")

	so := map[string]json.RawMessage{}
	if raw, ok := obj["stream_options"]; ok {
		_ = json.Unmarshal(raw, &so) // 形态不对就整体重建，不能因此拒掉请求
	}
	if so == nil {
		so = map[string]json.RawMessage{}
	}
	if string(so["include_usage"]) != "true" {
		so["include_usage"] = json.RawMessage("true")
	}
	soRaw, err := json.Marshal(so)
	if err != nil {
		return nil, err
	}
	obj["stream_options"] = soRaw
	return ir.MarshalJSONObject(obj)
}

func contentTypeOr(resp *http.Response, fallback string) string {
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		return ct
	}
	return fallback
}

func (p *OpenAIProvider) Models(ctx context.Context) ([]string, error) {
	url := p.baseURL + "/v1/models"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set(p.authHeader, p.authPrefix+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, errkit.ChannelError(0, "请求上游失败: "+err.Error(), err)
	}
	defer resp.Body.Close()

	body, err := readAllCapped(resp.Body)
	if err != nil {
		return nil, errkit.ChannelError(0, "读取响应失败: "+err.Error(), err)
	}
	// 与 anthropic / gemini 适配器保持同一契约：非 2xx 一律走 ClassifyUpstream。
	// 原先的 fmt.Errorf 会把 401 的状态码丢掉（外面看是 500），也就不会被识别成
	// 供应商级错误——而鉴权失败正是必须换供应商重试的那一类。
	if resp.StatusCode != http.StatusOK {
		return nil, errkit.ClassifyUpstream(resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errkit.UpstreamError(http.StatusBadGateway, "解析模型列表失败", err)
	}

	models := make([]string, len(result.Data))
	for i, m := range result.Data {
		models[i] = m.ID
	}
	return models, nil
}

// SSE 解析器
type sseDecoder struct {
	reader io.Reader
	buf    string
}

func newSSEDecoder(r io.Reader) *sseDecoder {
	return &sseDecoder{reader: r}
}

func (d *sseDecoder) Next() (string, error) {
	for {
		// 从 buffer 中找换行
		idx := strings.IndexByte(d.buf, '\n')
		if idx < 0 {
			// 需要读更多数据
			tmp := make([]byte, 4096)
			n, err := d.reader.Read(tmp)
			if n > 0 {
				d.buf += string(tmp[:n])
				continue
			}
			if err != nil {
				return "", err
			}
			continue
		}
		line := d.buf[:idx]
		d.buf = d.buf[idx+1:]
		line = strings.TrimRight(line, "\r")

		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			return data, nil
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			return strings.TrimSpace(data), nil
		}
	}
}
