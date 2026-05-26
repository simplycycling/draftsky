package auth

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is a PostgreSQL-backed implementation of oauth.ClientAuthStore.
// It survives server restarts, unlike the in-memory MemStore.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore returns a PGStore backed by the given connection pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

var _ oauth.ClientAuthStore = (*PGStore)(nil)

func (s *PGStore) GetSession(ctx context.Context, did syntax.DID, sessionID string) (*oauth.ClientSessionData, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM oauth_sessions WHERE did = $1 AND session_id = $2`,
		string(did), sessionID,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("oauth session not found (did=%s sid=%s): %w", did, sessionID, err)
	}
	var sess oauth.ClientSessionData
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("unmarshal oauth session: %w", err)
	}
	return &sess, nil
}

func (s *PGStore) SaveSession(ctx context.Context, sess oauth.ClientSessionData) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal oauth session: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO oauth_sessions (did, session_id, data)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (did, session_id) DO UPDATE SET data = EXCLUDED.data`,
		string(sess.AccountDID), sess.SessionID, raw,
	)
	return err
}

func (s *PGStore) DeleteSession(ctx context.Context, did syntax.DID, sessionID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM oauth_sessions WHERE did = $1 AND session_id = $2`,
		string(did), sessionID,
	)
	return err
}

func (s *PGStore) GetAuthRequestInfo(ctx context.Context, state string) (*oauth.AuthRequestData, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM oauth_auth_requests WHERE state = $1`,
		state,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("oauth auth request not found (state=%s): %w", state, err)
	}
	var info oauth.AuthRequestData
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("unmarshal oauth auth request: %w", err)
	}
	return &info, nil
}

func (s *PGStore) SaveAuthRequestInfo(ctx context.Context, info oauth.AuthRequestData) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal oauth auth request: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO oauth_auth_requests (state, data) VALUES ($1, $2)`,
		info.State, raw,
	)
	if err != nil {
		return fmt.Errorf("save auth request (state=%s): %w", info.State, err)
	}
	return nil
}

func (s *PGStore) DeleteAuthRequestInfo(ctx context.Context, state string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM oauth_auth_requests WHERE state = $1`,
		state,
	)
	return err
}
