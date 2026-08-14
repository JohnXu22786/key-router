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

	// Migrate legacy consumption rows whose model_name holds the upstream
	// TARGET model back to the model the client requested (the model group
	// id). One-time, marker-gated; see migrateConsumptionModelToIngress.
	if err := migrateConsumptionModelToIngress(db); err != nil {
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

// migrateConsumptionModelToIngress migrates consumption rows recorded by
// older builds, whose model_name held the upstream TARGET model (the name the
// relay substituted into the forwarded request). Since the model-by-ingress
// change, model_name stores the model the CLIENT requested (the model group
// id) — the Activity page groups by it. This migration rewrites legacy rows
// to the new format using the route configuration as the reverse mapping.
//
// Ambiguity rules (best-effort: rows are only rewritten when the CURRENT
// route configuration can attribute them with certainty):
//   - a row is remapped only when its key's provider has EXACTLY ONE group
//     targeting that model (the same target via two providers resolves per
//     provider, so each provider's rows stay correct);
//   - rows whose key was deleted fall back to the globally-unique mapping;
//   - a target name that equals ANY live group's GroupID is never rewritten:
//     rows carrying that name may be either legacy target-format rows or
//     already-correct ingress rows (a pass-through group's id, or a chain
//     where one group's id is another group's target). The two are
//     indistinguishable, so all such rows stay untouched — rewriting them
//     could mis-attribute already-correct rows;
//   - rows with no unique mapping (two groups on the same provider targeting
//     the same model, or a name that is not a route target — e.g. pass-through
//     models, which already stored the ingress name) are left untouched;
//   - attribution is computed from the CURRENT configuration: rows whose
//     routes or groups were deleted since recording may be attributed to a
//     surviving group targeting the same name (unavoidable without per-row
//     provenance). In particular, the id of a DELETED pass-through group is
//     not protected by the live-group-id rule, so its already-correct rows
//     can be relabeled by a live group whose route targets that id.
//     Unmigrated rows keep showing the upstream name on the Activity page for
//     the life of the database; they are deliberately NOT re-processed later
//     (see below).
//
// One-time: the completion marker (a settings row) is written in the same
// transaction, so a re-run — which would corrupt post-migration rows whose
// ingress name happens to equal another group's target — never happens.
// The marker insert is conflict-tolerant (OnConflict DoNothing) so a second
// app instance racing the first at startup cannot fail on the unique key.
// Idempotent and atomic (single transaction), runs at launch before the
// server accepts requests. Rows written by a still-running PRE-UPGRADE
// instance after this migration commits are not remapped (the marker gates
// any later run) — restart after upgrading is expected.
func migrateConsumptionModelToIngress(db *gorm.DB) error {
	var n int64
	if err := db.Model(&model.Setting{}).Where("key = ?", model.SettingConsumptionModelSource).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	// groupID: model group id -> its public group id (the ingress model name).
	var groups []model.ModelGroup
	if err := db.Find(&groups).Error; err != nil {
		return err
	}
	groupID := make(map[int64]string, len(groups))
	liveGroupIDs := make(map[string]bool, len(groups))
	for i := range groups {
		groupID[groups[i].ID] = groups[i].GroupID
		liveGroupIDs[groups[i].GroupID] = true
	}

	// byProvider[providerID][targetModel] = set of group ids targeting it on
	// that provider; byTarget[targetModel] = group ids across ALL providers
	// (for rows whose key no longer exists).
	byProvider := make(map[int64]map[string]map[string]bool)
	byTarget := make(map[string]map[string]bool)
	var routes []model.Route
	if err := db.Find(&routes).Error; err != nil {
		return err
	}
	for i := range routes {
		r := &routes[i]
		if r.TargetModel == "" {
			continue // pass-through: rows already carry the ingress name
		}
		gid, ok := groupID[r.ModelGroupID]
		if !ok {
			continue // group deleted: its rows cannot be attributed anymore
		}
		if byProvider[r.ProviderID] == nil {
			byProvider[r.ProviderID] = make(map[string]map[string]bool)
		}
		if byProvider[r.ProviderID][r.TargetModel] == nil {
			byProvider[r.ProviderID][r.TargetModel] = make(map[string]bool)
		}
		byProvider[r.ProviderID][r.TargetModel][gid] = true
		if byTarget[r.TargetModel] == nil {
			byTarget[r.TargetModel] = make(map[string]bool)
		}
		byTarget[r.TargetModel][gid] = true
	}

	// Compute the remap statements before opening the transaction so a
	// failure leaves nothing half-applied.
	type remap struct {
		providerID int64 // 0 = orphan rows (key deleted), globally-unique mapping
		groupID    string
		target     string
	}
	solo := func(set map[string]bool) (string, bool) {
		if len(set) != 1 {
			return "", false
		}
		for g := range set {
			return g, true
		}
		return "", false
	}
	// A remap whose target equals a live group id is never emitted: the rows
	// matching it may already be correct (see the doc comment), and skipping
	// it also makes remaps order-independent (no chain where one remap's
	// groupID is another remap's target).
	remappable := func(target string, groups map[string]bool) (string, bool) {
		if liveGroupIDs[target] {
			return "", false
		}
		return solo(groups)
	}
	var remaps []remap
	for providerID, byTargetPerProvider := range byProvider {
		for target, groups := range byTargetPerProvider {
			if gid, ok := remappable(target, groups); ok {
				remaps = append(remaps, remap{providerID, gid, target})
			}
		}
	}
	for target, groups := range byTarget {
		if gid, ok := remappable(target, groups); ok {
			remaps = append(remaps, remap{0, gid, target})
		}
	}
	if len(remaps) == 0 {
		// Nothing to rewrite (fresh install or no target-mapped routes): just
		// set the marker so later installs of legacy rows are not reprocessed.
		return db.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.Setting{Key: model.SettingConsumptionModelSource, Value: "ingress"}).Error
	}

	log.Printf("[db] migrating consumption model names from upstream target to ingress model (%d target(s))", len(remaps))
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

	for _, rm := range remaps {
		// Rows with a live key are restricted to the key's provider; orphan
		// rows (providerID 0) fall back to the globally-unique mapping.
		if rm.providerID != 0 {
			if err := tx.Exec(
				"UPDATE consumptions SET model_name = ? WHERE model_name = ? AND key_id IN (SELECT id FROM keys WHERE provider_id = ?)",
				rm.groupID, rm.target, rm.providerID,
			).Error; err != nil {
				tx.Rollback()
				return err
			}
		} else if err := tx.Exec(
			"UPDATE consumptions SET model_name = ? WHERE model_name = ? AND key_id NOT IN (SELECT id FROM keys)",
			rm.groupID, rm.target,
		).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	// Mark the migration done in the same transaction so a crash cannot
	// leave the data half-remapped with the marker set. DoNothing: a second
	// app instance that raced the first through the marker check must not
	// fail on the unique key (its UPDATEs are no-ops by then).
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.Setting{Key: model.SettingConsumptionModelSource, Value: "ingress"}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	log.Println("[db] consumption model migration complete (target → ingress)")
	return nil
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
