package provider

import (
	"encoding/json"
	"testing"
)

// 这些测试锁住"上游用量 -> IR 归一化用量"的映射契约。
// 引入缓存/推理定价前，三个适配器都只取 input/output 两项，
// cache_read / cache_creation / thoughts / cached_tokens 被静默丢弃，
// 导致命中缓存的 token 按全价收费。契约见 ir.Usage 的注释。

func TestOpenAIUsageAdoptsDetails(t *testing.T) {
	body := `{
		"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1000,"completion_tokens":700,"total_tokens":1700,
			"prompt_tokens_details":{"cached_tokens":800,"audio_tokens":0},
			"completion_tokens_details":{"reasoning_tokens":500}}
	}`
	var resp ChatResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	resp.Usage.AdoptOpenAIDetails()

	want := Usage{
		PromptTokens: 1000, CompletionTokens: 700, TotalTokens: 1700,
		CachedTokens: 800, ReasoningTokens: 500,
	}
	if got := resp.Usage; got.PromptTokens != want.PromptTokens || got.CompletionTokens != want.CompletionTokens ||
		got.TotalTokens != want.TotalTokens || got.CachedTokens != want.CachedTokens || got.ReasoningTokens != want.ReasoningTokens {
		t.Errorf("归一化用量 = %+v, want %+v", got, want)
	}
	// 明细必须原样回传给 OpenAI 客户端，不能被归一化过程吃掉。
	if resp.Usage.PromptDetails == nil || resp.Usage.PromptDetails.CachedTokens != 800 {
		t.Errorf("prompt_tokens_details 未保留: %+v", resp.Usage.PromptDetails)
	}
}

func TestAnthropicUsageFoldsCacheIntoPrompt(t *testing.T) {
	// Anthropic 三段互不包含：100 新输入 + 800 缓存读 + 50 缓存写，输出 20。
	u := anthropicUsage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 800, CacheCreationInputTokens: 50}
	got := u.toIR()

	if got.PromptTokens != 950 {
		t.Errorf("PromptTokens = %d, want 950（三段之和）", got.PromptTokens)
	}
	if got.CachedTokens != 800 || got.CacheWriteTokens != 50 {
		t.Errorf("缓存子集 = read %d / write %d, want 800 / 50", got.CachedTokens, got.CacheWriteTokens)
	}
	if got.CompletionTokens != 20 || got.TotalTokens != 970 {
		t.Errorf("Completion/Total = %d / %d, want 20 / 970", got.CompletionTokens, got.TotalTokens)
	}
	// 收缩后不应出现负的全价段。
	if got.UncachedPromptTokens() != 100 {
		t.Errorf("UncachedPromptTokens = %d, want 100", got.UncachedPromptTokens())
	}
}

func TestAnthropicStreamUsageMergeMax(t *testing.T) {
	// message_start 带 input/cache 与 output 初值；message_delta 只带累计 output。
	var acc anthropicUsage
	acc.mergeMax(anthropicUsage{InputTokens: 100, OutputTokens: 1, CacheReadInputTokens: 800})
	acc.mergeMax(anthropicUsage{OutputTokens: 250})

	got := acc.toIR()
	if got.PromptTokens != 900 {
		t.Errorf("PromptTokens = %d, want 900（message_delta 缺 input 时不得清零）", got.PromptTokens)
	}
	if got.CachedTokens != 800 {
		t.Errorf("CachedTokens = %d, want 800", got.CachedTokens)
	}
	if got.CompletionTokens != 250 {
		t.Errorf("CompletionTokens = %d, want 250（取累计值）", got.CompletionTokens)
	}
}

func TestGeminiUsageAddsThoughtsToCompletion(t *testing.T) {
	// Gemini 的 thoughtsTokenCount 不在 candidatesTokenCount 内，但已计入 total；
	// cachedContentTokenCount 则是 promptTokenCount 的子集。
	got := geminiUsage{
		PromptTokenCount: 1000, CandidatesTokenCount: 30, TotalTokenCount: 1700,
		ThoughtsTokenCount: 670, CachedContentTokenCount: 400,
	}.toIR()

	if got.PromptTokens != 1000 {
		t.Errorf("PromptTokens = %d, want 1000", got.PromptTokens)
	}
	if got.CachedTokens != 400 {
		t.Errorf("CachedTokens = %d, want 400", got.CachedTokens)
	}
	if got.CompletionTokens != 700 {
		t.Errorf("CompletionTokens = %d, want 700（candidates 30 + thoughts 670）", got.CompletionTokens)
	}
	if got.ReasoningTokens != 670 || got.TotalTokens != 1700 {
		t.Errorf("Reasoning/Total = %d / %d, want 670 / 1700", got.ReasoningTokens, got.TotalTokens)
	}
}

// 上游省略 totalTokenCount 时按三段之和兜底，不能算成 0。
func TestGeminiUsageDerivesTotalWhenAbsent(t *testing.T) {
	got := geminiUsage{PromptTokenCount: 100, CandidatesTokenCount: 20, ThoughtsTokenCount: 5}.toIR()
	if got.TotalTokens != 125 {
		t.Errorf("TotalTokens = %d, want 125", got.TotalTokens)
	}
}
