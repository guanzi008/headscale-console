package idp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrConsumed   = errors.New("already consumed")
	ErrExpired    = errors.New("expired")
	ErrForbidden  = errors.New("administrator permission required")
	ErrSelfDelete = errors.New("cannot delete the current administrator")
	ErrLastAdmin  = errors.New("cannot delete the last administrator")
)

type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	IsAdmin      bool
	Disabled     bool
}

type Identity struct {
	Provider  string
	Subject   string
	UserID    string
	Email     string
	CreatedAt time.Time
}

type AuthCode struct {
	CodeHash            string
	ClientID            string
	UserID              string
	RedirectURI         string
	Scope               string
	Nonce               string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

type OAuthState struct {
	State     string
	Provider  string
	UserID    string
	Next      string
	Verifier  string
	Nonce     string
	ExpiresAt time.Time
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open account database: %w", err)
	}
	db.SetMaxOpenConns(1)

	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure account database: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize account database: %w", err)
	}
	// Existing installations keep their accounts. The first registered account
	// becomes the platform administrator; subsequent accounts own their tailnets.
	if _, err := db.Exec(`INSERT OR IGNORE INTO account_states (user_id, is_admin, disabled)
		SELECT id, CASE WHEN id = (SELECT id FROM users ORDER BY created_at, rowid LIMIT 1)
		THEN 1 ELSE 0 END, 0 FROM users`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize account roles: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateUser(ctx context.Context, email, displayName, passwordHash string) (User, error) {
	now := time.Now().UTC()
	user := User{
		ID:           "usr_" + randomHex(12),
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		user.ID, user.Email, user.DisplayName, user.PasswordHash, user.CreatedAt.Unix(), user.UpdatedAt.Unix())
	if err != nil {
		if isUniqueError(err) {
			return User{}, ErrConflict
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_states (user_id, is_admin, disabled)
		SELECT ?, NOT EXISTS (SELECT 1 FROM account_states WHERE is_admin = 1), 0`, user.ID); err != nil {
		return User{}, fmt.Errorf("create account role: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT is_admin FROM account_states WHERE user_id = ?", user.ID).Scan(&user.IsAdmin); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

const userColumns = `u.id, u.email, u.display_name, u.password_hash, u.created_at, u.updated_at,
	a.is_admin, a.disabled`

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`
		FROM users u JOIN account_states a ON a.user_id = u.id WHERE u.email = ? COLLATE NOCASE`, email)
	return scanUser(row)
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`
		FROM users u JOIN account_states a ON a.user_id = u.id WHERE u.id = ?`, id)
	return scanUser(row)
}

func (s *Store) SetPassword(ctx context.Context, userID, passwordHash string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		passwordHash, time.Now().UTC().Unix(), userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at, created_at)
		SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM account_states WHERE user_id = ? AND disabled = 0)`, tokenHash, userID, expiresAt.Unix(), time.Now().UTC().Unix(), userID)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UserBySession(ctx context.Context, tokenHash string, now time.Time) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`
		FROM sessions s JOIN users u ON u.id = s.user_id JOIN account_states a ON a.user_id = u.id
		WHERE s.token_hash = ? AND s.expires_at > ? AND a.disabled = 0`, tokenHash, now.UTC().Unix())
	return scanUser(row)
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *Store) LinkIdentity(ctx context.Context, provider, subject, userID, email string) error {
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO identities (provider, subject, user_id, email, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		provider, subject, userID, email, now)
	if err != nil {
		if isUniqueError(err) {
			return ErrConflict
		}
		return fmt.Errorf("link identity: %w", err)
	}
	return nil
}

func (s *Store) IdentityBySubject(ctx context.Context, provider, subject string) (Identity, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT provider, subject, user_id, email, created_at
		FROM identities WHERE provider = ? AND subject = ?`, provider, subject)
	var identity Identity
	var created int64
	if err := row.Scan(&identity.Provider, &identity.Subject, &identity.UserID, &identity.Email, &created); err != nil {
		return Identity{}, normalizeNotFound(err)
	}
	identity.CreatedAt = time.Unix(created, 0).UTC()
	return identity, nil
}

func (s *Store) IdentityForUser(ctx context.Context, provider, userID string) (Identity, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT provider, subject, user_id, email, created_at
		FROM identities WHERE provider = ? AND user_id = ?`, provider, userID)
	var identity Identity
	var created int64
	if err := row.Scan(&identity.Provider, &identity.Subject, &identity.UserID, &identity.Email, &created); err != nil {
		return Identity{}, normalizeNotFound(err)
	}
	identity.CreatedAt = time.Unix(created, 0).UTC()
	return identity, nil
}

func (s *Store) ListIdentities(ctx context.Context, userID string) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider, subject, user_id, email, created_at
		FROM identities WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()
	var identities []Identity
	for rows.Next() {
		var identity Identity
		var created int64
		if err := rows.Scan(&identity.Provider, &identity.Subject, &identity.UserID, &identity.Email, &created); err != nil {
			return nil, fmt.Errorf("scan identity: %w", err)
		}
		identity.CreatedAt = time.Unix(created, 0).UTC()
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func (s *Store) UnlinkIdentity(ctx context.Context, provider, userID string) error {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM identities WHERE provider = ? AND user_id = ?", provider, userID)
	if err != nil {
		return fmt.Errorf("unlink identity: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateAuthCode(ctx context.Context, code AuthCode) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_codes (
			code_hash, client_id, user_id, redirect_uri, scope, nonce,
			code_challenge, code_challenge_method, expires_at, used, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		code.CodeHash, code.ClientID, code.UserID, code.RedirectURI, code.Scope,
		code.Nonce, code.CodeChallenge, code.CodeChallengeMethod,
		code.ExpiresAt.UTC().Unix(), time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("create authorization code: %w", err)
	}
	return nil
}

func (s *Store) ConsumeAuthCode(ctx context.Context, codeHash string, now time.Time) (AuthCode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthCode{}, fmt.Errorf("begin authorization code transaction: %w", err)
	}
	defer tx.Rollback()

	var code AuthCode
	var expires int64
	var used int
	err = tx.QueryRowContext(ctx, `
		SELECT code_hash, client_id, user_id, redirect_uri, scope, nonce,
			code_challenge, code_challenge_method, expires_at, used
		FROM auth_codes WHERE code_hash = ?`, codeHash).Scan(
		&code.CodeHash, &code.ClientID, &code.UserID, &code.RedirectURI,
		&code.Scope, &code.Nonce, &code.CodeChallenge, &code.CodeChallengeMethod,
		&expires, &used)
	if err != nil {
		return AuthCode{}, normalizeNotFound(err)
	}
	if used != 0 {
		return AuthCode{}, ErrConsumed
	}
	if expires <= now.UTC().Unix() {
		return AuthCode{}, ErrExpired
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE auth_codes SET used = 1 WHERE code_hash = ? AND used = 0", codeHash)
	if err != nil {
		return AuthCode{}, fmt.Errorf("consume authorization code: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return AuthCode{}, ErrConsumed
	}
	if err := tx.Commit(); err != nil {
		return AuthCode{}, fmt.Errorf("commit authorization code: %w", err)
	}
	code.ExpiresAt = time.Unix(expires, 0).UTC()
	return code, nil
}

func (s *Store) CreateAccessToken(ctx context.Context, tokenHash, userID, scope string, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO access_tokens (token_hash, user_id, scope, expires_at, created_at)
		SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM account_states WHERE user_id = ? AND disabled = 0)`,
		tokenHash, userID, scope, expiresAt.UTC().Unix(), time.Now().UTC().Unix(), userID)
	if err != nil {
		return fmt.Errorf("create access token: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UserByAccessToken(ctx context.Context, tokenHash string, now time.Time) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`
		FROM access_tokens t JOIN users u ON u.id = t.user_id JOIN account_states a ON a.user_id = u.id
		WHERE t.token_hash = ? AND t.expires_at > ? AND a.disabled = 0`, tokenHash, now.UTC().Unix())
	return scanUser(row)
}

func (s *Store) CreateOAuthState(ctx context.Context, state OAuthState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, user_id, next, verifier, nonce, expires_at)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?)`,
		state.State, state.Provider, state.UserID, state.Next, state.Verifier,
		state.Nonce, state.ExpiresAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("create OAuth state: %w", err)
	}
	return nil
}

func (s *Store) ConsumeOAuthState(ctx context.Context, state string, now time.Time) (OAuthState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OAuthState{}, fmt.Errorf("begin OAuth state transaction: %w", err)
	}
	defer tx.Rollback()

	var result OAuthState
	var userID sql.NullString
	var expires int64
	err = tx.QueryRowContext(ctx, `
		SELECT state, provider, user_id, next, verifier, nonce, expires_at
		FROM oauth_states WHERE state = ?`, state).Scan(
		&result.State, &result.Provider, &userID, &result.Next,
		&result.Verifier, &result.Nonce, &expires)
	if err != nil {
		return OAuthState{}, normalizeNotFound(err)
	}
	if expires <= now.UTC().Unix() {
		return OAuthState{}, ErrExpired
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_states WHERE state = ?", state); err != nil {
		return OAuthState{}, fmt.Errorf("consume OAuth state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OAuthState{}, fmt.Errorf("commit OAuth state: %w", err)
	}
	result.UserID = userID.String
	result.ExpiresAt = time.Unix(expires, 0).UTC()
	return result, nil
}

func (s *Store) Cleanup(ctx context.Context, now time.Time) error {
	for _, query := range []string{
		"DELETE FROM sessions WHERE expires_at <= ?",
		"DELETE FROM auth_codes WHERE expires_at <= ?",
		"DELETE FROM access_tokens WHERE expires_at <= ?",
		"DELETE FROM oauth_states WHERE expires_at <= ?",
	} {
		if _, err := s.db.ExecContext(ctx, query, now.UTC().Unix()); err != nil {
			return fmt.Errorf("clean account database: %w", err)
		}
	}
	return nil
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var user User
	var created, updated int64
	if err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &user.PasswordHash, &created, &updated, &user.IsAdmin, &user.Disabled); err != nil {
		return User{}, normalizeNotFound(err)
	}
	user.CreatedAt = time.Unix(created, 0).UTC()
	user.UpdatedAt = time.Unix(updated, 0).UTC()
	return user, nil
}

func normalizeNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func isUniqueError(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") || contains(err.Error(), "constraint failed"))
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE COLLATE NOCASE,
	display_name TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS identities (
	provider TEXT NOT NULL,
	subject TEXT NOT NULL,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	email TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (provider, subject),
	UNIQUE (user_id, provider)
);

CREATE TABLE IF NOT EXISTS account_states (
	user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
	disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1))
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_codes (
	code_hash TEXT PRIMARY KEY,
	client_id TEXT NOT NULL,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	redirect_uri TEXT NOT NULL,
	scope TEXT NOT NULL,
	nonce TEXT NOT NULL DEFAULT '',
	code_challenge TEXT NOT NULL DEFAULT '',
	code_challenge_method TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL,
	used INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS access_tokens (
	token_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	scope TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_states (
	state TEXT PRIMARY KEY,
	provider TEXT NOT NULL,
	user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
	next TEXT NOT NULL DEFAULT '',
	verifier TEXT NOT NULL DEFAULT '',
	nonce TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_codes_expiry ON auth_codes(expires_at);
CREATE INDEX IF NOT EXISTS idx_access_tokens_expiry ON access_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_oauth_states_expiry ON oauth_states(expires_at);
`
