package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/ratelimit"
	"github.com/routerapi/backend/pkg/config"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/routerapi/backend/pkg/database"
	"github.com/routerapi/backend/pkg/jwt"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const testJWTSecret = "handler-test-secret-0123456789abcdef"

// newTestDB 建一个文件库并跑真实迁移。
// 用文件而不是内存库：连接数被钉在 1 会让"关掉即销毁"的行为更好预测，
// 且 Windows 上句柄必须随用例释放（见 t.Cleanup）。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func seedUser(t *testing.T, db *gorm.DB, name, password, role string, status int) *model.User {
	t.Helper()
	hashed, err := crypto.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Username: name, Password: hashed, Role: role, Status: status, Credits: 100}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	if status == 0 {
		// gorm:"default:1" 会让零值 Status 在 INSERT 时被省略、落到库端默认，
		// 直插得不到"禁用"。按管理员的真实操作路径（建完再改状态）补一刀。
		if err := db.Model(&u).Update("status", 0).Error; err != nil {
			t.Fatalf("置禁用失败: %v", err)
		}
	}
	return &u
}

func loginEngine(t *testing.T, db *gorm.DB, sec config.SecurityConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewAuthHandler(db, jwt.NewManager(testJWTSecret, 1), ratelimit.New(), sec)
	r := gin.New()
	r.POST("/login", h.Login)
	return r
}

type loginResult struct {
	status     int
	body       string
	retryAfter string
}

func doLogin(r *gin.Engine, user, pass string) loginResult {
	req := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return loginResult{
		status:     rec.Code,
		body:       strings.TrimSpace(rec.Body.String()),
		retryAfter: rec.Header().Get("Retry-After"),
	}
}

func TestLoginRateLimitPerUsername(t *testing.T) {
	db := newTestDB(t)
	seedUser(t, db, "alice", "correct horse", model.RoleAdmin, 1)
	sec := config.SecurityConfig{LoginPerIPPerMin: 1000, LoginPerUserPerMin: 3}
	r := loginEngine(t, db, sec)

	for i := 0; i < 3; i++ {
		if got := doLogin(r, "alice", "wrong"); got.status != 401 {
			t.Fatalf("第 %d 次错误密码应 401，实际 %d %s", i+1, got.status, got.body)
		}
	}
	locked := doLogin(r, "alice", "correct horse")
	if locked.status != http.StatusTooManyRequests {
		t.Fatalf("超过每用户名阈值后应被限流，实际 %d %s", locked.status, locked.body)
	}
	if n, e := strconv.Atoi(locked.retryAfter); e != nil || n < 1 {
		t.Errorf("429 缺少可用的 Retry-After 头: %q", locked.retryAfter)
	}
	// 限流是按用户名的：另一个账号的正常登录不该被邻居的失败尝试连坐
	if ok := doLogin(r, "bob", "pw"); ok.status != http.StatusUnauthorized {
		t.Errorf("未限流的其它账号被误伤了: %d %s", ok.status, ok.body)
	}
}

// 只按用户名限流会漏掉"一个来源扫一批账号"的凭据填充，所以还需要 IP 维度。
func TestLoginRateLimitPerIP(t *testing.T) {
	db := newTestDB(t)
	seedUser(t, db, "carol", "pw", model.RoleAdmin, 1)
	sec := config.SecurityConfig{LoginPerIPPerMin: 2, LoginPerUserPerMin: 1000}
	r := loginEngine(t, db, sec)

	// 每次换一个不存在的用户名，绕开用户名维度
	for i, name := range []string{"dave", "erin", "frank"} {
		got := doLogin(r, name, "pw")
		if i < 2 && got.status != http.StatusUnauthorized {
			t.Fatalf("第 %d 次应放行，实际 %d", i+1, got.status)
		}
		if i == 2 && got.status != http.StatusTooManyRequests {
			t.Fatalf("同一来源换用户名扫库应被 IP 维度拦下，实际 %d %s", got.status, got.body)
		}
	}
}

// 忘了配限流值时必须是"有限流"，不是"不限流"。
func TestLoginRateLimitDefaultsAreEnforced(t *testing.T) {
	db := newTestDB(t)
	seedUser(t, db, "gina", "pw", model.RoleAdmin, 1)
	// 零值配置走默认阈值（下面用 tight 引擎验证真的会拦）

	h := NewAuthHandler(db, jwt.NewManager(testJWTSecret, 1), nil, config.SecurityConfig{})
	if h.perIP != config.DefaultLoginPerIPPerMin || h.perUser != config.DefaultLoginPerUserPerMin {
		t.Errorf("零值配置未回落到默认阈值: ip=%d user=%d", h.perIP, h.perUser)
	}
	// 且真的能拦：把每用户名阈值压到 1 之后第二次必须 429
	tight := loginEngine(t, db, config.SecurityConfig{LoginPerIPPerMin: 1000, LoginPerUserPerMin: 1})
	if got := doLogin(tight, "gina", "bad"); got.status != http.StatusUnauthorized {
		t.Fatalf("首次错误密码应 401，实际 %d", got.status)
	}
	if got := doLogin(tight, "gina", "bad"); got.status != http.StatusTooManyRequests {
		t.Fatalf("第二次应 429，实际 %d", got.status)
	}
}

// 响应侧不泄露账号是否存在：未知用户名与密码错误必须一字不差地相同。
func TestLoginDoesNotLeakAccountExistence(t *testing.T) {
	db := newTestDB(t)
	seedUser(t, db, "henry", "real-password", model.RoleAdmin, 1)
	r := loginEngine(t, db, config.SecurityConfig{LoginPerIPPerMin: 1000, LoginPerUserPerMin: 1000})

	unknown := doLogin(r, "no-such-user", "whatever")
	badPass := doLogin(r, "henry", "wrong-password")
	if unknown.status != badPass.status || unknown.body != badPass.body {
		t.Errorf("账号存在性可从响应区分: 未知用户=(%d,%s) 密码错=(%d,%s)",
			unknown.status, unknown.body, badPass.status, badPass.body)
	}
	if unknown.status != http.StatusUnauthorized {
		t.Errorf("应统一回 401, got %d", unknown.status)
	}
}

// "账户已被禁用"是句真话，但只有在密码验对之后才可以说。
// 原先它排在密码校验前面，任何人都能靠状态码列出哪些账号存在且被停用。
func TestLoginDisabledAccountStillNeedsPassword(t *testing.T) {
	db := newTestDB(t)
	seedUser(t, db, "ivy", "her-password", model.RoleViewer, 0)
	r := loginEngine(t, db, config.SecurityConfig{LoginPerIPPerMin: 1000, LoginPerUserPerMin: 1000})

	guess := doLogin(r, "ivy", "not-her-password")
	if guess.status != http.StatusUnauthorized {
		t.Errorf("被禁用账号 + 错误密码应回 401（与未知用户同形），实际 %d %s", guess.status, guess.body)
	}
	if strings.Contains(guess.body, "禁用") {
		t.Errorf("错误密码的情况下泄露了禁用状态: %s", guess.body)
	}
	disabled := doLogin(r, "ivy", "her-password")
	if disabled.status != http.StatusForbidden {
		t.Errorf("验对密码后才该告知禁用状态, got %d %s", disabled.status, disabled.body)
	}
}

func TestLoginSuccessIssuesUsableToken(t *testing.T) {
	db := newTestDB(t)
	u := seedUser(t, db, "kate", "pw-kate", model.RoleAdmin, 1)
	got := doLogin(loginEngine(t, db, config.SecurityConfig{}), "kate", "pw-kate")
	if got.status != 200 {
		t.Fatalf("正确口令登录失败: %d %s", got.status, got.body)
	}

	var out struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	if err := json.Unmarshal([]byte(got.body), &out); err != nil {
		t.Fatalf("响应不是预期的 JSON: %v\n%s", err, got.body)
	}
	claims, err := jwt.NewManager(testJWTSecret, 1).Parse(out.Token)
	if err != nil {
		t.Fatalf("签出的令牌解析不了: %v", err)
	}
	if claims.UserID != u.ID || claims.Role != model.RoleAdmin {
		t.Errorf("令牌主体不对: %+v", claims)
	}
	var reloaded model.User
	db.First(&reloaded, u.ID)
	if reloaded.LastLoginAt == nil {
		t.Error("登录成功没记 last_login_at")
	}
}
