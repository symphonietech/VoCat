package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"vocat/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	service, err := New(database, Options{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.EnsureAdmin(context.Background(), "admin", "correct-password"); err != nil {
		t.Fatalf("EnsureAdmin() error = %v", err)
	}
	return service
}

// TestLoginAuthenticateCSRFAndLogout checks the complete local session lifecycle and rejection of invalid credentials.
func TestLoginAuthenticateCSRFAndLogout(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	if _, err := service.Login(ctx, "admin", "wrong-password", false); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
	credentials, err := service.Login(ctx, "admin", "correct-password", false)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	session, err := service.Authenticate(ctx, credentials.SessionToken)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if session.Principal.Username != "admin" {
		t.Fatalf("Principal = %+v", session.Principal)
	}
	if _, err := service.ValidateCSRF(ctx, credentials.SessionToken, "wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("ValidateCSRF() error = %v, want ErrInvalidCSRF", err)
	}
	if _, err := service.ValidateCSRF(ctx, credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatalf("ValidateCSRF() error = %v", err)
	}
	_, csrfToken, err := service.CSRFToken(
		ctx,
		credentials.SessionToken,
		credentials.CSRFToken,
	)
	if err != nil {
		t.Fatalf("CSRFToken() error = %v", err)
	}
	if csrfToken != credentials.CSRFToken {
		t.Fatal("CSRFToken() rotated an already valid token")
	}

	if err := service.Logout(ctx, credentials.SessionToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authenticate() after logout error = %v, want ErrUnauthorized", err)
	}
}

// TestEnsureAdminRevokesSessionOnPasswordChange verifies that replacing administrator credentials revokes existing sessions.
func TestEnsureAdminRevokesSessionOnPasswordChange(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	credentials, err := service.Login(ctx, "admin", "correct-password", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.EnsureAdmin(ctx, "admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session error = %v, want ErrUnauthorized", err)
	}
	if _, err := service.Login(ctx, "admin", "new-password", false); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

// TestResetAdminCredentialsChangesUsernameAndPasswordWithoutOldPassword checks administrative credential recovery and rejection of superseded credentials.
func TestResetAdminCredentialsChangesUsernameAndPasswordWithoutOldPassword(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	credentials, err := service.Login(ctx, "admin", "correct-password", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.ResetAdminCredentials(ctx, "new-admin", "replacement-password"); err != nil {
		t.Fatalf("ResetAdminCredentials() error = %v", err)
	}
	if _, err := service.Login(ctx, "admin", "correct-password", false); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old credentials error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := service.Login(ctx, "new-admin", "replacement-password", false); err != nil {
		t.Fatalf("new credentials login error = %v", err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session error = %v, want ErrUnauthorized", err)
	}
}

func TestResetAdminCredentialsValidatesInput(t *testing.T) {
	service := newTestService(t)
	for _, test := range []struct {
		name     string
		username string
		password string
	}{
		{name: "empty username", password: "replacement-password"},
		{name: "control whitespace", username: "bad\tname", password: "replacement-password"},
		{name: "empty password", username: "admin", password: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := service.ResetAdminCredentials(context.Background(), test.username, test.password); err == nil {
				t.Fatal("ResetAdminCredentials() accepted invalid input")
			}
		})
	}
}

// TestResetAdminCredentialsAcceptsPasswordsWithoutComplexityRules keeps administrative recovery independent of password complexity rules.
func TestResetAdminCredentialsAcceptsPasswordsWithoutComplexityRules(t *testing.T) {
	ctx := context.Background()
	for _, password := range []string{"1", strings.Repeat("x", 256)} {
		service := newTestService(t)
		if err := service.ResetAdminCredentials(ctx, "admin", password); err != nil {
			t.Fatalf("ResetAdminCredentials(%d-byte password) error = %v", len(password), err)
		}
		if _, err := service.Login(ctx, "admin", password, false); err != nil {
			t.Fatalf("Login(%d-byte password) error = %v", len(password), err)
		}
	}
}

// TestChangePasswordAcceptsPasswordsWithoutComplexityRules keeps authenticated password changes independent of complexity rules.
func TestChangePasswordAcceptsPasswordsWithoutComplexityRules(t *testing.T) {
	ctx := context.Background()
	for _, password := range []string{"1", strings.Repeat("long-password-", 32)} {
		service := newTestService(t)
		if err := service.ChangePassword(ctx, "admin", "correct-password", password); err != nil {
			t.Fatalf("ChangePassword(%d-byte password) error = %v", len(password), err)
		}
		if _, err := service.Login(ctx, "admin", password, false); err != nil {
			t.Fatalf("Login(%d-byte password) error = %v", len(password), err)
		}
	}
}

// TestEnsureAdminIfMissingDoesNotOverwriteChangedPassword preserves an existing administrator when startup credentials differ.
func TestEnsureAdminIfMissingDoesNotOverwriteChangedPassword(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if err := service.ChangePassword(ctx, "admin", "correct-password", "changed-password"); err != nil {
		t.Fatal(err)
	}
	created, err := service.EnsureAdminIfMissing(ctx, "admin", "stale-config-password")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing administrator was reported as newly created")
	}
	if _, err := service.Login(ctx, "admin", "changed-password", false); err != nil {
		t.Fatalf("database password was overwritten: %v", err)
	}
	if _, err := service.Login(ctx, "admin", "stale-config-password", false); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("stale configured password became active: %v", err)
	}
}

// Symfive issues "sym_" tokens: the prefix makes a leaked token recognisable
// on sight and to secret scanners. Pin the exact form so neither upstream's
// "vcat_" nor a prefix-less token creeps back in through a merge.
func TestCreateAPITokenIssuesSymPrefix(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	credentials, err := service.CreateAPIToken(ctx, "ci-script", false, time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if !strings.HasPrefix(credentials.Token, "sym_") || strings.HasPrefix(credentials.Token, "sym_at_") {
		t.Fatalf("token = %q, want the sym_ prefix", credentials.Token)
	}
	if got, want := len(credentials.Token), len("sym_")+legacyAPITokenLength; got != want {
		t.Fatalf("token length = %d, want %d (prefix + 32 random bytes, base64url)", got, want)
	}
	principal, err := service.AuthenticateAPIToken(ctx, credentials.Token)
	if err != nil {
		t.Fatalf("AuthenticateAPIToken() error = %v", err)
	}
	if principal.Name != "ci-script" || principal.ReadOnly {
		t.Fatalf("principal = %+v, want ci-script read-write", principal)
	}
}

// Tokens issued in an earlier form must keep working: a prefix-less token
// (issued after "vocat_at_" was dropped and before "sym_" was adopted), the
// original "vocat_at_" form, and "sym_at_", which the operator asked to have
// recognised. Each is stored as the hash of the whole string, so validation
// must hash exactly what the caller presents and strip nothing.
func TestAuthenticateAPITokenAcceptsEarlierForms(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	for _, form := range []struct {
		name   string
		prefix string
	}{
		{name: "prefix-less", prefix: ""},
		{name: "vocat-at", prefix: "vocat_at_"},
		{name: "sym-at", prefix: "sym_at_"},
	} {
		secret, err := randomToken()
		if err != nil {
			t.Fatalf("randomToken: %v", err)
		}
		token := form.prefix + secret
		if _, err := service.store.CreateAPIToken(ctx, form.name, hashToken(token), false, time.Time{}); err != nil {
			t.Fatalf("%s: store token: %v", form.name, err)
		}
		principal, err := service.AuthenticateAPIToken(ctx, token)
		if err != nil {
			t.Fatalf("%s: AuthenticateAPIToken() error = %v, want success", form.name, err)
		}
		if principal.Name != form.name {
			t.Fatalf("%s: principal = %+v", form.name, principal)
		}
	}
}

func TestAuthenticateAPITokenRejectsUnknownAndJunk(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	for name, candidate := range map[string]string{
		"empty":               "",
		"too short":           "short",
		"unknown sym":         "sym_" + strings.Repeat("A", 43),
		"unknown sym_at":      "sym_at_" + strings.Repeat("A", 43),
		"unknown vocat_at":    "vocat_at_" + strings.Repeat("A", 43),
		"unknown prefix-less": strings.Repeat("A", 43),
		"upstream vcat form":  "vcat_" + strings.Repeat("A", 43),
		"bare prefix":         "sym_",
		"not base64url":       strings.Repeat("A", 42) + "+",
		"one character short": strings.Repeat("A", 42),
	} {
		if _, err := service.AuthenticateAPIToken(ctx, candidate); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("AuthenticateAPIToken(%s) error = %v, want ErrUnauthorized", name, err)
		}
	}
}

func TestLooksLikeAPIToken(t *testing.T) {
	secret := strings.Repeat("a", legacyAPITokenLength-1) + "_"
	for token, want := range map[string]bool{
		"sym_" + secret:      true,
		"sym_at_" + secret:   true,
		"vocat_at_" + secret: true,
		secret:               true,
		// A bare prefix is not a token. ("sym_at_" alone passes, being
		// "sym_" followed by "at_": this is a cheap filter, not validation.)
		"sym_":             false,
		"vcat_" + secret:   false,
		secret[1:]:         false,
		secret + "a":       false,
		"Bearer " + secret: false,
		"":                 false,
	} {
		if got := LooksLikeAPIToken(token); got != want {
			t.Errorf("LooksLikeAPIToken(%q) = %v, want %v", token, got, want)
		}
	}
}
