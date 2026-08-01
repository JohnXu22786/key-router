package db

import (
	"local-router/model"
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

	dbPath := filepath.Join(dataDir, "local-router.db")
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

	// Seed default settings if not exist
	seedDefaults(db)

	DB = db
	return nil
}

// GetDB returns the database instance
func GetDB() *gorm.DB {
	return DB
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
