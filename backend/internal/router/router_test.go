package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/config"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/routerapi/backend/pkg/database"
	"gorm.io/gorm"
)

// testStack 起一套真实网关（真 sqlite + 真 router + 真 HTTP），配一个会计数的假上游。
func testStack(t *testing.T, maxBody int64) (baseURL, apiKey string, db *gorm.DB, upstreamHits *int64) {
	t.Helper()

	// 两把密钥刻意取不同值，且这里不调 handler.CryptoSetKey：
	// 供应商凭证能解开，就只可能是 router.New 用 EffectiveEncryptKey() 设对了密钥。
	const jwtSecret = "router-e2e-jwt-secret-0123456789ab"
	const encryptSecret = "router-e2e-encrypt-key-0123456789cd"

	cfg := &config.Config{
		Server: config.ServerConfig{Mode: "release", JWTSecret: jwtSecret, EncryptKey: encryptSecret,
			JWTExpireHours: 1, MaxRequestBodyBytes: maxBody},
		Database: config.DBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "t.db")},
		Retry:    config.RetryConfig{Times: 2},
		Billing:  config.BillingConfig{Enabled: true, ReserveDefaultMaxTokens: 4096, ReserveSafetyPercent: 25, ReserveTTLMinutes: 30},
	}
	gormDB, err := database.Init(cfg.Database)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { database.Close(gormDB) })
	if err := database.AutoMigrate(gormDB); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"up","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`))
	}))
	t.Cleanup(upstream.Close)

	// 用 encrypt_key 封存，模拟"已按新方案配好密钥"的库
	encKey, err := crypto.EncryptWith(encryptSecret, "sk-up")
	if err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "c", Password: "x", Role: model.RoleViewer, Credits: 1000}
	if err := gormDB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	vendor := model.Vendor{Name: "v", Protocol: model.ProtocolOpenAICompat, BaseURL: upstream.URL,
		APIKey: encKey, Status: 1, Weight: 1, MaxRetry: 3}
	if err := gormDB.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	m := model.Model{Name: "virt", DisplayName: "virt", Enabled: true,
		PromptCreditsPer1M: 1000, CompletionCreditsPer1M: 2000}
	if err := gormDB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	// 上游模型名与虚拟名不同，顺带把直通 lane 的改名路径也走一遍
	binding := model.ModelBinding{ModelID: m.ID, VendorID: vendor.ID, UpstreamModel: "up", Enabled: true, Weight: 1}
	if err := gormDB.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	// 与 handler 的创建路径一致：库里只落摘要与脱敏形态，明文只交给调用方一次。
	const testKeySecret = "sk-test-key-0123456789abcdef0123456789abcdef"
	key := model.APIKey{
		Name: "k", KeyHash: crypto.HashSecret(testKeySecret), KeyMasked: crypto.MaskString(testKeySecret),
		OwnerUserID: user.ID, Status: 1, AllowedModels: `["virt"]`,
	}
	if err := gormDB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(New(gormDB, cfg, nil, nil))
	t.Cleanup(srv.Close)
	return srv.URL, testKeySecret, gormDB, &hits
}

func chatBody(size int) []byte {
	return []byte(`{"model":"virt","messages":[{"role":"user","content":"` + strings.Repeat("x", size) + `"}]}`)
}

// sendChat 真实地发一次 /v1/chat/completions。chunked=true 时不声明 Content-Length，
// 用来绕过代理路由里那道"先看声明长度"的预检。
func sendChat(t *testing.T, url, apiKey string, body []byte, chunked bool) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if chunked {
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return resp.StatusCode, "unparseable:" + string(raw)
	}
	return resp.StatusCode, parsed.Error.Code
}

// 超限请求必须被拒在门外：不打上游、不动钱包。
// 尤其"不动钱包"——预留发生在解析之后，若先预留再拒，用户余额会被一次体积问题冻住。
func TestOversizedRequestBodyGets413(t *testing.T) {
	url, apiKey, db, hits := testStack(t, 512)

	code, errCode := sendChat(t, url, apiKey, chatBody(4096), false)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d (%s), want 413", code, errCode)
	}
	if errCode != "request_too_large" {
		t.Errorf("错误码 = %q, want request_too_large", errCode)
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Errorf("超限请求仍打了上游 %d 次", n)
	}

	var u model.User
	if err := db.First(&u).Error; err != nil {
		t.Fatal(err)
	}
	if u.ReservedCredits != 0 {
		t.Errorf("预留未被拒在门外: reserved_credits = %d", u.ReservedCredits)
	}
	var ok int64
	db.Model(&model.RequestLog{}).Where("success = ?", true).Count(&ok)
	if ok != 0 {
		t.Errorf("被拒的请求写出了成功日志: %d 条", ok)
	}
}

// 客户端按 chunked 一直喂时，Content-Length 预检不生效，必须由 BodyLimit 在读时兜住。
func TestOversizedChunkedBodyGets413(t *testing.T) {
	url, apiKey, _, hits := testStack(t, 512)

	code, detail := sendChat(t, url, apiKey, chatBody(4096), true)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked 超限未被拦下: %d %s", code, detail)
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Errorf("仍打了上游 %d 次", n)
	}
}

// 上限不能把正常请求一起挡掉：限流拦的是体积，不是功能。
// 1MB 的 body 是"带 base64 图片的多模态请求"的最小合理量级。
func TestNormalSizedBodyStillWorks(t *testing.T) {
	url, apiKey, db, hits := testStack(t, config.DefaultMaxRequestBodyBytes)

	code, detail := sendChat(t, url, apiKey, chatBody(1<<20), false)
	if code != http.StatusOK {
		t.Fatalf("1MB 的正常请求被拒了: %d %s", code, detail)
	}
	if n := atomic.LoadInt64(hits); n != 1 {
		t.Errorf("上游调用次数 = %d, want 1", n)
	}
	var u model.User
	if err := db.First(&u).Error; err != nil {
		t.Fatal(err)
	}
	if u.ReservedCredits != 0 {
		t.Errorf("结算后仍留着预留: %d", u.ReservedCredits)
	}
	if u.Credits >= 1000 {
		t.Errorf("请求成功却没扣积分: credits = %d", u.Credits)
	}
}

// 管理端的 JSON 接口同样受保护：超限会变成绑定错误，而不是被整个读进内存。
func TestAdminEndpointRejectsOversizedBody(t *testing.T) {
	url, _, _, _ := testStack(t, 512)

	resp, err := http.Post(url+"/api/auth/login", "application/json",
		bytes.NewReader([]byte(`{"username":"`+strings.Repeat("z", 4096)+`"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("超限的登录请求居然成功了")
	}
	if resp.StatusCode >= 500 {
		t.Errorf("体积超限不该变成 5xx: %d", resp.StatusCode)
	}
}
