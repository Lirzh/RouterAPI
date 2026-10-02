package provider

import (
	"fmt"

	"github.com/routerapi/backend/internal/ir"
	"github.com/routerapi/backend/internal/model"
)

// Vocab 返回某供应商协议所对应的 IR 词汇表标识。
//
// 与 NewFromVendor 共用同一套协议归类（openai-compat / custom 都说 OpenAI 词汇），
// 用于判定入站 Extra 能否原样回填给该上游。
func Vocab(protocol string) string {
	switch protocol {
	case model.ProtocolAnthropic:
		return ir.ProtocolAnthropic
	case model.ProtocolGemini:
		return ir.ProtocolGemini
	default:
		return ir.ProtocolOpenAI
	}
}

// NewFromVendor 根据供应商创建对应的上游适配器实例。
// 注意：传入的 Vendor.APIKey 必须已解密。
func NewFromVendor(v *model.Vendor) (Provider, error) {
	baseURL := v.EffectiveBaseURL()
	apiKey := v.APIKey
	_, authHeader, authPrefix := v.EffectiveAuth()

	switch v.Protocol {
	case model.ProtocolOpenAI, model.ProtocolOpenAICompat, model.ProtocolCustom:
		return NewOpenAIProvider(v.Name, baseURL, apiKey, authHeader, authPrefix), nil
	case model.ProtocolAnthropic:
		return NewAnthropicProvider(v.Name, baseURL, apiKey), nil
	case model.ProtocolGemini:
		return NewGeminiProvider(v.Name, baseURL, apiKey), nil
	default:
		return nil, fmt.Errorf("不支持的协议类型: %s", v.Protocol)
	}
}
