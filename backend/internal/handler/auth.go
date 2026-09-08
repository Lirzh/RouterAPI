package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/routerapi/backend/pkg/jwt"
	"gorm.io/gorm"
)

// AuthHandler 登录认证。
type AuthHandler struct {
	db     *gorm.DB
	jwtMgr *jwt.Manager
}

func NewAuthHandler(db *gorm.DB, jwtMgr *jwt.Manager) *AuthHandler {
	return &AuthHandler{db: db, jwtMgr: jwtMgr}
}

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

	var user model.User
	if err := h.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	if user.Status == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "账户已被禁用，请联系管理员"})
		return
	}

	if err := crypto.CheckPassword(user.Password, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
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
