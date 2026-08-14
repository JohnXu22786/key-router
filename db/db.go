package db

import (
	"key-router/model"
	"log"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// Init opens or creates the SQLite database and runs migrations
func Init(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}

	dbPath := filepath.Join(dataDir, "key-router.db")
	// _busy_timeout: concurrent writers (parallel relays, health checker,
	// admin transactions) retry instead of failing with SQLITE_BUSY.
	// _journal_mode=WAL: readers don't block the writer.
	db, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return err
	}

	// One writer at a time for SQLite; serializes writes, avoids lock storms.
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Run migrations
	if err := db.AutoMigrate(
		&model.Provider{},
		&model.Key{},
		&model.ModelGroup{},
		&model.Route{},
		&model.Consumption{},
		&model.Pricing{},
		&model.Setting{},
	); err != nil {
		return err
	}

	// Migrate pricing from per-1K to per-1M rates. Older builds stored
	// prompt_per_1k etc. (USD per 1,000 tokens); the new schema uses
	// prompt_per_1m (USD per 1,000,000 tokens). AutoMigrate adds the new
	// columns but leaves the old ones in place, so copy the values ×1000 and
	// drop the old columns. Idempotent: once the old columns are gone this
	// does nothing.
	if err := migratePricingPer1KToPer1M(db); err != nil {
		return err
	}

	// Fold cached tokens into input_tokens for legacy Anthropic consumption
	// rows (one-time; see migrateAnthropicInputTokensOnce).
	if err := migrateAnthropicInputTokensOnce(db); err != nil {
		return err
	}

	// Seed default settings if not exist
	seedDefaults(db)

	DB = db
	return nil
}

// migratePricingPer1KToPer1M converts legacy per-1K pricing rows to the
// per-1M schema. SQLite cannot alter a column in place, so the migration is:
// detect old column -> copy each value ×1000 into the new column -> drop the
// old column. Runs inside one transaction; idempotent (old columns gone
// => no-op on subsequent launches).
func migratePricingPer1KToPer1M(db *gorm.DB) error {
	hasColumn := func(col string) bool {
		var n int
		db.Raw("SELECT COUNT(*) FROM pragma_table_info('pricings') WHERE name = ?", col).Scan(&n)
		return n > 0
	}

	// Newer schema already in place (or table brand new): nothing to do.
	if !hasColumn("prompt_per_1k") {
		return nil
	}
	if !hasColumn("prompt_per_1m") {
		// Shouldn't happen after AutoMigrate, but be safe.
		return db.Exec("ALTER TABLE pricings ADD COLUMN prompt_per_1m REAL DEFAULT 0").Error
	}

	log.Println("[db] migrating pricing rates from per-1K to per-1M tokens")
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	// Copy ×1000 into the new columns (per 1K → per 1M is ×1000).
	for _, pair := range [][2]string{
		{"prompt_per_1k", "prompt_per_1m"},
		{"completion_per_1k", "completion_per_1m"},
		{"cache_read_per_1k", "cache_read_per_1m"},
		{"cache_write_per_1k", "cache_write_per_1m"},
	} {
		oldCol, newCol := pair[0], pair[1]
		if !hasColumn(newCol) {
			if err := tx.Exec("ALTER TABLE pricings ADD COLUMN " + newCol + " REAL DEFAULT 0").Error; err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Exec("UPDATE pricings SET " + newCol + " = " + oldCol + " * 1000").Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	// Drop the old columns (SQLite supports DROP COLUMN since 3.35).
	for _, oldCol := range []string{"prompt_per_1k", "completion_per_1k", "cache_read_per_1k", "cache_write_per_1k"} {
		if err := tx.Exec("ALTER TABLE pricings DROP COLUMN " + oldCol).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	if err := tx.Commit().Error; err != nil {
		return err
	}
	log.Println("[db] pricing migration complete (per-1K → per-1M)")
	return nil
}

// GetDB returns the database instance
func GetDB() *gorm.DB {
	return DB
}

// migrationInputTokensInclCache is the settings flag that gates
// migrateAnthropicInputTokensOnce: it must run exactly once, because rows
// recorded after this build already include cached tokens in input_tokens
// (RecordConsumption folds them) and a second pass would double-count.
const migrationInputTokensInclCache = "migration.input_tokens_incl_cache"

// migrateAnthropicInputTokensOnce brings legacy consumption rows in line
// with the uniform "input_tokens includes cached tokens" convention
// (RecordConsumption folds cache reads + writes for anthropic-format usage).
// Rows recorded by older builds under anthropic-type providers stored input
// EXCLUDING cached tokens, which made the UI cache-hit rate
// (cached / input_tokens) read 0% for fully-cached requests and over 100%
// otherwise. The fold adds cache_hit + cache_write for those rows exactly
// once; rows under openai-type providers already include cached tokens and
// are left untouched. Runs at startup inside a transaction, before any
// request is served; no-op once the flag is set.
//
// Keyed by each key's CURRENT provider type: consumption rows store no
// usage format, so rows recorded before a provider was re-typed (or a key
// moved between providers) are indistinguishable from new ones — skipped
// rows then read above 100% (the frontend rate clamps to 100) and
// over-folded rows read low. Rows whose key or provider was deleted since
// are skipped too.
func migrateAnthropicInputTokensOnce(db *gorm.DB) error {
	var n int64
	if err := db.Model(&model.Setting{}).Where("key = ? AND value = '1'", migrationInputTokensInclCache).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	log.Println("[db] folding cached tokens into input_tokens for legacy Anthropic consumption rows")
	// Fold and flag write are ONE transaction: a crash or error mid-way
	// leaves the flag unset, so the next launch retries WITHOUT having
	// double-counted any rows.
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE consumptions
			SET input_tokens = input_tokens + cache_hit_tokens + cache_write_tokens
			WHERE key_id IN (
				SELECT k.id FROM keys k JOIN providers p ON p.id = k.provider_id
				WHERE p.type = 'anthropic'
			)`).Error; err != nil {
			return err
		}
		return tx.Create(&model.Setting{Key: migrationInputTokensInclCache, Value: "1"}).Error
	})
	return err
}

// seedDefaults inserts default settings if they don't exist
func seedDefaults(db *gorm.DB) {
	defaults := map[string]string{
		model.SettingPort:        model.DefaultPort,
		model.SettingAuthToken:   model.DefaultAuthToken,
		model.SettingRetryTimes:  model.DefaultRetryTimes,
		model.SettingHealthCheck: model.DefaultHealthCheck,
	}

	for k, v := range defaults {
		var count int64
		if err := db.Model(&model.Setting{}).Where("key = ?", k).Count(&count).Error; err != nil {
			log.Printf("[db] failed to check setting %s: %v", k, err)
			continue
		}
		if count == 0 {
			if err := db.Create(&model.Setting{Key: k, Value: v}).Error; err != nil {
				log.Printf("[db] failed to create default setting %s: %v", k, err)
			}
		}
	}
}

// GetSetting retrieves a setting value
func GetSetting(key string) string {
	s, _ := GetSettingChecked(key)
	return s
}

// GetSettingChecked retrieves a setting value, distinguishing "unset" from a
// DB read error (callers must fail closed on error)
func GetSettingChecked(key string) (string, error) {
	var s model.Setting
	if err := DB.Where("key = ?", key).First(&s).Error; err != nil {
		return "", err
	}
	return s.Value, nil
}

// SetSetting updates a setting value
func SetSetting(key, value string) error {
	// Atomic upsert — avoids the FirstOrCreate SELECT+INSERT race where
	// concurrent writes to a new key would collide on the primary key.
	return DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"value": value}),
	}).Create(&model.Setting{Key: key, Value: value}).Error
}
