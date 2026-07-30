package db

import (
	"log"
	"local-router/model"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// Init opens or creates the SQLite database and runs migrations
func Init(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}

	dbPath := filepath.Join(dataDir, "local-router.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return err
	}

	// Run migrations
	if err := db.AutoMigrate(
		&model.Provider{},
		&model.Key{},
		&model.ModelGroup{},
		&model.Route{},
		&model.WindowCounter{},
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
		model.SettingPort:          model.DefaultPort,
		model.SettingAuthToken:     model.DefaultAuthToken,
		model.SettingRetryTimes:    model.DefaultRetryTimes,
		model.SettingHealthCheck:   model.DefaultHealthCheck,
		model.SettingWindowPersist: model.DefaultWindowPersist,
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
	var s model.Setting
	if err := DB.Where("key = ?", key).First(&s).Error; err != nil {
		return ""
	}
	return s.Value
}

// SetSetting updates a setting value
func SetSetting(key, value string) error {
	return DB.Where("key = ?", key).Assign(model.Setting{Value: value}).FirstOrCreate(&model.Setting{Key: key}).Error
}
