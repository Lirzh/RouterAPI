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
	body, err := json.Marshal(req)
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

	respBody, err := io.ReadAll(resp.Body)
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
	return &chatResp, nil
}

func (p *OpenAIProvider) ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	req.Stream = true
	// 请求上游在流式末帧返回 usage，供精确计费（OpenAI stream_options.include_usage）
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(req)
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
		respBody, _ := io.ReadAll(resp.Body)
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
			select {
			case <-ctx.Done():
				return
			case ch <- chunk:
			}
		}
	}()

	return ch, nil
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
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取模型列表失败: %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
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
