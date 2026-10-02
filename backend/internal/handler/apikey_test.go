package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/crypto"
	"gorm.io/gorm"
)

func apiKeyEngine(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewAPIKeyHandler(db)
	r := gin.New()
	// 真实路由上还有一层 AuthAdmin 会写入 user_id；这里用等价的最小中间件补上归属人上下文
	r.Use(func(c *gin.Context) { c.Set("user_id", uint(1)) })
	r.POST("/api-keys", h.Create)
	r.GET("/api-keys", h.List)
	r.PUT("/api-keys/:id", h.Update)
	return r
}

type createdKey struct {
	ID        uint   `json:"id"`
	Key       string `json:"key"`
	KeyMasked string `json:"key_masked"`
}

func createKey(t *testing.T, r *gin.Engine, payload map[string]interface{}) (int, createdKey, string) {
	t.Helper()
	b, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api-keys", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	var out struct {
		Data createdKey `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Data, rec.Body.String()
}

// 建库时不许存在明文列：整个改动的目的就是"库里拿不到可用凭证"。
func columnsOf(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	types, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		t.Fatalf("读 %s 表结构失败: %v", table, err)
	}
	names := make([]string, 0, len(types))
	for _, ct := range types {
		names = append(names, ct.Name())
	}
	return names
}

func TestCreateAPIKeyStoresOnlyDigest(t *testing.T) {
	db := newTestDB(t)
	r := apiKeyEngine(t, db)

	code, created, raw := createKey(t, r, gin.H{"name": "svc"})
	if code != http.StatusOK {
		t.Fatalf("创建失败: %d %s", code, raw)
	}
	if !strings.HasPrefix(created.Key, "ra-") || len(created.Key) < minAPIKeyLen {
		t.Fatalf("响应里的明文密钥不对: %q", created.Key)
	}
	for _, col := range columnsOf(t, db, "api_keys") {
		if col == "key" {
			t.Error("api_keys 仍有明文 key 列")
		}
	}

	var stored model.APIKey
	if err := db.First(&stored, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.KeyHash != crypto.HashSecret(created.Key) {
		t.Errorf("落库的不是明文摘要: %q", stored.KeyHash)
	}
	if stored.KeyMasked == "" || strings.Contains(stored.KeyMasked, created.Key[6:]) {
		t.Errorf("脱敏形态泄露明文: %q", stored.KeyMasked)
	}

	// 全表扫一遍，任何一列都不该含明文
	var blob string
	db.Model(&model.APIKey{}).Select("key_hash || '|' || key_masked || '|' || name AS v").Scan(&blob)
	if strings.Contains(blob, created.Key) {
		t.Errorf("明文出现在库里的别处: %s", blob)
	}

	// 摘要就是唯一的查询形态（消费者侧 authenticateAPIKey 用的那条）
	var found model.APIKey
	if err := db.Where("key_hash = ? AND status = 1", crypto.HashSecret(created.Key)).First(&found).Error; err != nil {
		t.Errorf("按摘要查不回新建的 Key: %v", err)
	}
}

// 列表接口只出现一次调用者本来就有权限看的脱敏形态；明文无从返回。
func TestListAPIKeysNeverReturnsPlaintext(t *testing.T) {
	db := newTestDB(t)
	r := apiKeyEngine(t, db)

	_, first, _ := createKey(t, r, gin.H{"name": "one"})
	_, second, _ := createKey(t, r, gin.H{"name": "two", "key": "custom-value-0123456789abcdef"})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api-keys", nil))
	body := rec.Body.String()
	for _, secret := range []string{first.Key, second.Key} {
		if strings.Contains(body, secret) {
			t.Errorf("列表接口回了明文密钥: %s", body)
		}
	}
	if !strings.Contains(body, "key_masked") {
		t.Errorf("列表接口丢了脱敏形态，UI 将无法辨认是哪把: %s", body)
	}
}

// 允许自定义 Key，但不能允许一把能猜出来的 Key：只存摘要并不自动等于安全。
func TestCreateAPIKeyRejectsWeakCustomKey(t *testing.T) {
	db := newTestDB(t)
	r := apiKeyEngine(t, db)

	code, _, body := createKey(t, r, gin.H{"name": "weak", "key": "test"})
	if code != http.StatusBadRequest {
		t.Errorf("短自定义密钥应 400，实际 %d %s", code, body)
	}
	var count int64
	db.Model(&model.APIKey{}).Count(&count)
	if count != 0 {
		t.Errorf("被拒的密钥仍落库: %d 行", count)
	}
}

// 同一把 Key 建两次：摘要唯一索引会撞，必须翻成 409 而不是把驱动错误原文吐给调用方。
func TestCreateAPIKeyRejectsDuplicate(t *testing.T) {
	db := newTestDB(t)
	r := apiKeyEngine(t, db)

	const custom = "duplicate-key-0123456789abcdef"
	if code, _, body := createKey(t, r, gin.H{"name": "a", "key": custom}); code != http.StatusOK {
		t.Fatalf("首次创建失败: %d %s", code, body)
	}
	code, _, body := createKey(t, r, gin.H{"name": "b", "key": custom})
	if code != http.StatusConflict {
		t.Errorf("重复密钥应 409，实际 %d %s", code, body)
	}
	if strings.Contains(body, "UNIQUE constraint") {
		t.Errorf("原始驱动错误泄给了调用方: %s", body)
	}
}

// 更新走白名单。请求体里塞 key_hash 就等于把任意一把 Key 的凭据换成攻击者选的摘要，
// 那是比"能建 Key"更直接的越权。
func TestUpdateAPIKeyIgnoresNonAllowlistedColumns(t *testing.T) {
	db := newTestDB(t)
	r := apiKeyEngine(t, db)

	_, created, _ := createKey(t, r, gin.H{"name": "orig"})
	var before model.APIKey
	db.First(&before, created.ID)

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(gin.H{
		"name":          "renamed",
		"key_hash":      crypto.HashSecret("attacker-chosen-secret"),
		"key_masked":    "evil",
		"id":            999,
		"owner_user_id": 4242, // 在白名单里，应当生效
	})
	req := httptest.NewRequest("PUT", "/api-keys/"+strconv.Itoa(int(created.ID)), strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新失败: %d %s", rec.Code, rec.Body.String())
	}

	var after model.APIKey
	db.First(&after, created.ID)
	if after.Name != "renamed" {
		t.Errorf("白名单内的列没更新: %q", after.Name)
	}
	if after.OwnerUserID != 4242 {
		t.Errorf("owner_user_id 应可改: %d", after.OwnerUserID)
	}
	if after.KeyHash != before.KeyHash {
		t.Error("key_hash 被请求体改写了（摘要可被越权替换）")
	}
	if after.KeyMasked != before.KeyMasked {
		t.Errorf("key_masked 被请求体改写了: %q", after.KeyMasked)
	}
	if after.ID != before.ID {
		t.Error("主键被请求体改写了")
	}
	// 攻击者选的摘要必须查不到任何 Key
	var hit model.APIKey
	if err := db.Where("key_hash = ?", crypto.HashSecret("attacker-chosen-secret")).First(&hit).Error; err == nil {
		t.Error("攻击者选择的摘要在库里生效了")
	}
}

// 明文只出现一次这个承诺要有兑现处：更新接口不能把 Key 再吐出来。
func TestUpdateAPIKeyResponseHasNoPlaintext(t *testing.T) {
	db := newTestDB(t)
	r := apiKeyEngine(t, db)
	_, created, _ := createKey(t, r, gin.H{"name": "one"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api-keys/"+strconv.Itoa(int(created.ID)), strings.NewReader(`{"name":"two"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), created.Key) {
		t.Errorf("更新响应里出现了明文: %s", rec.Body.String())
	}
}
