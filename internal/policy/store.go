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

func (s *Store) Delete(ctx context.Context, modelID string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM model_policies WHERE model_id=?;", modelID)
	return err
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

CREATE TABLE IF NOT EXISTS api_keys (
  key_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  hashed_key TEXT NOT NULL,
  created_at DATETIME NOT NULL,
  last_used_at DATETIME,
  allowed_nodes TEXT NOT NULL DEFAULT '',
  allowed_models TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS users (
  username TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL,
  allowed_nodes TEXT NOT NULL DEFAULT '',
  allowed_models TEXT NOT NULL DEFAULT ''
);
`)
	return err
}

type APIKeyRecord struct {
	ID            string
	Name          string
	Prefix        string
	HashedKey     string
	CreatedAt     time.Time
	LastUsedAt    *time.Time
	AllowedNodes  string
	AllowedModels string
}

type UserRecord struct {
	Username      string
	PasswordHash  string
	AllowedNodes  string
	AllowedModels string
}

func (s *Store) CreateAPIKey(ctx context.Context, record APIKeyRecord) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO api_keys(key_id, name, prefix, hashed_key, created_at, allowed_nodes, allowed_models)
VALUES(?, ?, ?, ?, ?, ?, ?);
`, record.ID, record.Name, record.Prefix, record.HashedKey, record.CreatedAt, record.AllowedNodes, record.AllowedModels)
	return err
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT key_id, name, prefix, hashed_key, created_at, last_used_at, allowed_nodes, allowed_models
FROM api_keys ORDER BY created_at DESC;
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var r APIKeyRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.Prefix, &r.HashedKey, &r.CreatedAt, &r.LastUsedAt, &r.AllowedNodes, &r.AllowedModels); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) GetAPIKey(ctx context.Context, id string) (APIKeyRecord, bool, error) {
	if s.db == nil {
		return APIKeyRecord{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT key_id, name, prefix, hashed_key, created_at, last_used_at, allowed_nodes, allowed_models
FROM api_keys WHERE key_id=?;
`, id)
	var r APIKeyRecord
	err := row.Scan(&r.ID, &r.Name, &r.Prefix, &r.HashedKey, &r.CreatedAt, &r.LastUsedAt, &r.AllowedNodes, &r.AllowedModels)
	if err == sql.ErrNoRows {
		return APIKeyRecord{}, false, nil
	}
	if err != nil {
		return APIKeyRecord{}, false, err
	}
	return r, true, nil
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE key_id=?;", id)
	return err
}

func (s *Store) UpdateAPIKeyLastUsed(ctx context.Context, id string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at=? WHERE key_id=?;", time.Now(), id)
	return err
}

func (s *Store) CreateUser(ctx context.Context, u UserRecord) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users(username, password_hash, allowed_nodes, allowed_models)
VALUES(?, ?, ?, ?);
`, u.Username, u.PasswordHash, u.AllowedNodes, u.AllowedModels)
	return err
}

func (s *Store) GetUser(ctx context.Context, username string) (UserRecord, bool, error) {
	if s.db == nil {
		return UserRecord{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, "SELECT username, password_hash, allowed_nodes, allowed_models FROM users WHERE username=?;", username)
	var u UserRecord
	err := row.Scan(&u.Username, &u.PasswordHash, &u.AllowedNodes, &u.AllowedModels)
	if err == sql.ErrNoRows {
		return UserRecord{}, false, nil
	}
	if err != nil {
		return UserRecord{}, false, err
	}
	return u, true, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]UserRecord, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT username, password_hash, allowed_nodes, allowed_models FROM users ORDER BY username ASC;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRecord
	for rows.Next() {
		var u UserRecord
		if err := rows.Scan(&u.Username, &u.PasswordHash, &u.AllowedNodes, &u.AllowedModels); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

func (s *Store) DeleteUser(ctx context.Context, username string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE username=?;", username)
	return err
}

func (s *Store) UpdateUser(ctx context.Context, u UserRecord) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE users SET allowed_nodes=?, allowed_models=? WHERE username=?;
`, u.AllowedNodes, u.AllowedModels, u.Username)
	return err
}

func (s *Store) UpdateUserPassword(ctx context.Context, username, passwordHash string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "UPDATE users SET password_hash=? WHERE username=?;", passwordHash, username)
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
