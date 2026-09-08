package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// ConfigureTrustedProxies 配置 gin 的可信代理列表，正确解析 X-Forwarded-For 得到真实客户端 IP。
//   - proxies 为空：设为不信任任何代理（c.ClientIP() 直接返回 RemoteAddr），防止 XFF 伪造，
//     这是面向公网部署的保守安全默认。
//   - proxies 非空：仅信任配置的代理网段/地址。
//
// 借鉴 new-api：未配置时打印告警，提示运维显式设置。
func ConfigureTrustedProxies(r *gin.Engine, proxies []string) {
	if len(proxies) == 0 {
		if err := r.SetTrustedProxies(nil); err != nil {
			logrus.Warnf("设置可信代理失败: %v", err)
		}
		logrus.Warn("未配置 trusted_proxies：已设为不信任任何代理，X-Forwarded-For 将不被采信。" +
			"若部署在反向代理之后，请在 config.yaml 的 trusted_proxies 中填写代理网段。")
		return
	}
	if err := r.SetTrustedProxies(proxies); err != nil {
		logrus.Errorf("设置可信代理失败: %v", err)
		return
	}
	logrus.Infof("已配置可信代理: %v", proxies)
}
