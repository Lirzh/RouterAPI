package provider

import (
	"fmt"

	"github.com/routerapi/backend/internal/model"
)

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
