package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/jwt"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// CORS 跨域中间件
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key")
		c.Writer.Header().Set("Access-Control-Max-Age", "86400")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// RequestLogger 请求日志
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		logrus.Infof("%s %s %d %v %s", method, path, status, latency, c.ClientIP())
	}
}

// AuthAdmin 管理后台认证中间件：校验 JWT 有效性、账户状态与 TokenVersion（支持改密/禁用后撤销旧令牌）。
func AuthAdmin(jwtMgr *jwt.Manager, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未提供认证令牌"})
			c.Abort()
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		claims, err := jwtMgr.Parse(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "认证令牌无效"})
			c.Abort()
			return
		}

		// 校验账户仍存在、启用，且 TokenVersion 未失效（改密/禁用会递增，从而撤销已签发令牌）
		var user model.User
		if err := db.Select("id", "status", "role", "token_version").First(&user, claims.UserID).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "账户不存在"})
			c.Abort()
			return
		}
		if user.Status == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "账户已被禁用"})
			c.Abort()
			return
		}
		if user.TokenVersion != claims.TokenVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录状态已失效，请重新登录"})
			c.Abort()
			return
		}

		// 以数据库中的角色为准（防止令牌签发后被降权）
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", user.Role)
		c.Next()
	}
}

// RequireAdmin 要求管理员角色
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "需要管理员权限"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// ErrorHandler 全局错误处理
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 {
			err := c.Errors.Last()
			logrus.Errorf("请求错误: %v", err)
			if !c.Writer.Written() {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			}
		}
	}
}
