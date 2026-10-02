package database

import (
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DefaultAdminCredits 首次创建的默认管理员积分余额（积分制，不挂钩现实货币）。
// 给一个较大初值，保证系统开箱可用；成员积分由管理员分配。
const DefaultAdminCredits int64 = 1_000_000_000

// Close 关闭底层连接池。SQLite 靠它把 WAL 正常收尾，PostgreSQL 靠它归还连接。
// 调用点必须晚于所有还在写库的协程与在途请求，否则结算/日志写入会撞上 "sql: database is closed"。
func Close(db *gorm.DB) {
	if db == nil {
		return
	}
	sqlDB, err := db.DB()
	if err != nil {
		logrus.Warnf("取底层连接池失败，跳过关闭: %v", err)
		return
	}
	if err := sqlDB.Close(); err != nil {
		logrus.Warnf("关闭数据库连接失败: %v", err)
	}
}

// Init 依据配置初始化数据库连接（支持 SQLite 与 PostgreSQL）。
func Init(cfg config.DBConfig) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	gormCfg := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	}

	switch cfg.Driver {
	case "postgres", "postgresql":
		dsn := cfg.BuildPostgresDSN()
		db, err = gorm.Open(postgres.Open(dsn), gormCfg)
		if err == nil {
			logrus.Info("数据库连接成功 (PostgreSQL)")
		}
	default:
		db, err = gorm.Open(sqlite.Open(withSQLitePragmas(cfg.Path)), gormCfg)
		if err == nil {
			logrus.Info("数据库连接成功 (SQLite)")
		}
	}
	if err != nil {
		return nil, err
	}
	return db, nil
}

// withSQLitePragmas 给 SQLite DSN 附加必需的 PRAGMA。
//
// busy_timeout：请求路径上存在并发写事务（预留/结算/供应商状态/日志），
// 而 SQLite 默认拿不到写锁就立刻返回 SQLITE_BUSY，会把锁冲突直接暴露成线上 500。
// journal_mode(WAL)：让读不阻塞写。内存库不支持 WAL，SQLite 会自行回退成 memory，无副作用。
// 调用方已显式提供 query 参数时原样返回，不覆盖其配置。
func withSQLitePragmas(path string) string {
	if path == "" || strings.Contains(path, "?") {
		return path
	}
	return path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
}

// AutoMigrate 创建/更新所有表结构。
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.User{},
		&model.Vendor{},
		&model.Model{},
		&model.ModelBinding{},
		&model.APIKey{},
		&model.RequestLog{},
		&model.CreditTransaction{},
		&model.QuotaReservation{},
		&model.RedemptionCode{},
		&model.AuditLog{},
		&model.StatData{},
	)
}

// ============ 旧表 -> 新表 数据迁移 ============

// 旧结构（仅用于一次性迁移读取，映射到旧表名）
type legacyProvider struct {
	ID         uint
	Name       string
	BaseURL    string
	Protocol   string
	AuthType   string
	AuthHeader string
	AuthPrefix string
}

func (legacyProvider) TableName() string { return "providers" }

type legacyChannel struct {
	ID         uint
	Name       string
	ProviderID uint
	APIKey     string
	BaseURL    string
	Weight     int
	Status     int
	Priority   int
	MaxRetry   int
	MaxRPM     int
	LastError  string
	LastUsedAt *time.Time
}

func (legacyChannel) TableName() string { return "channels" }

type legacyModelMapping struct {
	ID            uint
	VirtualModel  string
	ChannelID     uint
	UpstreamModel string
	Params        string
}

func (legacyModelMapping) TableName() string { return "model_mappings" }

// MigrateLegacy 将旧的 channels/providers/model_mappings 数据迁移到 vendors/models/model_bindings。
// 幂等：仅当 vendors 表为空且旧 channels 表存在时执行；保留渠道 ID 作为供应商 ID。
func MigrateLegacy(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable("channels") || !m.HasTable("providers") || !m.HasTable("model_mappings") {
		return nil // 无旧表，全新安装
	}
	var vendorCount int64
	db.Model(&model.Vendor{}).Count(&vendorCount)
	if vendorCount > 0 {
		return nil // 已迁移过
	}
	var channelCount int64
	db.Table("channels").Count(&channelCount)
	if channelCount == 0 {
		return nil // 旧库为空，无需迁移
	}

	logrus.Info("检测到旧数据，开始迁移 channels/providers/model_mappings -> vendors/models/model_bindings ...")

	// 1) providers 映射表（id -> preset）
	var providers []legacyProvider
	db.Table("providers").Find(&providers)
	provByID := make(map[uint]legacyProvider, len(providers))
	for _, p := range providers {
		provByID[p.ID] = p
	}

	// 2) channels -> vendors（保留原 ID）
	var channels []legacyChannel
	db.Table("channels").Find(&channels)
	for _, ch := range channels {
		p := provByID[ch.ProviderID]
		protocol := p.Protocol
		if protocol == "" {
			protocol = model.ProtocolOpenAI
		}
		baseURL := ch.BaseURL
		if baseURL == "" {
			baseURL = p.BaseURL
		}
		status := ch.Status
		if status == -1 {
			status = 1 // 旧的“异常”状态改为启用，交由健康探测管理
		}
		v := model.Vendor{
			ID: ch.ID, Name: ch.Name, Protocol: protocol, BaseURL: baseURL,
			APIKey: ch.APIKey, AuthType: p.AuthType, AuthHeader: p.AuthHeader, AuthPrefix: p.AuthPrefix,
			Weight: ch.Weight, Priority: ch.Priority, Status: status,
			MaxRetry: ch.MaxRetry, MaxRPM: ch.MaxRPM, LastError: ch.LastError, LastUsedAt: ch.LastUsedAt,
		}
		if v.Weight <= 0 {
			v.Weight = 1
		}
		if err := db.Create(&v).Error; err != nil {
			return err
		}
	}
	// 3) model_mappings -> models + model_bindings
	var mappings []legacyModelMapping
	db.Table("model_mappings").Find(&mappings)
	modelIDByName := make(map[string]uint)
	for _, mm := range mappings {
		if mm.VirtualModel == "" {
			continue
		}
		mid, ok := modelIDByName[mm.VirtualModel]
		if !ok {
			newModel := model.Model{Name: mm.VirtualModel, DisplayName: mm.VirtualModel, Enabled: true}
			if err := db.Create(&newModel).Error; err != nil {
				return err
			}
			mid = newModel.ID
			modelIDByName[mm.VirtualModel] = mid
		}
		// 绑定权重/优先级沿用对应供应商的默认值
		var v model.Vendor
		weight, priority := 1, 0
		if err := db.First(&v, mm.ChannelID).Error; err == nil {
			weight, priority = v.Weight, v.Priority
		}
		if weight <= 0 {
			weight = 1
		}
		binding := model.ModelBinding{
			ModelID: mid, VendorID: mm.ChannelID, UpstreamModel: mm.UpstreamModel,
			Params: mm.Params, Weight: weight, Priority: priority, Enabled: true,
		}
		if err := db.Create(&binding).Error; err != nil {
			return err
		}
	}

	logrus.Infof("旧数据迁移完成：%d 个供应商，%d 个模型，%d 条绑定", len(channels), len(modelIDByName), len(mappings))
	return nil
}

// SeedAdmin 首次启动创建默认管理员（含初始积分）。
func SeedAdmin(db *gorm.DB, username, password string) error {
	var count int64
	db.Model(&model.User{}).Count(&count)
	if count > 0 {
		return nil
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	user := model.User{
		Username: username,
		Password: string(hashed),
		Role:     model.RoleAdmin,
		Status:   1,
		Credits:  DefaultAdminCredits,
	}
	if err := db.Create(&user).Error; err != nil {
		return err
	}

	logrus.Info("默认管理员已创建")
	return nil
}

// EnsureAdminCredits 为旧库中“零积分且无任何流水”的管理员补足初始积分。
// 场景：Credits 列由 AutoMigrate 后加，早于该列存在的 admin 会得到 0，
// 启用钱包门槛后将被锁死。仅当该用户从未产生任何积分流水时补足，
// 保证幂等且不误伤被手动清零的账户。
func EnsureAdminCredits(db *gorm.DB) error {
	var admins []model.User
	if err := db.Where("role = ? AND credits = 0", model.RoleAdmin).Find(&admins).Error; err != nil {
		return err
	}
	for _, a := range admins {
		var txnCount int64
		db.Model(&model.CreditTransaction{}).Where("user_id = ?", a.ID).Count(&txnCount)
		if txnCount > 0 {
			continue // 已有流水：说明被手动清零或已计费，不补
		}
		if err := db.Model(&model.User{}).Where("id = ?", a.ID).
			Update("credits", DefaultAdminCredits).Error; err != nil {
			return err
		}
		logrus.Infof("为管理员 %s 补足初始积分 %d", a.Username, DefaultAdminCredits)
	}
	return nil
}
