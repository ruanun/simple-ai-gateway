// 一次性运维工具：渠道密钥（AES key）轮换迁移。
//
// 场景：历史上以「内置默认凭证」启动过的库，accounts.api_key_enc 是用默认
// AES key 加密的。切换到强随机 AES key 后，旧密文将无法解密。本工具用旧 key
// 解密、再用 config 里的新 key 重新加密写回，从而在换 key 的同时保留密钥池数据；
// 并可选地把管理员口令从默认弱口令重置为 config.admin.password（护栏只查
// config、不查库，故弱口令 hash 需在此一并清除）。
//
// 用法：
//
//	go run ./tools/migrate-key -c config.local.yaml            # 默认旧 key=内置默认 key，并重置 admin 口令
//	go run ./tools/migrate-key -c config.local.yaml -old <旧key> -reset-admin=false
//
// 全程事务包裹：任一步失败整体回滚，数据不动。可安全重跑（已迁移的账号自动跳过）。
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/ruanun/simple-ai-gateway/internal/config"
	"github.com/ruanun/simple-ai-gateway/internal/crypto"
	"github.com/ruanun/simple-ai-gateway/internal/db"
	"github.com/ruanun/simple-ai-gateway/internal/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// 内置默认 AES key，与 internal/config 的 defaultAESKey 同值。
// 以零配置默认启动的库，其 api_key_enc 就是用它加密的。
const defaultAESKey = "0123456789abcdef0123456789abcdef"

func main() {
	cfgPath := flag.String("c", "config.local.yaml", "配置文件路径（提供新 AES key 与 admin 口令）")
	oldKey := flag.String("old", defaultAESKey, "旧 AES key（默认=内置默认 key）")
	resetAdmin := flag.Bool("reset-admin", true, "把管理员口令重置为 config.admin.password 并使旧令牌失效")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	newKey := cfg.Security.ChannelKeyAESKey
	switch len(newKey) {
	case 16, 24, 32:
	default:
		log.Fatalf("新 AES key 长度非法 %d，必须为 16/24/32 字节", len(newKey))
	}
	if newKey == *oldKey {
		log.Fatalf("新旧 AES key 相同，无需迁移（config 里的 channel_key_aes_key 仍是旧值？）")
	}

	g, err := db.Open(cfg.Database)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}

	var migrated, emptyN, already int
	err = g.Transaction(func(tx *gorm.DB) error {
		var accs []model.Account
		if err := tx.Find(&accs).Error; err != nil {
			return err
		}
		for i := range accs {
			acc := &accs[i]
			if acc.APIKeyEnc == "" {
				emptyN++
				continue
			}
			// 先用旧 key 解；失败则尝试新 key（幂等重跑：已迁移的账号自动跳过）。
			plain, err := crypto.Decrypt(*oldKey, acc.APIKeyEnc)
			if err != nil {
				if _, e2 := crypto.Decrypt(newKey, acc.APIKeyEnc); e2 == nil {
					already++
					continue
				}
				return fmt.Errorf("账号 #%d(%s) 用旧 key 解密失败，且非新 key 加密：%w", acc.ID, acc.Name, err)
			}
			enc, err := crypto.Encrypt(newKey, plain)
			if err != nil {
				return fmt.Errorf("账号 #%d 重新加密失败：%w", acc.ID, err)
			}
			// 用 struct 更新，交给 GORM 映射列名（避开 APIKeyEnc→api_key_enc 手写列名的坑）。
			if err := tx.Model(&model.Account{}).Where("id = ?", acc.ID).
				Updates(model.Account{APIKeyEnc: enc}).Error; err != nil {
				return err
			}
			migrated++
		}

		if *resetAdmin {
			hash, err := bcrypt.GenerateFromPassword([]byte(cfg.Admin.Password), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("生成 admin 口令 hash 失败：%w", err)
			}
			// 仅重置 password_hash：本工具场景下 jwt.secret 已一并换新，所有旧 JWT
			// 会因验签失败而自动失效，无需再 bump token_version（且旧 schema 可能尚无该列）。
			res := tx.Model(&model.AdminUser{}).Where("username = ?", cfg.Admin.Username).
				Update("password_hash", string(hash))
			if res.Error != nil {
				return res.Error
			}
			log.Printf("已重置管理员 %q 口令（影响 %d 行；jwt.secret 换新后旧令牌自动失效）",
				cfg.Admin.Username, res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		log.Fatalf("迁移事务失败（已回滚，数据未改动）：%v", err)
	}

	// 验证：新 key 必须能解密所有非空账号，否则视为迁移失败。
	var accs []model.Account
	if err := g.Find(&accs).Error; err != nil {
		log.Fatalf("验证读取失败: %v", err)
	}
	var bad, total int
	for i := range accs {
		if accs[i].APIKeyEnc == "" {
			continue
		}
		total++
		if _, err := crypto.Decrypt(newKey, accs[i].APIKeyEnc); err != nil {
			bad++
			log.Printf("⚠ 账号 #%d(%s) 迁移后仍无法用新 key 解密", accs[i].ID, accs[i].Name)
		}
	}
	if bad > 0 {
		log.Fatalf("验证失败：%d/%d 个账号无法用新 key 解密", bad, total)
	}
	fmt.Printf("迁移完成：重加密 %d 个账号，空密文跳过 %d，已是新 key %d；验证通过（%d 个账号均可用新 key 解密）。\n",
		migrated, emptyN, already, total)
}
