package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/routerapi/backend/internal/balancer"
	"github.com/routerapi/backend/internal/cleanup"
	"github.com/routerapi/backend/internal/health"
	"github.com/routerapi/backend/internal/ratelimit"
	"github.com/routerapi/backend/internal/router"
	"github.com/routerapi/backend/pkg/config"
	"github.com/routerapi/backend/pkg/database"
	"github.com/routerapi/backend/pkg/logger"

	"github.com/sirupsen/logrus"
)

func main() {
	// 加载配置
	cfg, err := config.Load("config.yaml")
	if err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化日志
	logger.Init(cfg.Server.Mode == "release")
	logrus.Info("RouterAPI 正在启动...")

	// SQLite 需确保数据目录存在（PostgreSQL 无本地文件）
	if cfg.Database.Driver == "" || cfg.Database.Driver == "sqlite" {
		_ = os.MkdirAll(filepath.Dir(cfg.Database.Path), 0755)
	}

	// 初始化数据库
	db, err := database.Init(cfg.Database)
	if err != nil {
		logrus.Fatalf("初始化数据库失败: %v", err)
	}

	// 自动迁移 & 旧数据迁移 & 创建默认数据
	if err := database.AutoMigrate(db); err != nil {
		logrus.Fatalf("数据库迁移失败: %v", err)
	}
	if err := database.MigrateLegacy(db); err != nil {
		logrus.Errorf("旧数据迁移失败: %v", err)
	}
	if err := database.SeedAdmin(db, cfg.Admin.Username, cfg.Admin.Password); err != nil {
		logrus.Errorf("创建默认管理员失败: %v", err)
	}
	if err := database.EnsureAdminCredits(db); err != nil {
		logrus.Errorf("补足管理员初始积分失败: %v", err)
	}

	// 负载均衡器 + 限流器
	bal := balancer.New(db)
	limiter := ratelimit.New()
	stop := make(chan struct{})
	defer close(stop)
	limiter.StartCleanup(stop)

	// 后台健康探测（熔断自动恢复）
	if cfg.Health.Enabled {
		monitor := health.NewMonitor(db, bal, cfg.Health.IntervalMinutes)
		monitor.Start()
		defer monitor.Stop()
	}

	// 后台日志清理（按保留天数分批删除过期调用日志）
	cleanupMon := cleanup.NewMonitor(db, cfg.LogRetentionDays)
	cleanupMon.Start()
	defer cleanupMon.Stop()

	// 启动 HTTP 服务
	r := router.New(db, cfg, bal, limiter)
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logrus.Infof("服务监听于 %s", addr)
	if err := r.Run(addr); err != nil {
		logrus.Fatalf("启动服务失败: %v", err)
	}
}
