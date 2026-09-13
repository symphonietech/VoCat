package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/store"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrInvalidCSRF        = errors.New("invalid csrf token")
	ErrEmptyPassword      = errors.New("password cannot be empty")
)

const bcryptPasswordLimit = 72

var longPasswordHashPrefix = []byte("$vocat-sha256$")

type Options struct {
	SessionTTL time.Duration
	BcryptCost int
}

type Service struct {
	store      *store.Store
	sessionTTL time.Duration
	bcryptCost int
	dummyHash  []byte
}

type Principal struct {
	ID       int64  `json:"-"`
	Username string `json:"username"`
}

type Credentials struct {
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
	Principal    Principal
}

type AuthenticatedSession struct {
	Principal Principal
	ExpiresAt time.Time
	tokenHash []byte
	csrfHash  []byte
}

func New(database *store.Store, options Options) (*Service, error) {
	if database == nil {
		return nil, errors.New("auth: store is required")
	}
	if options.SessionTTL <= 0 {
		return nil, errors.New("auth: session TTL must be positive")
	}
	if options.BcryptCost == 0 {
		options.BcryptCost = 12
	}
	if options.BcryptCost < bcrypt.MinCost || options.BcryptCost > bcrypt.MaxCost {
		return nil, errors.New("auth: bcrypt cost is out of range")
	}
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), options.BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("auth: generate timing hash: %w", err)
	}
	return &Service{
		store:      database,
		sessionTTL: options.SessionTTL,
		bcryptCost: options.BcryptCost,
		dummyHash:  dummyHash,
	}, nil
}

// EnsureAdmin configures the single administrator. Existing sessions are
// revoked only when the configured username or password changes.
func (s *Service) EnsureAdmin(ctx context.Context, username string, password string) error {
	username = strings.TrimSpace(username)
	current, err := s.store.CurrentAdmin(ctx)
	if err == nil &&
		current.Username == username &&
		comparePassword(current.PasswordHash, password) == nil {
		return nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("auth: read configured admin: %w", err)
	}

	passwordHash, err := hashPassword(password, s.bcryptCost)
	if err != nil {
		return fmt.Errorf("auth: hash admin password: %w", err)
	}
	if err := s.store.SetAdmin(ctx, username, passwordHash); err != nil {
		return err
	}
	return nil
}

// EnsureAdminIfMissing initializes the administrator only for a new database.
// Once an administrator exists, the database is the sole credential source;
// process configuration must never overwrite a password changed through the UI
// or CLI on a later restart.
func (s *Service) EnsureAdminIfMissing(ctx context.Context, username string, password string) (bool, error) {
	if _, err := s.store.CurrentAdmin(ctx); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, fmt.Errorf("auth: read configured admin: %w", err)
	}
	if err := s.EnsureAdmin(ctx, username, password); err != nil {
		return false, err
	}
	return true, nil
}

// ResetAdminCredentials replaces the single administrator without requiring
// the previous credentials. It is intended for trusted local recovery flows
// such as the root-only management CLI. Store.SetAdmin atomically revokes all
// existing sessions when the credentials change.
func (s *Service) ResetAdminCredentials(ctx context.Context, username string, password string) error {
	username = strings.TrimSpace(username)
	if len(username) < 1 || len(username) > 64 || strings.ContainsAny(username, "\r\n\t") {
		return errors.New("administrator username must contain between 1 and 64 characters without control whitespace")
	}
	if password == "" {
		return ErrEmptyPassword
	}
	if err := s.EnsureAdmin(ctx, username, password); err != nil {
		return fmt.Errorf("auth: reset administrator credentials: %w", err)
	}
	return nil
}

func (s *Service) Login(ctx context.Context, username string, password string) (Credentials, error) {
	admin, err := s.store.AdminByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, store.ErrNotFound) {
		_ = comparePassword(s.dummyHash, password)
		return Credentials{}, ErrInvalidCredentials
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("auth: find admin: %w", err)
	}
	if comparePassword(admin.PasswordHash, password) != nil {
		return Credentials{}, ErrInvalidCredentials
	}

	if err := s.store.DeleteExpiredSessions(ctx, time.Now()); err != nil {
		return Credentials{}, err
	}
	sessionToken, err := randomToken()
	if err != nil {
		return Credentials{}, err
	}
	csrfToken, err := randomToken()
	if err != nil {
		return Credentials{}, err
	}
	expiresAt := time.Now().UTC().Add(s.sessionTTL)
	if err := s.store.CreateSession(
		ctx,
		admin.ID,
		hashToken(sessionToken),
		hashToken(csrfToken),
		expiresAt,
	); err != nil {
		return Credentials{}, err
	}
	return Credentials{
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		ExpiresAt:    expiresAt,
		Principal: Principal{
			ID:       admin.ID,
			Username: admin.Username,
		},
	}, nil
}

func (s *Service) Authenticate(ctx context.Context, sessionToken string) (AuthenticatedSession, error) {
	if sessionToken == "" {
		return AuthenticatedSession{}, ErrUnauthorized
	}
	tokenHash := hashToken(sessionToken)
	session, err := s.store.SessionByTokenHash(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		return AuthenticatedSession{}, ErrUnauthorized
	}
	if err != nil {
		return AuthenticatedSession{}, fmt.Errorf("auth: load session: %w", err)
	}
	if !session.ExpiresAt.After(time.Now().UTC()) {
		_ = s.store.DeleteSession(ctx, tokenHash)
		return AuthenticatedSession{}, ErrUnauthorized
	}
	return AuthenticatedSession{
		Principal: Principal{
			ID:       session.Admin.ID,
			Username: session.Admin.Username,
		},
		ExpiresAt: session.ExpiresAt,
		tokenHash: tokenHash,
		csrfHash:  session.CSRFHash,
	}, nil
}

// RotateCSRF replaces the session-bound CSRF value and returns the new raw
// token. Only its SHA-256 digest is persisted.
func (s *Service) RotateCSRF(ctx context.Context, sessionToken string) (AuthenticatedSession, string, error) {
	return s.CSRFToken(ctx, sessionToken, "")
}

// CSRFToken reuses a valid CSRF cookie or rotates it when the cookie is absent
// or stale. Reuse prevents one browser tab from invalidating another tab's
// session-bound token.
func (s *Service) CSRFToken(
	ctx context.Context,
	sessionToken string,
	existingToken string,
) (AuthenticatedSession, string, error) {
	session, err := s.Authenticate(ctx, sessionToken)
	if err != nil {
		return AuthenticatedSession{}, "", err
	}
	if existingToken != "" {
		existingHash := hashToken(existingToken)
		if subtle.ConstantTimeCompare(existingHash, session.csrfHash) == 1 {
			return session, existingToken, nil
		}
	}
	csrfToken, err := randomToken()
	if err != nil {
		return AuthenticatedSession{}, "", err
	}
	csrfHash := hashToken(csrfToken)
	if err := s.store.UpdateSessionCSRF(ctx, session.tokenHash, csrfHash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return AuthenticatedSession{}, "", ErrUnauthorized
		}
		return AuthenticatedSession{}, "", err
	}
	session.csrfHash = csrfHash
	return session, csrfToken, nil
}

func (s *Service) ValidateCSRF(
	ctx context.Context,
	sessionToken string,
	csrfToken string,
) (AuthenticatedSession, error) {
	if csrfToken == "" {
		return AuthenticatedSession{}, ErrInvalidCSRF
	}
	session, err := s.Authenticate(ctx, sessionToken)
	if err != nil {
		return AuthenticatedSession{}, err
	}
	providedHash := hashToken(csrfToken)
	if subtle.ConstantTimeCompare(providedHash, session.csrfHash) != 1 {
		return AuthenticatedSession{}, ErrInvalidCSRF
	}
	return session, nil
}

func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	if sessionToken == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, hashToken(sessionToken)); err != nil {
		return err
	}
	return nil
}

// apiTokenPrefix marks a raw token as a VoCat API token so it is
// recognizable (e.g. in logs or config files) without decoding it.
const apiTokenPrefix = "vocat_at_"

// DefaultAPITokenTTL is applied by callers that do not pick their own
// lifetime (the CLI's --ttl flag, for example).
const DefaultAPITokenTTL = 30 * 24 * time.Hour

type APITokenInfo struct {
	ID         int64
	Name       string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

// CreateAPIToken mints a new long-lived, non-interactive credential and
// returns the raw token exactly once; only its SHA-256 hash is persisted.
func (s *Service) CreateAPIToken(ctx context.Context, name string, ttl time.Duration) (string, APITokenInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", APITokenInfo{}, errors.New("auth: api token name is required")
	}
	if ttl <= 0 {
		return "", APITokenInfo{}, errors.New("auth: api token ttl must be positive")
	}
	secret, err := randomToken()
	if err != nil {
		return "", APITokenInfo{}, err
	}
	rawToken := apiTokenPrefix + secret
	expiresAt := time.Now().UTC().Add(ttl)
	record, err := s.store.CreateAPIToken(ctx, name, hashToken(rawToken), expiresAt)
	if err != nil {
		return "", APITokenInfo{}, err
	}
	return rawToken, APITokenInfo{
		ID:        record.ID,
		Name:      record.Name,
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
	}, nil
}

// AuthenticateAPIToken validates a raw bearer token and returns the
// principal it grants access as. VoCat has a single administrator, so a
// valid token always authenticates as the current admin. Expired tokens are
// deleted so ListAPITokens does not accumulate dead rows.
func (s *Service) AuthenticateAPIToken(ctx context.Context, rawToken string) (Principal, error) {
	if rawToken == "" || !strings.HasPrefix(rawToken, apiTokenPrefix) {
		return Principal{}, ErrUnauthorized
	}
	record, err := s.store.APITokenByHash(ctx, hashToken(rawToken))
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, fmt.Errorf("auth: load api token: %w", err)
	}
	if !record.ExpiresAt.After(time.Now().UTC()) {
		_ = s.store.DeleteAPIToken(ctx, record.ID)
		return Principal{}, ErrUnauthorized
	}
	admin, err := s.store.CurrentAdmin(ctx)
	if err != nil {
		return Principal{}, fmt.Errorf("auth: read administrator: %w", err)
	}
	// Best-effort: a failed last-used update must never block the request.
	_ = s.store.TouchAPIToken(ctx, record.ID, time.Now().UTC())
	return Principal{ID: admin.ID, Username: admin.Username}, nil
}

func (s *Service) ListAPITokens(ctx context.Context) ([]APITokenInfo, error) {
	records, err := s.store.ListAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]APITokenInfo, 0, len(records))
	for _, record := range records {
		infos = append(infos, APITokenInfo{
			ID:         record.ID,
			Name:       record.Name,
			CreatedAt:  record.CreatedAt,
			ExpiresAt:  record.ExpiresAt,
			LastUsedAt: record.LastUsedAt,
		})
	}
	return infos, nil
}

func (s *Service) RevokeAPIToken(ctx context.Context, id int64) error {
	return s.store.DeleteAPIToken(ctx, id)
}

// ChangePassword verifies the current password, replaces it with a fresh
// bcrypt hash and revokes every session through Store.SetAdmin.
func (s *Service) ChangePassword(
	ctx context.Context,
	username string,
	currentPassword string,
	newPassword string,
) error {
	if newPassword == "" {
		return ErrEmptyPassword
	}
	admin, err := s.store.AdminByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, store.ErrNotFound) {
		_ = comparePassword(s.dummyHash, currentPassword)
		return ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("auth: find admin: %w", err)
	}
	if comparePassword(admin.PasswordHash, currentPassword) != nil {
		return ErrInvalidCredentials
	}
	if comparePassword(admin.PasswordHash, newPassword) == nil {
		return errors.New("new password must differ from the current password")
	}
	passwordHash, err := hashPassword(newPassword, s.bcryptCost)
	if err != nil {
		return fmt.Errorf("auth: hash new password: %w", err)
	}
	if err := s.store.SetAdmin(ctx, admin.Username, passwordHash); err != nil {
		return fmt.Errorf("auth: save new password: %w", err)
	}
	return nil
}

// hashPassword keeps ordinary bcrypt hashes compatible with existing
// installations. bcrypt rejects inputs longer than 72 bytes, so only longer
// passwords use a tagged SHA-256 pre-hash before bcrypt.
func hashPassword(password string, cost int) ([]byte, error) {
	material := []byte(password)
	longPassword := len(material) > bcryptPasswordLimit
	if longPassword {
		// SHA-256 here is strictly a fixed-length condenser for bcrypt's 72-byte limit,
		// not a standalone password hash. bcrypt provides the actual adaptive work factor.
		// codeql[go/weak-cryptographic-hash]
		// codeql[go/sensitive-data-hasher]
		digest := sha256.Sum256(material)
		material = digest[:]
	}
	passwordHash, err := bcrypt.GenerateFromPassword(material, cost)
	if err != nil {
		return nil, err
	}
	if !longPassword {
		return passwordHash, nil
	}
	return append(append([]byte(nil), longPasswordHashPrefix...), passwordHash...), nil
}

func comparePassword(passwordHash []byte, password string) error {
	material := []byte(password)
	if bytes.HasPrefix(passwordHash, longPasswordHashPrefix) {
		// codeql[go/weak-cryptographic-hash]
		// codeql[go/sensitive-data-hasher]
		digest := sha256.Sum256(material)
		material = digest[:]
		passwordHash = passwordHash[len(longPasswordHashPrefix):]
	}
	return bcrypt.CompareHashAndPassword(passwordHash, material)
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("auth: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}
