package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"vocat/internal/auth"
	"vocat/internal/config"
	"vocat/internal/store"
)

// runAPIToken handles `vocat api-token create|list|revoke`. API tokens are a
// long-lived, non-interactive credential for scripted access: unlike a
// browser session they need no cookie or CSRF token, are sent as
// `Authorization: Bearer <token>`, and are authenticated in
// internal/server/server.go's requireAuthenticated.
func runAPIToken(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: vocat api-token <create|list|revoke> [flags]")
	}
	action, rest := args[0], args[1:]

	// Match the menu's env resolution so this reads/writes the same database
	// the running service uses, on both bare-metal (systemd EnvironmentFile)
	// and Docker (VOCAT_DATABASE_PATH already set in the container env).
	loadMenuEnv()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	authService, err := auth.New(database, auth.Options{SessionTTL: cfg.SessionTTL})
	if err != nil {
		return err
	}

	switch action {
	case "create":
		return runAPITokenCreate(ctx, authService, rest)
	case "list":
		return runAPITokenList(ctx, authService, rest)
	case "revoke":
		return runAPITokenRevoke(ctx, authService, rest)
	default:
		return fmt.Errorf("vocat api-token: unknown action %q (expected create, list, or revoke)", action)
	}
}

func runAPITokenCreate(ctx context.Context, authService *auth.Service, args []string) error {
	flags := flag.NewFlagSet("api-token create", flag.ContinueOnError)
	name := flags.String("name", "", "label to identify this token (required)")
	ttl := flags.Duration("ttl", auth.DefaultAPITokenTTL, "token lifetime, e.g. 720h for 30 days")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: vocat api-token create --name <label> [--ttl 720h]")
	}
	rawToken, info, err := authService.CreateAPIToken(ctx, *name, *ttl)
	if err != nil {
		return err
	}
	fmt.Printf("Token created. Copy it now — it will not be shown again:\n\n%s\n\n", rawToken)
	fmt.Printf("id=%d name=%q expires_at=%s\n", info.ID, info.Name, info.ExpiresAt.Format(time.RFC3339))
	fmt.Println(`Use it as: curl -H "Authorization: Bearer <token>" http://<host>:7575/api/...`)
	return nil
}

func runAPITokenList(ctx context.Context, authService *auth.Service, args []string) error {
	flags := flag.NewFlagSet("api-token list", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	tokens, err := authService.ListAPITokens(ctx)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Println("No API tokens.")
		return nil
	}
	fmt.Printf("%-6s %-24s %-20s %-20s %s\n", "ID", "NAME", "EXPIRES_AT", "LAST_USED_AT", "STATUS")
	now := time.Now().UTC()
	for _, token := range tokens {
		lastUsed := "never"
		if token.LastUsedAt != nil {
			lastUsed = token.LastUsedAt.Format(time.RFC3339)
		}
		status := "active"
		if !token.ExpiresAt.After(now) {
			status = "expired"
		}
		fmt.Printf("%-6d %-24s %-20s %-20s %s\n",
			token.ID, token.Name, token.ExpiresAt.Format(time.RFC3339), lastUsed, status)
	}
	return nil
}

func runAPITokenRevoke(ctx context.Context, authService *auth.Service, args []string) error {
	flags := flag.NewFlagSet("api-token revoke", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: vocat api-token revoke <id>")
	}
	id, err := strconv.ParseInt(flags.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid token id %q: %w", flags.Arg(0), err)
	}
	if err := authService.RevokeAPIToken(ctx, id); err != nil {
		return err
	}
	fmt.Printf("Token %d revoked.\n", id)
	return nil
}
