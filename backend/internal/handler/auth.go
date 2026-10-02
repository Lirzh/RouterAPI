package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/ratelimit"
	"github.com/routerapi/backend/pkg/config"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/routerapi/backend/pkg/jwt"
	"gorm.io/gorm"
)

// AuthHandler 登录认证。
type AuthHandler struct {
	db      *gorm.DB
	jwtMgr  *jwt.Manager
	limiter *ratelimit.Limiter
	perIP   int
	perUser int
}

func NewAuthHandler(db *gorm.DB, jwtMgr *jwt.Manager, limiter *ratelimit.Limiter, sec config.SecurityConfig) *AuthHandler {
	if limiter == nil {
		limiter = ratelimit.New()
	}
	perIP, perUser := sec.LoginPerIPPerMin, sec.LoginPerUserPerMin
	if perIP <= 0 {
		perIP = config.DefaultLoginPerIPPerMin
	}
	if perUser <= 0 {
		perUser = config.DefaultLoginPerUserPerMin
	}
	return &AuthHandler{db: db, jwtMgr: jwtMgr, limiter: limiter, perIP: perIP, perUser: perUser}
}

// notFoundBcryptHash 是一个真实 bcrypt 摘要（对应一个谁都不会去注册的口令）。
// 账号不存在时也对它做一次 Compare，让"没这个用户"和"密码错了"花一样的时间——
// 否则响应快几十毫秒这一件事就足够让人离线枚举出全部用户名。
const notFoundBcryptHash = "$2a$10$nsiDNeCfB4u.3jH15pWM/OHQfNV8oq3iRRTIDu/pYwTHsUcNqu6fm"

// Login 用户名密码登录，返回 JWT。
func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 两个维度各挡一类攻击：只按 IP 会误伤共享出口的整个公司，只按用户名
	// 则允许一个来源逐个账号慢慢试。两道都在计数时才真正进 bcrypt。
	ipKey := "login:ip:" + c.ClientIP()
	userKey := "login:user:" + strings.ToLower(strings.TrimSpace(req.Username))
	if !h.limiter.Allow(ipKey, h.perIP) || !h.limiter.Allow(userKey, h.perUser) {
		c.Header("Retry-After", strconv.Itoa(h.limiter.RetryAfter(ipKey)))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "登录尝试过于频繁，请稍后再试"})
		return
	}

	var user model.User
	if err := h.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		_ = crypto.CheckPassword(notFoundBcryptHash, req.Password)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	if err := crypto.CheckPassword(user.Password, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	// 禁用提示必须在密码校验通过之后：原来它排在密码检查前面，
	// 于是任何人不用知道密码、只看状态码是 401 还是 403，就能列出哪些账号存在且被停用。
	if user.Status == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "账户已被禁用，请联系管理员"})
		return
	}

	token, err := h.jwtMgr.Generate(user.ID, user.Username, user.Role, user.TokenVersion)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成令牌失败"})
		return
	}

	now := time.Now()
	h.db.Model(&user).Update("last_login_at", now)

	c.JSON(http.StatusOK, gin.H{
		"token":    token,
		"username": user.Username,
		"role":     user.Role,
		"credits":  user.Credits,
	})
}
