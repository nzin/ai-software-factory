package catalog

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/nzin/ai-software-factory/internal/modelext"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store is the GORM-backed persistence layer for the catalog.
type Store struct {
	db *gorm.DB
}

// Open opens (and migrates) a SQLite database at dsn. Use
// "file::memory:?cache=shared" for tests.
func Open(dsn string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(withPragmas(dsn)), &gorm.Config{
		Logger:  logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{LogLevel: logger.Warn, IgnoreRecordNotFoundError: true}),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite serializes writers; agents register concurrently at startup, so
	// keep a single connection and let busy_timeout (in the DSN) absorb waits.
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(&agentRow{}, &modelConfigRow{}); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// withPragmas appends busy_timeout + WAL to a file DSN so concurrent writers
// wait rather than failing with SQLITE_BUSY. In-memory / already-parameterised
// DSNs are left alone.
func withPragmas(dsn string) string {
	if strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory") || strings.Contains(dsn, "_pragma=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Upsert creates or updates an agent registration. The model configuration is
// only written when the agent is new, unless forceModel is true. It always
// returns the effective (stored) model configuration.
func (s *Store) Upsert(ctx context.Context, a Agent, mc modelext.Config, forceModel bool) (Agent, modelext.Config, error) {
	if a.Role == "" {
		return Agent{}, modelext.Config{}, errors.New("catalog: role is required")
	}
	now := time.Now().UTC()
	var out Agent
	var outMC modelext.Config

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing agentRow
		findErr := tx.Take(&existing, "role = ?", a.Role).Error
		isNew := errors.Is(findErr, gorm.ErrRecordNotFound)
		if findErr != nil && !isNew {
			return findErr
		}

		row := agentRow{
			Role:        a.Role,
			Name:        a.Name,
			Description: a.Description,
			BaseURL:     a.BaseURL,
			Transport:   a.Transport,
			Enabled:     a.Enabled,
			Concurrency: a.Concurrency,
			Skills:      a.Skills,
			UpdatedAt:   now,
		}
		if row.Transport == "" {
			row.Transport = TransportJSONRPC
		}
		if row.Concurrency <= 0 {
			row.Concurrency = 1
		}
		if isNew {
			row.CreatedAt = now
		} else {
			row.CreatedAt = existing.CreatedAt
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		out = row.toDomain()

		var mcRow modelConfigRow
		mcErr := tx.Take(&mcRow, "role = ?", a.Role).Error
		mcMissing := errors.Is(mcErr, gorm.ErrRecordNotFound)
		if mcErr != nil && !mcMissing {
			return mcErr
		}
		if mcMissing || forceModel {
			eff := mc.WithDefaults()
			mcRow = modelConfigRow{
				Role:      a.Role,
				Provider:  eff.Provider,
				Model:     eff.Model,
				MaxTokens: eff.MaxTokens,
				Effort:    eff.Effort,
				Thinking:  eff.Thinking,
				Params:    eff.Params,
				UpdatedAt: now,
			}
			if err := tx.Save(&mcRow).Error; err != nil {
				return err
			}
		}
		outMC = mcRowToConfig(mcRow)
		return nil
	})
	if err != nil {
		return Agent{}, modelext.Config{}, err
	}
	return out, outMC, nil
}

// Get returns one agent and its model configuration.
func (s *Store) Get(ctx context.Context, role string) (Agent, modelext.Config, error) {
	var row agentRow
	if err := s.db.WithContext(ctx).Take(&row, "role = ?", role).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Agent{}, modelext.Config{}, ErrNotFound
		}
		return Agent{}, modelext.Config{}, err
	}
	mc, err := s.GetModel(ctx, role)
	if err != nil {
		return Agent{}, modelext.Config{}, err
	}
	return row.toDomain(), mc, nil
}

// GetModel returns just the model configuration for a role.
func (s *Store) GetModel(ctx context.Context, role string) (modelext.Config, error) {
	var agent agentRow
	if err := s.db.WithContext(ctx).Take(&agent, "role = ?", role).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelext.Config{}, ErrNotFound
		}
		return modelext.Config{}, err
	}
	var row modelConfigRow
	if err := s.db.WithContext(ctx).Take(&row, "role = ?", role).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelext.Defaults(role).WithDefaults(), nil
		}
		return modelext.Config{}, err
	}
	return mcRowToConfig(row), nil
}

// List returns registered agents, optionally filtered by a skill tag and/or the
// enabled flag.
func (s *Store) List(ctx context.Context, skill string, enabled *bool) ([]Agent, error) {
	var rows []agentRow
	q := s.db.WithContext(ctx).Order("role")
	if enabled != nil {
		q = q.Where("enabled = ?", *enabled)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(rows))
	for _, r := range rows {
		a := r.toDomain()
		if skill != "" && !containsFold(a.Skills, skill) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// PatchModel applies a partial update to an agent's model configuration.
func (s *Store) PatchModel(ctx context.Context, role string, p ModelPatch) (modelext.Config, error) {
	var out modelext.Config
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var agent agentRow
		if err := tx.Take(&agent, "role = ?", role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var row modelConfigRow
		if err := tx.Take(&row, "role = ?", role).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			base := modelext.Defaults(role).WithDefaults()
			row = modelConfigRow{
				Role: role, Provider: base.Provider, Model: base.Model,
				MaxTokens: base.MaxTokens, Effort: base.Effort, Thinking: base.Thinking,
			}
		}
		if p.Provider != nil {
			row.Provider = *p.Provider
		}
		if p.Model != nil {
			row.Model = *p.Model
		}
		if p.MaxTokens != nil {
			row.MaxTokens = *p.MaxTokens
		}
		if p.Effort != nil {
			row.Effort = *p.Effort
		}
		if p.Thinking != nil {
			row.Thinking = *p.Thinking
		}
		if p.Params != nil {
			row.Params = p.Params
		}
		row.UpdatedAt = time.Now().UTC()
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		out = mcRowToConfig(row)
		return nil
	})
	return out, err
}

// Delete removes an agent and its model configuration.
func (s *Store) Delete(ctx context.Context, role string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&agentRow{}, "role = ?", role)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.Delete(&modelConfigRow{}, "role = ?", role).Error
	})
}

func mcRowToConfig(r modelConfigRow) modelext.Config {
	return modelext.Config{
		Provider:  r.Provider,
		Model:     r.Model,
		MaxTokens: r.MaxTokens,
		Effort:    r.Effort,
		Thinking:  r.Thinking,
		Params:    r.Params,
	}.WithDefaults()
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}
