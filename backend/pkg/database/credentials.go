package database

import (
	"fmt"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// legacyAPIKeyRow 只用于读取升级前的 api_keys 表：那个版本把调用方密钥的**明文**
// 存在 key 列里。迁移完之后该列会被删掉，所以这里用一张独立的最小结构，
// 不借道 model.APIKey（它已经没有明文字段了）。
type legacyAPIKeyRow struct {
	ID      uint
	Key     string
	KeyHash string
}

func (legacyAPIKeyRow) TableName() string { return "api_keys" }

// tableHasColumn 查真实表结构里有没有这一列。
//
// 不能用 db.Migrator().HasColumn(&model.APIKey{}, "key")：新模型的 Key 已经是
// gorm:"-" 的瞬态字段，拿模型去问列存在性会答错，而这条判断正是迁移的幂等门。
func tableHasColumn(db *gorm.DB, table, column string) bool {
	types, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		return false // 表还不存在
	}
	for _, ct := range types {
		if ct.Name() == column {
			return true
		}
	}
	return false
}

// MigrateAPIKeyHashes 把存量明文 API Key 迁成"只存 SHA-256 + 脱敏形态"，然后删掉明文列。
//
// 幂等：明文列不存在（全新安装或已迁过）就直接返回；单行已有 key_hash 就跳过。
// 顺序不能反：先回填再删列，否则删完就没有可派生摘要的原文了。
func MigrateAPIKeyHashes(db *gorm.DB) error {
	if !tableHasColumn(db, "api_keys", "key") {
		return nil
	}

	var rows []legacyAPIKeyRow
	if err := db.Table("api_keys").Select("id", "key", "key_hash").Find(&rows).Error; err != nil {
		return err
	}

	var hashed, disabled int
	for _, r := range rows {
		if r.KeyHash != "" {
			continue
		}
		if r.Key == "" {
			// 没有原文可派生摘要：这行永远不可能被任何请求匹配上。
			// 留着它只是让授权列表里多一条"看着存在、其实没人能用"的记录，禁用掉。
			if err := db.Model(&model.APIKey{}).Where("id = ?", r.ID).Update("status", 0).Error; err != nil {
				return err
			}
			disabled++
			continue
		}
		updates := map[string]interface{}{
			"key_hash":   crypto.HashSecret(r.Key),
			"key_masked": crypto.MaskString(r.Key),
		}
		if err := db.Model(&model.APIKey{}).Where("id = ?", r.ID).Updates(updates).Error; err != nil {
			return err
		}
		hashed++
	}

	// 明文列的使命到此结束。整列删掉，而不是留一个名叫 key、装着空串的死列——
	// 那种列迟早被人重新利用起来。
	//
	// 两条语句都同时合法于 SQLite(>=3.35) 与 PostgreSQL，不需要按方言分支：
	//   - DROP INDEX IF EXISTS：索引名是 GORM 对字段级 uniqueIndex 的默认命名。
	//     SQLite 不允许丢掉仍被索引引用的列（它会在校验依赖索引时回滚整条语句），
	//     所以必须先删；PostgreSQL 的 DROP COLUMN 本来就会连带删除依赖它的索引，
	//     这一句在 PG 上即便名字对不上也只是 IF EXISTS 空转，不会有副作用。
	//   - ALTER TABLE ... DROP COLUMN：两边同形。
	if err := db.Exec("DROP INDEX IF EXISTS idx_api_keys_key").Error; err != nil {
		logrus.Warnf("删除 api_keys.key 唯一索引失败: %v", err)
	}
	if err := db.Exec("ALTER TABLE api_keys DROP COLUMN key").Error; err != nil {
		// 老 SQLite 不支持 DROP COLUMN。退一步至少保证"库里不再有可用凭证"：清空。
		logrus.Warnf("删除 api_keys.key 列失败，改为清空该列内容: %v", err)
		if err := db.Exec("UPDATE api_keys SET key = ''").Error; err != nil {
			logrus.Warnf("清空 api_keys.key 失败: %v", err)
		}
	}

	// DDL 说了不算，回读一次表结构：万一某个方言/版本两条路都没走通，
	// 明文凭据就还在库里，而"迁移已完成"的日志会替它掩盖这件事。
	if tableHasColumn(db, "api_keys", "key") {
		var left int64
		db.Table("api_keys").Where("key <> ''").Count(&left)
		if left > 0 {
			// 只有"列还在且里面还有值"才是真风险，报错误级别并给出可执行的手工语句。
			logrus.Errorf("api_keys.key 明文列仍存在且有 %d 行非空值，自动迁移未成功。"+
				"请人工确认并执行：ALTER TABLE api_keys DROP COLUMN key", left)
			return fmt.Errorf("api_keys.key 明文列未能移除，仍有 %d 行明文凭据", left)
		}
		logrus.Warn("api_keys.key 明文列仍在，但内容已清空（无凭据泄露风险），可择机手工 DROP COLUMN")
	}
	logrus.Infof("API Key 已迁为哈希存储：回填 %d 条，禁用无原文的 %d 条", hashed, disabled)
	return nil
}

// ReencryptVendorSecrets 在加密密钥与 JWT 密钥分开之后，把存量供应商凭证重封一遍。
//
// 不做这件事的后果很具体：老库里所有 api_key 都是用 jwt_secret 派生的密钥封的，
// 一旦生效密钥换成 encrypt_key，升级后每家供应商都解不开自己的凭证——
// 表现是全站 502，而配置一个字都没错。
//
// 返回被重封的行数。已经能用新密钥解开的行跳过，因此可重复执行。
// 两种密钥都解不开的行保持原样并报错：宁可用不起来等人工处理，
// 也不能拿一个猜出来的明文去覆盖真实凭证。
func ReencryptVendorSecrets(db *gorm.DB, oldKey, newKey string) (int, error) {
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return 0, nil
	}

	var rows []struct {
		ID     uint
		APIKey string
	}
	if err := db.Model(&model.Vendor{}).Where("api_key <> ''").Find(&rows).Error; err != nil {
		return 0, err
	}

	resealed := 0
	for _, r := range rows {
		if _, err := crypto.DecryptWith(newKey, r.APIKey); err == nil {
			continue
		}
		plain, err := crypto.DecryptWith(oldKey, r.APIKey)
		if err != nil {
			logrus.Errorf("供应商[%d] 的凭证既不能用新密钥也不能用旧密钥解开，保持原样：请在管理端重填该密钥", r.ID)
			continue
		}
		sealed, err := crypto.EncryptWith(newKey, plain)
		if err != nil {
			return resealed, err
		}
		if err := db.Model(&model.Vendor{}).Where("id = ?", r.ID).Update("api_key", sealed).Error; err != nil {
			return resealed, err
		}
		resealed++
	}
	if resealed > 0 {
		logrus.Infof("已用新的加密密钥重封 %d 条供应商凭证", resealed)
	}
	return resealed, nil
}
