package health

import (
	"testing"

	"github.com/routerapi/backend/internal/errkit"
)

// 探测三态判定的回归测试。
//
// 把"上游没有模型发现端点"误判成"仍未恢复"会持续 ExtendCooldown，
// 一个对话完全正常的供应商会被永久锁死在冷却里且不会自愈——这个静默失败模式
// 值得单独钉住。
func TestProbeInconclusive(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"404 无模型端点", errkit.New(404, errkit.TypeUpstream, "upstream_error", "not found", nil), true},
		{"405 方法不允许", errkit.New(405, errkit.TypeUpstream, "upstream_error", "method not allowed", nil), true},
		{"401 密钥失效", errkit.ChannelError(401, "bad key", nil), false},
		{"403 无权访问", errkit.ChannelError(403, "forbidden", nil), false},
		{"429 限流中", errkit.ChannelError(429, "rate limited", nil), false},
		{"500 上游故障", errkit.ChannelError(500, "boom", nil), false},
		// 网络层错误状态码为 0，属于"明确还没恢复"，应当继续延长冷却。
		{"网络层错误", errkit.ChannelError(0, "connection refused", nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeInconclusive(tc.err); got != tc.want {
				t.Errorf("probeInconclusive = %v, want %v", got, tc.want)
			}
		})
	}
}
