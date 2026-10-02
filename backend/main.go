package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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

// 监听侧超时。
//
// 不设 WriteTimeout 是有意的：流式补全本来就是分钟级响应，任何有限的 WriteTimeout
// 都会在每个 SSE 请求半路把它切断。慢写保护由"读侧有界 + 请求体有上限 + 上游客户端超时"
// 三件事共同兜住，不靠 WriteTimeout。
const (
	readHeaderTimeout = 20 * time.Second // Slowloris：请求头没在这段时间内读完就不该占着连接
	readTimeout       = 5 * time.Minute  // 覆盖"慢链路上传几十 MB 多模态 body"的最坏情况
	idleTimeout       = 2 * time.Minute  // keep-alive 空闲连接回收
	shutdownDrainMax  = 90 * time.Second // 停机时排空在途请求的最长等待
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

	// 鉴权密钥体检放在最前：用错这两把钥匙的症状是"上线后被攻击时才发现"，
	// 只有启动这一刻能便宜地拦住。
	if chk := cfg.CheckAuthKeys(); len(chk.Fatal) > 0 || len(chk.Warn) > 0 {
		for _, msg := range chk.Warn {
			logrus.Warn(msg)
		}
		if len(chk.Fatal) > 0 {
			for _, msg := range chk.Fatal {
				logrus.Error(msg)
			}
			if cfg.Server.Mode == "release" {
				logrus.Fatal("release 模式拒绝以不安全的鉴权密钥启动；修好配置再来，或临时用 server.mode=debug 本地排障")
			}
			logrus.Error("（debug 模式下继续启动；对外运营前必须修掉以上问题）")
		}
	}

	// SQLite 需确保数据目录存在（PostgreSQL 无本地文件）
	if cfg.Database.Driver == "" || cfg.Database.Driver == "sqlite" {
		_ = os.MkdirAll(filepath.Dir(cfg.Database.Path), 0755)
	}

	// 初始化数据库
	db, err := database.Init(cfg.Database)
	if err != nil {
		logrus.Fatalf("初始化数据库失败: %v", err)
	}
	// 注册得最早，因此跑得最晚：必须晚于在途请求排空与后台协程停止，
	// 否则结算/日志写入会撞上"database is closed"。
	defer database.Close(db)

	// 自动迁移 & 旧数据迁移 & 创建默认数据
	if err := database.AutoMigrate(db); err != nil {
		logrus.Fatalf("数据库迁移失败: %v", err)
	}
	if err := database.MigrateLegacy(db); err != nil {
		logrus.Errorf("旧数据迁移失败: %v", err)
	}
	// 存量明文 API Key → 只存摘要 + 脱敏，并删掉明文列。
	// 这一步失败必须拒绝启动：模型已经不读明文列了，带着未回填的库上线
	// 表现是"老 Key 静默全部失效"，比启动失败难查得多。
	if err := database.MigrateAPIKeyHashes(db); err != nil {
		logrus.Fatalf("API Key 哈希化迁移失败: %v", err)
	}
	// 配了独立的 encrypt_key 之后，把用 jwt_secret 封的存量供应商凭证重封一遍。
	if key := cfg.Server.EncryptKey; key != "" && key != cfg.Server.JWTSecret {
		if _, err := database.ReencryptVendorSecrets(db, cfg.Server.JWTSecret, key); err != nil {
			logrus.Fatalf("重封供应商凭证失败: %v", err)
		}
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
	if err := serve(r, cfg.Server.Port); err != nil {
		logrus.Fatalf("服务异常退出: %v", err)
	}
	// serve 返回即表示已停止接新连接、在途请求已排空（或已超时强关）。
	// 后面的 defer 依次收掉限流清理、健康探测、日志/预留清扫，最后关数据库连接池。
	logrus.Info("已停止监听，正在收掉后台任务")
}

// serve 启动带超时的 HTTP 服务，收到 SIGINT/SIGTERM 后优雅停机。
func serve(handler http.Handler, port int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// stop() 之后进程不再捕获这两个信号：第二次 Ctrl+C 走系统默认行为直接终止。
	// 这是给运维的逃生口——排空预算再长也不该变成"关不掉"。
	shutdown := make(chan struct{})
	go func() {
		<-ctx.Done()
		stop()
		close(shutdown)
	}()

	srv := newHTTPServer(handler, port)
	logrus.Infof("服务监听于 %s", srv.Addr)
	return runServer(srv, shutdown, shutdownDrainMax)
}

// newHTTPServer 集中放监听侧的超时配置，便于单独断言（尤其"WriteTimeout 必须为 0"这条）。
func newHTTPServer(handler http.Handler, port int) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		// WriteTimeout 刻意留 0：流式补全是分钟级响应，任何有限值都会在半路切断 SSE。
	}
}

// runServer 跑到 shutdown 被关闭，然后在 drainBudget 内排空在途请求。
//
// 优雅的含义只有两条：不再接新连接，以及在预算内等在途请求写完。
// 预算用满仍没写完的请求会被强关——它们留下的预留由进程重启后的 TTL 清扫器回收。
func runServer(srv *http.Server, shutdown <-chan struct{}, drainBudget time.Duration) error {
	serveErr := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		// ListenAndServe 自己就返回了（端口被占/权限不足），没有可停的东西。
		return err
	case <-shutdown:
	}

	logrus.Warnf("收到退出信号：%s 内不再接新请求，正在排空在途请求", drainBudget)
	drain, cancel := context.WithTimeout(context.Background(), drainBudget)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		logrus.Warnf("排空超时，强制关闭剩余连接: %v", err)
		_ = srv.Close()
	}
	return <-serveErr
}
