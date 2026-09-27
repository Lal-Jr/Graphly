// Command server is Graphly's API, realtime and static file server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Addr          string
	DatabaseURL   string
	StaticDir     string
	SecureCookies bool
	// Extra origins allowed to make authenticated requests (the page's own origin always is).
	AllowedOrigins []string
	// Header carrying the real client IP when behind a trusted proxy, e.g. "Fly-Client-IP".
	ClientIPHeader string
}

func loadConfig() Config {
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	cfg := Config{
		Addr:           env("ADDR", ":8080"),
		DatabaseURL:    env("DATABASE_URL", "postgres://localhost:5432/graphly?sslmode=disable"),
		StaticDir:      os.Getenv("STATIC_DIR"),
		SecureCookies:  env("SECURE_COOKIES", "false") == "true",
		ClientIPHeader: os.Getenv("CLIENT_IP_HEADER"),
	}
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
		}
	}
	return cfg
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("connect database", err)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		fatal("migrate", err)
	}

	hub := NewHub(pool)
	go hub.Run(ctx)
	go pruneLoop(ctx, pool)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           NewServer(pool, hub, cfg).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("graphly listening", "addr", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("serve", err)
	}
}

// pruneLoop drops expired sessions and invites, and realtime events older than a day.
func pruneLoop(ctx context.Context, pool *pgxpool.Pool) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		for _, q := range []string{
			`DELETE FROM sessions WHERE expires_at < now()`,
			`DELETE FROM invites WHERE expires_at < now()`,
			`DELETE FROM events WHERE created_at < now() - interval '1 day'`,
			`DELETE FROM workspaces WHERE is_demo AND created_at < now() - interval '7 days'`,
			`DELETE FROM users WHERE is_demo AND created_at < now() - interval '7 days'`,
		} {
			if _, err := pool.Exec(ctx, q); err != nil && ctx.Err() == nil {
				slog.Warn("prune", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
