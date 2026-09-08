// Package runstore provides a GORM/SQLite implementation of coordinator.Store so
// runs survive a coordinator restart. Wired from cmd/coordinator via --runstore.
package runstore

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/nzin/ai-software-factory/internal/coordinator"
)

type runRow struct {
	ID        string `gorm:"primaryKey"`
	Status    string
	Stage     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Doc       []byte // the whole coordinator.Run, JSON-encoded
}

func (runRow) TableName() string { return "runs" }

type store struct {
	db *gorm.DB
}

// Open opens (and migrates) a SQLite run store at dsn.
func Open(dsn string) (coordinator.Store, error) {
	pragmas := dsn
	if !strings.Contains(dsn, ":memory:") && !strings.Contains(dsn, "_pragma=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		pragmas = dsn + sep + "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	db, err := gorm.Open(sqlite.Open(pragmas), &gorm.Config{
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
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&runRow{}); err != nil {
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) Put(r *coordinator.Run) error {
	doc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	row := runRow{
		ID:        r.ID,
		Status:    string(r.Status),
		Stage:     r.Stage,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
		Doc:       doc,
	}
	return s.db.Save(&row).Error
}

func (s *store) Get(id string) (*coordinator.Run, bool, error) {
	var row runRow
	if err := s.db.Take(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return decode(row.Doc)
}

func (s *store) Delete(id string) error {
	return s.db.Delete(&runRow{}, "id = ?", id).Error
}

func (s *store) All() ([]*coordinator.Run, error) {
	var rows []runRow
	if err := s.db.Order("created_at desc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*coordinator.Run, 0, len(rows))
	for _, row := range rows {
		r, ok, err := decode(row.Doc)
		if err != nil || !ok {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func decode(doc []byte) (*coordinator.Run, bool, error) {
	var r coordinator.Run
	if err := json.Unmarshal(doc, &r); err != nil {
		return nil, false, err
	}
	return &r, true, nil
}
