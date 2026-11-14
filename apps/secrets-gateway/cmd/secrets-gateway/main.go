// Command secrets-gateway serves the codebox development-secrets gateway:
// ProConnect OIDC authentication, per-project ACL authorisation, and resolution
// of secret bundles from the dedicated Scaleway Secret Manager project.
//
// It holds no database and no user table. All state lives in two secrets per
// project (/acl/<id> and /secrets/<id>) inside an otherwise empty Scaleway
// project, and every secret value it reads is returned to the authenticated
// caller without being logged or persisted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/etalab-ia/codebox/apps/secrets-gateway/internal/gateway"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "secrets-gateway: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("secrets-gateway", flag.ContinueOnError)
	printSeed := fs.Bool("print-token-seed", false, "print a fresh token signing seed and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *printSeed {
		seed, err := gateway.NewTokenSeed()
		if err != nil {
			return err
		}
		fmt.Println(seed)
		return nil
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := configFromEnv(logger)
	if err != nil {
		return err
	}

	oidc, err := gateway.NewOIDCClient(cfg.OIDC, &http.Client{Timeout: 15 * time.Second}, time.Now)
	if err != nil {
		return err
	}
	tokens, err := gateway.NewTokenIssuer(cfg.TokenSeedB64, cfg.PublicURL, cfg.TokenLifetime, time.Now)
	if err != nil {
		return err
	}
	store, err := gateway.NewScalewayStore(
		envOr("SCW_API_URL", "https://api.scaleway.com"),
		os.Getenv("SCW_SECRET_KEY"),
		os.Getenv("SCW_PROJECT_ID"),
		envOr("SCW_REGION", "fr-par"),
		&http.Client{Timeout: 20 * time.Second},
	)
	if err != nil {
		return err
	}
	server, err := gateway.NewServer(
		cfg,
		oidc,
		store,
		tokens,
		gateway.NewAttemptStore(10*time.Minute, 4096, time.Now),
		gateway.NewRateLimiter(60, time.Minute, 8192, time.Now),
		time.Now,
	)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("secrets-gateway listening", slog.String("addr", cfg.ListenAddr), slog.String("store", store.Kind()))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func configFromEnv(logger *slog.Logger) (gateway.Config, error) {
	publicURL := os.Getenv("GATEWAY_PUBLIC_URL")
	if publicURL == "" {
		return gateway.Config{}, errors.New("GATEWAY_PUBLIC_URL is required")
	}
	oidcCfg := gateway.OIDCConfig{
		Issuer:        envOr("PROCONNECT_ISSUER", gateway.ProConnectSandboxIssuer),
		ClientID:      os.Getenv("PROCONNECT_CLIENT_ID"),
		ClientSecret:  os.Getenv("PROCONNECT_CLIENT_SECRET"),
		RedirectURL:   strings.TrimSuffix(publicURL, "/") + "/auth/callback",
		Scopes:        splitNonEmpty(os.Getenv("PROCONNECT_SCOPES")),
		AllowedSIRETs: splitNonEmpty(os.Getenv("GATEWAY_ALLOWED_SIRETS")),
	}
	if oidcCfg.Issuer == gateway.ProConnectSandboxIssuer {
		logger.Warn("using the ProConnect sandbox issuer; set PROCONNECT_ISSUER to the production issuer once the DataPass authorisation is granted")
	}
	return gateway.Config{
		ListenAddr:     envOr("PORT_LISTEN", ":"+envOr("PORT", "8080")),
		PublicURL:      publicURL,
		TokenSeedB64:   os.Getenv("GATEWAY_TOKEN_SEED"),
		TokenLifetime:  durationOr("GATEWAY_TOKEN_LIFETIME", 12*time.Hour),
		TicketLifetime: durationOr("GATEWAY_TICKET_LIFETIME", 5*time.Minute),
		OIDC:           oidcCfg,
		AuditLogger:    logger,
	}, nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func durationOr(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func splitNonEmpty(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
