package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/crypto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 这套用例刻意在 SQLite 与 PostgreSQL 上各跑一遍。
//
// 理由不是"多一个引擎更热闹"：删索引 + 删列这两条 DDL 在两个方言上的行为并不对称
// （SQLite 会拒绝删除仍被索引引用的列，PostgreSQL 的 DROP COLUMN 反而会自动连带删索引），
// "同一条语句两边都收敛"是需要被证明的结论，不是可以从 SQL 标准推出来的推论。
const (
	envPostgresTests = "POSTGRES_TESTS" // =1 时用本地默认连接参数跑
	envPostgresDSN   = "POSTGRES_DSN"   // 完整 DSN，优先级更高
)

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("开 SQLite 失败: %v", err)
	}
	// Windows 上文件句柄没释放会让 t.TempDir 的清理失败（连带整个用例判负）。
	t.Cleanup(func() { Close(db) })
	return db
}

// schemaSeq 保证并发/重跑时临时 schema 不重名。
var schemaSeq int64

// openPostgres 在一个一次性 schema 里跑，跑完 CASCADE 删掉。
// 直接借用现成 database 会把别人的数据当测试品，所以不这么干。
func openPostgres(t *testing.T) (*gorm.DB, bool) {
	t.Helper()
	dsn := os.Getenv(envPostgresDSN)
	switch {
	case dsn != "":
	case os.Getenv(envPostgresTests) == "1":
		dsn = "host=localhost port=5432 user=postgres password= dbname=postgres sslmode=disable"
	default:
		return nil, false
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("连 PostgreSQL 失败（设了 %s/%s 却连不上就是真问题，别当跳过）: %v",
			envPostgresDSN, envPostgresTests, err)
	}
	if err := db.Exec("SELECT 1").Error; err != nil {
		Close(db)
		t.Fatalf("PostgreSQL 连接不可用: %v", err)
	}

	schema := fmt.Sprintf("routerapi_t_%d_%d", os.Getpid(), atomic.AddInt64(&schemaSeq, 1))
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		Close(db)
		t.Fatalf("建临时 schema 失败: %v", err)
	}
	Close(db)

	// search_path 走连接参数，不能用 SET：连接池里每条物理连接各自为政。
	withSchema := dsn
	if strings.Contains(withSchema, "?") {
		withSchema += "&search_path=" + schema
	} else {
		withSchema += " search_path=" + schema
	}
	scoped, err := gorm.Open(postgres.Open(withSchema), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("带 search_path 重连失败: %v", err)
	}
	t.Cleanup(func() {
		if err := scoped.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("清理临时 schema %s 失败: %v", schema, err)
		}
		Close(scoped)
	})
	return scoped, true
}

// legacyAPIKeysDDL 是鉴权改造之前的 api_keys：明文 key 列 + 唯一索引。
// 只有这一张表需要按方言分开写（自增主键语法不同），迁移逻辑本身是共用的。
var legacyAPIKeysDDL = map[string]string{
	"sqlite": `CREATE TABLE api_keys (
		id INTEGER PRIMARY KEY, name TEXT, key TEXT NOT NULL,
		status INTEGER DEFAULT 1, owner_user_id INTEGER)`,
	"postgres": `CREATE TABLE api_keys (
		id BIGSERIAL PRIMARY KEY, name VARCHAR(128), key VARCHAR(128) NOT NULL,
		status INTEGER DEFAULT 1, owner_user_id BIGINT)`,
}

const legacyAPIKeysIndex = `CREATE UNIQUE INDEX idx_api_keys_key ON api_keys(key)`

// seedLegacyAPIKeys 造一张"改造前"的表并插入两把明文 Key。
func seedLegacyAPIKeys(t *testing.T, db *gorm.DB, dialect string) (k1, k2 string) {
	t.Helper()
	ddl, ok := legacyAPIKeysDDL[dialect]
	if !ok {
		t.Fatalf("没有 %s 的旧表 DDL", dialect)
	}
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("建旧表失败: %v", err)
	}
	if err := db.Exec(legacyAPIKeysIndex).Error; err != nil {
		t.Fatalf("建旧表唯一索引失败: %v", err)
	}
	k1, k2 = "ra-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ra-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// 不指定 id：两个方言的自增语义都能接上，也免得 PG 序列与显式 id 打架
	if err := db.Exec(`INSERT INTO api_keys (name, key, status, owner_user_id) VALUES (?, ?, 1, 1)`,
		"one", k1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO api_keys (name, key, status, owner_user_id) VALUES (?, ?, 1, 1)`,
		"two", k2).Error; err != nil {
		t.Fatal(err)
	}
	return k1, k2
}

// 升级路径的核心：老库带着明文列必须能自动迁成摘要，且 AutoMigrate 不能因为
// "给非空表加带唯一索引的列"而失败——多个 NULL 在两方言里都算互不相同，
// 但这条假设值得钉住，因为它一旦不成立，升级就直接起不来。
func TestMigrateAPIKeyHashesFromPlaintextSchema(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := targetDB(t, dialect)
			k1, k2 := seedLegacyAPIKeys(t, db, dialect)

			if err := AutoMigrate(db); err != nil {
				t.Fatalf("在含明文的旧表上 AutoMigrate 失败（升级会直接起不来）: %v", err)
			}
			if err := MigrateAPIKeyHashes(db); err != nil {
				t.Fatalf("迁移失败: %v", err)
			}

			var keys []model.APIKey
			if err := db.Order("id asc").Find(&keys).Error; err != nil {
				t.Fatalf("读迁移结果失败: %v", err)
			}
			if len(keys) != 2 {
				t.Fatalf("行数 = %d, want 2", len(keys))
			}
			if keys[0].KeyHash != crypto.HashSecret(k1) || keys[1].KeyHash != crypto.HashSecret(k2) {
				t.Errorf("摘要不对: %q / %q", keys[0].KeyHash, keys[1].KeyHash)
			}
			for _, k := range keys {
				if k.KeyMasked == "" || !strings.Contains(k.KeyMasked, "*") {
					t.Errorf("脱敏形态没落库: %q", k.KeyMasked)
				}
				if strings.Contains(k.KeyMasked, k1[4:]) || strings.Contains(k.KeyMasked, k2[4:]) {
					t.Errorf("脱敏形态泄露了大部分明文: %q", k.KeyMasked)
				}
			}

			// 明文列必须真的不在了；留着它早晚被人再写进去
			if tableHasColumn(db, "api_keys", "key") {
				t.Error("明文列 api_keys.key 仍然存在")
			}
			// 迁移不能把用户已有的 Key 弄失效：拿原来的明文串按新查询形态仍要命中同一行
			var bySecret model.APIKey
			if err := db.Where("key_hash = ? AND status = 1", crypto.HashSecret(k1)).First(&bySecret).Error; err != nil {
				t.Errorf("升级后原来的明文 Key 不再可用: %v", err)
			}
			if bySecret.Name != "one" {
				t.Errorf("按原 Key 命中了错误的行: %q", bySecret.Name)
			}
			// 再跑一次必须无害（幂等）
			if err := MigrateAPIKeyHashes(db); err != nil {
				t.Errorf("重复执行迁移报错: %v", err)
			}
		})
	}
}

// 库里残留一条"整表扫一遍都找不到原文"的行时，禁用它。
// 留着的后果不是无害：一条 key_hash 为空的行永远匹配不上请求，
// 却会出现在授权列表里，看起来像一把能用的钥匙。
func TestMigrateAPIKeyHashesDisablesUnusableRows(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := targetDB(t, dialect)
			if err := db.Exec(legacyAPIKeysDDL[dialect]).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(legacyAPIKeysIndex).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`INSERT INTO api_keys (name, key, status, owner_user_id) VALUES (?, ?, 1, 1)`,
				"broken", "").Error; err != nil {
				t.Fatal(err)
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatalf("AutoMigrate 失败: %v", err)
			}
			if err := MigrateAPIKeyHashes(db); err != nil {
				t.Fatalf("迁移失败: %v", err)
			}

			var got model.APIKey
			if err := db.First(&got).Error; err != nil {
				t.Fatal(err)
			}
			if got.Status != 0 {
				t.Errorf("无原文的行 status = %d, want 0（禁用）", got.Status)
			}
			if got.KeyHash != "" {
				t.Errorf("无原文的行被凭空造出了摘要: %q", got.KeyHash)
			}
		})
	}
}

// 全新安装不该看到任何迁移动作。
func TestMigrateAPIKeyHashesOnFreshSchema(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := targetDB(t, dialect)
			if err := AutoMigrate(db); err != nil {
				t.Fatalf("AutoMigrate 失败: %v", err)
			}
			if err := MigrateAPIKeyHashes(db); err != nil {
				t.Fatalf("全新库上迁移失败: %v", err)
			}
			// 建一把新 Key 并确认能按摘要查回来（消费者侧唯一的查询形态）
			const secret = "ra-fresh0123456789abcdef0123456789abcdef"
			k := model.APIKey{Name: "n", KeyHash: crypto.HashSecret(secret), KeyMasked: "ra-f****cdef", Status: 1}
			if err := db.Create(&k).Error; err != nil {
				t.Fatalf("写入新 Key 失败: %v", err)
			}
			var found model.APIKey
			if err := db.Where("key_hash = ? AND status = 1", crypto.HashSecret(secret)).First(&found).Error; err != nil {
				t.Errorf("按摘要查不回新建的 Key: %v", err)
			}
		})
	}
}

// 换加密密钥必须把存量供应商凭证重封，否则升级后每家都解不开自己的 Key，
// 全站 502 而配置一个字都没错。
func TestReencryptVendorSecrets(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := targetDB(t, dialect)
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			const oldKey, newKey = "jwt-secret-old-0123456789abcdef", "encrypt-key-new-0123456789abcdef"
			const plain = "sk-upstream-secret"

			sealed, err := crypto.EncryptWith(oldKey, plain)
			if err != nil {
				t.Fatal(err)
			}
			v := model.Vendor{Name: "v", Protocol: model.ProtocolOpenAI, BaseURL: "http://x", APIKey: sealed, Status: 1}
			if err := db.Create(&v).Error; err != nil {
				t.Fatal(err)
			}

			n, err := ReencryptVendorSecrets(db, oldKey, newKey)
			if err != nil {
				t.Fatalf("重封失败: %v", err)
			}
			if n != 1 {
				t.Fatalf("重封行数 = %d, want 1", n)
			}

			var got model.Vendor
			if err := db.First(&got, v.ID).Error; err != nil {
				t.Fatal(err)
			}
			// 用新密钥必须能解开——router 里 CryptoSetKey(EffectiveEncryptKey()) 之后
			// balancer 的 Decrypt 走的就是这条路径（用 DecryptWith 等价断言，不动全局密钥）
			unlocked, err := crypto.DecryptWith(newKey, got.APIKey)
			if err != nil {
				t.Fatalf("用新密钥解不开重封后的凭证: %v", err)
			}
			if unlocked != plain {
				t.Errorf("明文变了: %q", unlocked)
			}
			if _, err := crypto.DecryptWith(oldKey, got.APIKey); err == nil {
				t.Error("旧密钥仍能解开，说明根本没有重封")
			}

			// 幂等：再跑一次不该重复处理
			again, err := ReencryptVendorSecrets(db, oldKey, newKey)
			if err != nil || again != 0 {
				t.Errorf("重复执行重封 = (%d, %v), want (0, nil)", again, err)
			}
		})
	}
}

// 两种密钥都解不开时保持原样，不拿猜出来的明文覆盖真实凭证。
func TestReencryptVendorSecretsKeepsUnknownCiphertext(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := targetDB(t, dialect)
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			v := model.Vendor{Name: "v", Protocol: model.ProtocolOpenAI, APIKey: "not-our-ciphertext", Status: 1}
			if err := db.Create(&v).Error; err != nil {
				t.Fatal(err)
			}
			n, err := ReencryptVendorSecrets(db, "key-a-0123456789abcdef0123456789ab", "key-b-0123456789abcdef0123456789ab")
			if err != nil {
				t.Fatalf("不该整体失败，只该跳过这一行: %v", err)
			}
			if n != 0 {
				t.Errorf("重封行数 = %d, want 0", n)
			}
			var got model.Vendor
			if err := db.First(&got, v.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.APIKey != "not-our-ciphertext" {
				t.Errorf("解不开的密文被改动了: %q", got.APIKey)
			}
		})
	}
}

// targetDB 按方言取库；PostgreSQL 未配置时跳过而不是失败。
// t.Skipf 会直接终止本子测试，所以调用方不需要再判一次"要不要跑"。
func targetDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	switch dialect {
	case "sqlite":
		return openSQLite(t)
	case "postgres":
		db, ok := openPostgres(t)
		if !ok {
			t.Skipf("跳过 PostgreSQL：设置 %s=1（用本地默认连接）或 %s=<DSN> 后重跑，"+
				"两条 DDL 在非对称方言上的收敛才算被验证过", envPostgresTests, envPostgresDSN)
		}
		return db
	}
	t.Fatalf("未知方言 %q", dialect)
	return nil
}
