package router

import (
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/balancer"
	"github.com/routerapi/backend/internal/handler"
	"github.com/routerapi/backend/internal/middleware"
	"github.com/routerapi/backend/internal/ratelimit"
	"github.com/routerapi/backend/pkg/config"
	"github.com/routerapi/backend/pkg/jwt"
	"gorm.io/gorm"
)

func New(db *gorm.DB, cfg *config.Config, bal *balancer.Balancer, limiter *ratelimit.Limiter) *gin.Engine {
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	if bal == nil {
		bal = balancer.New(db)
	}
	if limiter == nil {
		limiter = ratelimit.New()
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.CORS())
	// 请求体上限要挂在任何会读 body 的逻辑之前：客户端可以自称 1KB 然后一直喂。
	r.Use(middleware.BodyLimit(cfg.Server.MaxRequestBodyBytes))
	// 可信代理需在任何取 ClientIP 的逻辑之前配置
	middleware.ConfigureTrustedProxies(r, cfg.TrustedProxies)
	r.Use(middleware.RequestID())
	r.Use(middleware.RequestLogger())

	// 设置加密密钥
	handler.CryptoSetKey(cfg.EffectiveEncryptKey())

	// JWT 管理器
	jwtMgr := jwt.NewManager(cfg.Server.JWTSecret, cfg.Server.JWTExpireHours)

	// 处理器
	authH := handler.NewAuthHandler(db, jwtMgr, limiter, cfg.Security)
	vendorH := handler.NewVendorHandler(db)
	modelH := handler.NewModelHandler(db)
	apiKeyH := handler.NewAPIKeyHandler(db)
	logH := handler.NewLogHandler(db)
	userH := handler.NewUserHandler(db)
	walletH := handler.NewWalletHandler(db)
	auditH := handler.NewAuditHandler(db)
	redeemH := handler.NewRedeemHandler(db, limiter)
	proxyH := handler.NewProxyHandler(db, bal, limiter, cfg)
	leaderboardH := handler.NewLeaderboardHandler(db, cfg.Leaderboard)

	// ---- 公开 API ----
	api := r.Group("/api")
	{
		api.POST("/auth/login", authH.Login)
		// 全站用户排行榜：公开可读，仅暴露用户名与用量聚合（起始页展示用）
		api.GET("/leaderboard", leaderboardH.List)
	}

	// ---- 管理 API (JWT 认证) ----
	admin := r.Group("/api/admin", middleware.AuthAdmin(jwtMgr, db))
	{
		// 当前用户操作（所有角色）
		admin.GET("/profile", userH.GetProfile)
		admin.PUT("/profile", userH.UpdateProfile)
		admin.PUT("/profile/password", userH.ChangePassword)
		// 积分钱包：查看自己的余额与流水
		admin.GET("/wallet", walletH.GetWallet)
		// 兑换码：成员自助兑换，积分计入自己余额（所有角色可用）
		admin.POST("/redeem", redeemH.Redeem)

		// 只读接口（viewer 亦可访问，便于团队查看）
		admin.GET("/protocols", vendorH.Protocols)
		admin.GET("/vendors", vendorH.List)
		admin.GET("/vendors/:id", vendorH.Get)
		admin.GET("/vendors/:id/models", vendorH.DiscoverModels)
		admin.GET("/models", modelH.List)
		admin.GET("/models/:id", modelH.Get)
		admin.GET("/models/:id/bindings", modelH.ListBindings)
		admin.GET("/api-keys", apiKeyH.List)
		admin.GET("/logs", logH.List)
		admin.GET("/stats", logH.Stats)
		admin.GET("/stats/usage", logH.Usage)

		// 写操作（仅 admin）——修复越权：所有增删改统一挂 RequireAdmin；Audit 在前以同时捕获被拒尝试
		w := admin.Group("", middleware.Audit(db), middleware.RequireAdmin())
		{
			// 审计日志 + 统计重算（仅 admin）
			w.GET("/audit-logs", auditH.List)
			w.POST("/stats/recompute", logH.Recompute)

			// 用户管理
			w.GET("/users", userH.List)
			w.POST("/users", userH.Create)
			w.PUT("/users/:id", userH.Update)
			w.DELETE("/users/:id", userH.Delete)
			w.PUT("/users/:id/reset-password", userH.ResetPassword)
			// 积分充值/调整 + 全局流水（团队审计）
			w.POST("/users/:id/credits", walletH.AdjustCredits)
			w.GET("/credit-transactions", walletH.ListTransactions)

			// 兑换码（credits 发放路径）
			w.POST("/redeem-codes", redeemH.Create)
			w.GET("/redeem-codes", redeemH.List)
			w.POST("/redeem-codes/:id/disable", redeemH.Disable)
			w.DELETE("/redeem-codes/:id", redeemH.Delete)

			// 供应商
			w.POST("/vendors", vendorH.Create)
			w.PUT("/vendors/:id", vendorH.Update)
			w.DELETE("/vendors/:id", vendorH.Delete)
			w.POST("/vendors/:id/test", vendorH.Test)
			w.POST("/vendors/:id/enable", vendorH.Enable)
			w.POST("/vendors/:id/disable", vendorH.Disable)
			w.POST("/vendors/:id/import-models", modelH.Import)

			// 模型 + 绑定
			w.POST("/models", modelH.Create)
			w.PUT("/models/:id", modelH.Update)
			w.DELETE("/models/:id", modelH.Delete)
			w.POST("/models/:id/bindings", modelH.CreateBinding)
			w.PUT("/models/:id/bindings/:binding_id", modelH.UpdateBinding)
			w.DELETE("/models/:id/bindings/:binding_id", modelH.DeleteBinding)

			// API Key
			w.POST("/api-keys", apiKeyH.Create)
			w.PUT("/api-keys/:id", apiKeyH.Update)
			w.DELETE("/api-keys/:id", apiKeyH.Delete)
		}
	}

	// ---- 代理 API（多协议入站：OpenAI / Anthropic / Gemini） ----
	v1 := r.Group("/v1")
	{
		v1.POST("/chat/completions", proxyH.ChatCompletions) // OpenAI 入站
		v1.POST("/messages", proxyH.Messages)                // Anthropic 入站
		v1.GET("/models", proxyH.ListModels)
	}
	// Gemini 入站：/v1beta/models/{model}:generateContent | :streamGenerateContent
	v1beta := r.Group("/v1beta")
	{
		v1beta.POST("/models/*path", proxyH.GeminiGenerate)
	}

	// ---- 前端静态文件 (生产模式) ----
	distPath := cfg.FrontendDist
	if distPath != "" {
		if info, err := os.Stat(distPath); err == nil && info.IsDir() {
			indexPath := filepath.Join(distPath, "index.html")
			if _, err := os.Stat(indexPath); err == nil {
				r.Static("/assets", filepath.Join(distPath, "assets"))
				r.NoRoute(func(c *gin.Context) {
					c.File(indexPath)
				})
			}
		}
	}

	return r
}
