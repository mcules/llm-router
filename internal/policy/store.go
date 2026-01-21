package policy

import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Upsert(ctx context.Context, p ModelPolicy) error {
	return s.UpsertPolicy(ctx, p)
}

func (s *Store) ListAll(ctx context.Context) ([]ModelPolicy, error) {
	return s.ListPolicies(ctx)
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS model_policies (
  model_id TEXT PRIMARY KEY,
  ram_required_bytes INTEGER NOT NULL DEFAULT 0,
  ttl_secs INTEGER NOT NULL DEFAULT 0,
  pinned INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 0
);
`)
	return err
}

func (s *Store) UpsertPolicy(ctx context.Context, p ModelPolicy) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO model_policies(model_id, ram_required_bytes, ttl_secs, pinned, priority)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(model_id) DO UPDATE SET
  ram_required_bytes=excluded.ram_required_bytes,
  ttl_secs=excluded.ttl_secs,
  pinned=excluded.pinned,
  priority=excluded.priority;
`, p.ModelID, p.RAMRequiredBytes, p.TTLSecs, boolToInt(p.Pinned), p.Priority)
	return err
}

func (s *Store) GetPolicy(ctx context.Context, modelID string) (ModelPolicy, bool, error) {
	if s.db == nil {
		return ModelPolicy{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT model_id, ram_required_bytes, ttl_secs, pinned, priority
FROM model_policies WHERE model_id=?;
`, modelID)

	var p ModelPolicy
	var pinnedInt int
	err := row.Scan(&p.ModelID, &p.RAMRequiredBytes, &p.TTLSecs, &pinnedInt, &p.Priority)
	if err == sql.ErrNoRows {
		return ModelPolicy{}, false, nil
	}
	if err != nil {
		return ModelPolicy{}, false, err
	}
	p.Pinned = pinnedInt != 0
	return p, true, nil
}

func (s *Store) ListPolicies(ctx context.Context) ([]ModelPolicy, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT model_id, ram_required_bytes, ttl_secs, pinned, priority
FROM model_policies
ORDER BY model_id ASC;
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ModelPolicy
	for rows.Next() {
		var p ModelPolicy
		var pinnedInt int
		if err := rows.Scan(&p.ModelID, &p.RAMRequiredBytes, &p.TTLSecs, &pinnedInt, &p.Priority); err != nil {
			return nil, err
		}
		p.Pinned = pinnedInt != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
