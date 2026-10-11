// Command server is Graphly's API, realtime and static file server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the runtime image has no zoneinfo; FORECAST_TIMEZONE needs it

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	// Logging: LOG_FORMAT json (default) or text, LOG_LEVEL debug/info/warn/error, LOG_REQUESTS.
	LogFormat, LogLevel string
	LogRequests         bool
	// /metrics: on the main listener with METRICS_TOKEN as a bearer token, and/or unauthenticated
	// on a private METRICS_ADDR (e.g. :9090) that isn't exposed publicly.
	MetricsToken, MetricsAddr string

	Addr          string
	DatabaseURL   string
	StaticDir     string
	SecureCookies bool
	// Extra origins allowed to make authenticated requests (the page's own origin always is).
	AllowedOrigins []string
	// Header carrying the real client IP when behind a trusted proxy, e.g. "Fly-Client-IP".
	ClientIPHeader string
	// The timezone "today" is decided in for forecasts, and how often the background job runs.
	ForecastLocation *time.Location
	ForecastInterval time.Duration
	// A project's overruns are calibrated from its own history once it has this many finished,
	// estimated issues; each type needs this many of its own, or it uses the project-wide range.
	CalibrationMinIssues  int
	CalibrationMinPerType int
	// People involved in a project are notified when its ship date moves by at least this many working days.
	ShipDateMoveThreshold int
	// Hosts a user's Slack incoming webhook may point at.
	SlackWebhookHosts []string
	// Where the app is served, for links in emails and Slack messages (and OAuth redirects).
	PublicURL string
	// OAuth sign-in; a provider is offered when its client id is set.
	GoogleClientID, GoogleClientSecret, GitHubClientID, GitHubClientSecret string
	// Outgoing email. Leave SMTPHost empty to turn email off.
	SMTPHost, SMTPPort, SMTPUsername, SMTPPassword, SMTPFrom string
	// Requests per minute per API token.
	APITokenRateLimit int
	// Notification delivery: how long to gather a burst into one digest, how often to look, how
	// many times to try before giving up.
	DigestWindow         time.Duration
	DeliveryPollInterval time.Duration
	DeliveryMaxAttempts  int
}

func loadConfig() Config {
	cfg := Config{
		Addr:           env("ADDR", ":8080"),
		DatabaseURL:    env("DATABASE_URL", "postgres://localhost:5432/graphly?sslmode=disable"),
		StaticDir:      os.Getenv("STATIC_DIR"),
		SecureCookies:  env("SECURE_COOKIES", "false") == "true",
		ClientIPHeader: os.Getenv("CLIENT_IP_HEADER"),
	}
	cfg.LogFormat, cfg.LogLevel = env("LOG_FORMAT", "json"), env("LOG_LEVEL", "info")
	cfg.LogRequests = env("LOG_REQUESTS", "true") == "true"
	cfg.MetricsToken, cfg.MetricsAddr = os.Getenv("METRICS_TOKEN"), os.Getenv("METRICS_ADDR")
	loc, err := time.LoadLocation(env("FORECAST_TIMEZONE", "UTC"))
	if err != nil {
		fatal("FORECAST_TIMEZONE", err)
	}
	cfg.ForecastLocation = loc
	cfg.ForecastInterval = envDuration("FORECAST_INTERVAL", 15*time.Minute)
	cfg.CalibrationMinIssues = envInt("CALIBRATION_MIN_ISSUES", 20)
	cfg.CalibrationMinPerType = envInt("CALIBRATION_MIN_PER_TYPE", 5)
	cfg.ShipDateMoveThreshold = envInt("SHIP_DATE_MOVE_THRESHOLD", 2)
	cfg.SlackWebhookHosts = envList("SLACK_WEBHOOK_HOSTS", "hooks.slack.com")
	cfg.PublicURL = strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
	cfg.GoogleClientID, cfg.GoogleClientSecret = os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET")
	cfg.GitHubClientID, cfg.GitHubClientSecret = os.Getenv("GITHUB_CLIENT_ID"), os.Getenv("GITHUB_CLIENT_SECRET")
	cfg.SMTPHost, cfg.SMTPPort = os.Getenv("SMTP_HOST"), env("SMTP_PORT", "587")
	cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPFrom = os.Getenv("SMTP_USERNAME"), os.Getenv("SMTP_PASSWORD"), os.Getenv("SMTP_FROM")
	cfg.APITokenRateLimit = envInt("API_TOKEN_RATE_LIMIT", 600)
	cfg.DigestWindow = envDuration("NOTIFY_DIGEST_WINDOW", 2*time.Minute)
	cfg.DeliveryPollInterval = envDuration("NOTIFY_POLL_INTERVAL", 5*time.Second)
	cfg.DeliveryMaxAttempts = envInt("NOTIFY_MAX_ATTEMPTS", 8)
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
		}
	}
	return cfg
}

func envDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		fatal(k, err)
	}
	return d
}

func envList(k, def string) []string {
	var out []string
	for _, v := range strings.Split(env(k, def), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		fatal(k, err)
	}
	return n
}

func main() {
	cfg := loadConfig()
	slog.SetDefault(newLogger(cfg.LogFormat, cfg.LogLevel, os.Stdout))
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
	if len(os.Args) > 1 && os.Args[1] == "synthetic" {
		if err := runSynthetic(ctx, pool, os.Args[2:]); err != nil {
			fatal("synthetic", err)
		}
		return
	}

	hub := NewHub(pool)
	go hub.Run(ctx)
	go pruneLoop(ctx, pool)

	app := NewServer(pool, hub, cfg)
	go app.forecastLoop(ctx)
	go app.deliveryLoop(ctx)
	if cfg.MetricsAddr != "" {
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /metrics", app.metricsHandler(false))
			slog.Info("metrics listening", "addr", cfg.MetricsAddr)
			if err := http.ListenAndServe(cfg.MetricsAddr, mux); err != nil {
				slog.Error("metrics listener", "err", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Routes(),
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
