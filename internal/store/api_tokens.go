package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// APIToken is a long-lived, non-interactive credential for scripted API
// access. Unlike a session it is not tied to a browser cookie or CSRF token
// and is authenticated via an Authorization: Bearer header instead.
type APIToken struct {
	ID         int64
	Name       string
	TokenHash  []byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

func (s *Store) CreateAPIToken(
	ctx context.Context,
	name string,
	tokenHash []byte,
	expiresAt time.Time,
) (APIToken, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (name, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?)
	`, name, tokenHash, now.Unix(), expiresAt.UTC().Unix())
	if err != nil {
		return APIToken{}, fmt.Errorf("create api token: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return APIToken{}, fmt.Errorf("read created api token id: %w", err)
	}
	return APIToken{
		ID:        id,
		Name:      name,
		TokenHash: tokenHash,
		CreatedAt: now,
		ExpiresAt: expiresAt.UTC(),
	}, nil
}

func (s *Store) APITokenByHash(ctx context.Context, tokenHash []byte) (APIToken, error) {
	return scanAPIToken(s.db.QueryRowContext(ctx, `
		SELECT id, name, token_hash, created_at, expires_at, last_used_at
		FROM api_tokens
		WHERE token_hash = ?
	`, tokenHash))
}

func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, token_hash, created_at, expires_at, last_used_at
		FROM api_tokens
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()

	var tokens []APIToken
	for rows.Next() {
		token, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	return tokens, nil
}

func (s *Store) TouchAPIToken(ctx context.Context, id int64, usedAt time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens SET last_used_at = ? WHERE id = ?
	`, usedAt.UTC().Unix(), id); err != nil {
		return fmt.Errorf("touch api token: %w", err)
	}
	return nil
}

func (s *Store) DeleteAPIToken(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM api_tokens WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read api token delete result: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteExpiredAPITokens(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM api_tokens WHERE expires_at <= ?", now.UTC().Unix()); err != nil {
		return fmt.Errorf("delete expired api tokens: %w", err)
	}
	return nil
}

func scanAPIToken(row rowScanner) (APIToken, error) {
	var token APIToken
	var createdAt int64
	var expiresAt int64
	var lastUsedAt sql.NullInt64
	err := row.Scan(
		&token.ID,
		&token.Name,
		&token.TokenHash,
		&createdAt,
		&expiresAt,
		&lastUsedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("scan api token: %w", err)
	}
	token.CreatedAt = time.Unix(createdAt, 0).UTC()
	token.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	if lastUsedAt.Valid {
		t := time.Unix(lastUsedAt.Int64, 0).UTC()
		token.LastUsedAt = &t
	}
	return token, nil
}
