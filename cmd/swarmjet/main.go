package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/api"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forager"
	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/mcp"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
	"github.com/yyydddkkk/SwarmJet/internal/observability"
	"github.com/yyydddkkk/SwarmJet/internal/onboarding"
	"github.com/yyydddkkk/SwarmJet/internal/router"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/service"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

var buildVersion = "dev"
var buildCommit = "unknown"

// The control plane serves MCP straight from the Hive store. The Router may
// also link a Forager to the PR it opened once the same store implements
// SetAgentPR.
var (
	_ mcp.Backend       = (*store.Store)(nil)
	_ router.PRRecorder = (*store.Store)(nil)
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func required(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing environment", "name", k)
		os.Exit(2)
	}
	return v
}

func main() {
	if err := run(); err != nil {
		slog.Error("swarmjet stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "help" || mode == "--help" {
		fmt.Println("swarmjet serve | migrate | version | token create --name <name> --scope default|admin | connect --client claude|codex|json [--name <name>] [--url <mcp-url>]")
		return nil
	}
	if mode == "version" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": buildVersion, "commitSHA": buildCommit})
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := store.Open(ctx, required("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer s.Close()
	if mode == "migrate" {
		return s.Migrate(ctx)
	}
	if mode == "token" {
		return runToken(ctx, s, os.Args[2:])
	}
	if mode == "connect" {
		return runConnect(ctx, s, os.Args[2:], os.Stdout, os.Stderr)
	}
	if mode != "serve" {
		return fmt.Errorf("unknown mode %s", mode)
	}
	return runServe(ctx, s)
}

func runServe(ctx context.Context, s *store.Store) error {
	// The master key is deployment-managed and must be stable: a new key on
	// startup would make previously saved credentials undecryptable.
	box, err := secretbox.New(required("SWARMJET_MASTER_KEY"))
	if err != nil {
		return fmt.Errorf("SWARMJET_MASTER_KEY must be 32 bytes of hex: %w", err)
	}

	// The Provider is the single dynamic GitHub App source for Cells, webhook
	// verification, Router platform-event detection and the setup page.
	// Environment variables win over the encrypted database row; an App created
	// from the console works without a restart.
	ghProvider := onboarding.NewProvider(onboarding.ProviderConfig{
		Env:    onboarding.EnvConfigFromEnvironment(),
		Store:  s,
		Box:    box,
		Logger: slog.Default(),
	})
	var ghApp *githubapp.App
	ghApp, err = ghProvider.App(ctx)
	if err != nil {
		if !errors.Is(err, onboarding.ErrNotConfigured) {
			return err
		}
		ghApp = nil
	}

	metrics := observability.New()
	metrics.SetReadiness(func(parent context.Context) error {
		checkCtx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		return s.Pool.Ping(checkCtx)
	})
	if address := os.Getenv("METRICS_LISTEN_ADDRESS"); address != "" {
		if err := metrics.Serve(ctx, address); err != nil {
			return fmt.Errorf("metrics listener: %w", err)
		}
	}

	data, err := filepath.Abs(required("DATA_DIR"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(data, 0700); err != nil {
		return err
	}

	var tunnel *onboarding.Tunnel
	if raw := os.Getenv("SWARMJET_TUNNEL_URL"); raw != "" {
		tunnel, err = onboarding.NewTunnel(onboarding.TunnelConfig{
			MetricsURL: raw,
			Apps:       ghProvider,
			Logger:     slog.Default(),
		})
		if err != nil {
			return err
		}
		go tunnel.Run(ctx)
	}
	redeliverCfg := onboarding.RedeliveryConfig{Apps: ghProvider, Logger: slog.Default()}
	if tunnel != nil {
		redeliverCfg.Trigger = tunnel.Ready()
	}
	redeliverer, err := onboarding.NewRedeliverer(redeliverCfg)
	if err != nil {
		return err
	}
	go redeliverer.Run(ctx)
	setupManager, err := onboarding.NewManager(onboarding.ManagerConfig{
		Provider:     ghProvider,
		Store:        s,
		Box:          box,
		PublicOrigin: env("PUBLIC_ORIGIN", "http://localhost:8180"),
		Tunnel:       tunnel,
		Redeliverer:  redeliverer,
		Logger:       slog.Default(),
	})
	if err != nil {
		return err
	}

	// Cells run in this process: the River message delivery job hands each
	// delivered Inbox message straight to the supervisor. Install the sink
	// before the queue starts and wait for the supervisor on shutdown.
	var cells *cellStack
	if os.Getenv("SWARMJET_CELLS") == "1" {
		cells, err = newCellStack(s, ghProvider, data, slog.Default())
		if err != nil {
			return err
		}
		if ghApp == nil {
			slog.Warn("GitHub App is not configured; Cells will report an explicit error until the App is created in the setup page")
		}
		s.MessageSink = cells.Supervisor.Notify
		cells.Start(ctx)
		defer cells.Stop()
		slog.Info("forager cells enabled", "model", env("SWARMJET_FORAGER_MODEL", forager.DefaultModel))
	}

	// The merge service reads the Hive repository through the same dynamic App
	// provider as the Cells, so a newly created App works without a restart.
	mergeRepos := newGitHubRepositories(ghProvider)
	mergeService := merge.New(merge.Options{
		Store:        s,
		Attestations: s,
		Methodology:  methodologyStore{s: s},
		GitHub:       mergeGitHubSource{repos: mergeRepos},
	})
	var mergeScheduler *merge.Scheduler

	password := required("ADMIN_PASSWORD")
	if len(password) < 12 {
		return fmt.Errorf("ADMIN_PASSWORD must contain at least 12 characters")
	}
	adminUser := required("ADMIN_USERNAME")
	staticDir := env("STATIC_DIR", "web/dist")
	// The CLI login terminal runs in this process and shares the Cell data
	// directory, so a successful login is immediately available to Cells.
	cliLogin, cliLoginReason := newCLILoginHandler(data)
	handler := api.New(s, api.Config{
		GitHubApp:       ghApp,
		Box:             box,
		Username:        adminUser,
		Password:        password,
		AllowedOrigin:   env("PUBLIC_ORIGIN", "http://localhost:8180"),
		StaticDir:       staticDir,
		SecureCookies:   env("SECURE_COOKIES", "false") == "true",
		Setup:           setupManager,
		CellsEnabled:    os.Getenv("SWARMJET_CELLS") == "1",
		CellsImage:      env("AGENT_IMAGE", "swarmjet-agent:local"),
		CellsModel:      env("SWARMJET_FORAGER_MODEL", forager.DefaultModel),
		DockerAvailable: onboarding.DockerAvailable,
		CLILogin:        cliLogin,
		CLILoginReason:  cliLoginReason,
		CLILoginStatus:  cliLoginStatus(cliLogin, data),
		MCPURL:          publicMCPURL(),
		Merge:           mergeService,
	})
	svc := &service.Service{Store: s, Webhooks: s, AppSlugSource: ghProvider.Slug}

	recordJob := func(queue string, err error) {
		result := "acked"
		if errors.Is(err, jobs.ErrCancel) {
			result = "dead_letter"
		} else if err != nil {
			result = "retry"
		}
		metrics.Message(queue, result)
	}
	queue, err := jobs.Open(s.Pool, jobs.Handlers{
		Webhook: func(ctx context.Context, job jobs.Webhook) error {
			err := svc.HandleWebhookJob(ctx, job)
			recordJob("webhook", err)
			return err
		},
		Message: func(ctx context.Context, job jobs.Message) error {
			err := s.DeliverMessage(ctx, job.ID)
			recordJob("message", err)
			return err
		},
		MergeCheck: func(ctx context.Context, job jobs.MergeCheck) error {
			err := mergeScheduler.Handle(ctx, job)
			recordJob("merge", err)
			return err
		},
	}, jobs.Config{})
	if err != nil {
		return err
	}
	s.Jobs = queue
	mergeScheduler = merge.NewScheduler(s, mergeService, mergeService, mergeService, queue)
	mergeService.SetInserter(queue)
	svc.HiveRouter = router.NewWithOptions(s, router.Options{
		AppSlugSource: ghProvider.Slug,
		OnChecks:      mergeScheduler.OnChecks,
		OnHead: func(ctx context.Context, hive domain.Hive, _ domain.Agent, pr int64, sha string) error {
			return s.SupersedeAttestations(ctx, hive.ID, pr, sha)
		},
	})
	go func() {
		if err := queue.Start(ctx); err != nil && ctx.Err() == nil {
			slog.Error("job queue stopped", "error", err)
		}
	}()

	webhook := svc.GitHubWebhookDynamic(ghProvider.WebhookSecret)
	routes := http.NewServeMux()
	routes.Handle("/api/webhooks/github", webhook)
	// MCP authenticates with a bearer token only; it never uses the console
	// session (ADR section 12).
	routes.Handle("/mcp", mcp.NewHandler(s, mcp.Options{Logger: slog.Default(), Merge: mergeService}))
	routes.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	routes.Handle("/", handler)

	server := &http.Server{Addr: listenAddress(), Handler: metrics.HTTP(routes), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	// The tunnel forwards only this listener, so the console and /mcp stay off
	// the public internet.
	webhookServer := &http.Server{Addr: env("WEBHOOK_LISTEN_ADDRESS", ":8081"), Handler: metrics.HTTP(webhookRoutes(webhook)), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = webhookServer.Shutdown(c)
	}()
	webhookErr := make(chan error, 1)
	go func() {
		slog.Info("webhook listening", "address", webhookServer.Addr)
		if err := webhookServer.ListenAndServe(); err != http.ErrServerClosed {
			webhookErr <- fmt.Errorf("webhook listener: %w", err)
		}
	}()
	slog.Info("console listening", "address", server.Addr)
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	select {
	case err = <-errs:
	case err = <-webhookErr:
		_ = server.Close()
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// webhookRoutes is the only surface reachable through the public tunnel:
// GitHub webhooks and a health check. Every other path is a 404.
func webhookRoutes(webhook http.Handler) http.Handler {
	routes := http.NewServeMux()
	routes.Handle("/api/webhooks/github", webhook)
	routes.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	return routes
}

// listenAddress keeps LISTEN_ADDRESS for container deployments and accepts the
// documented LISTEN_HOST / HTTP_PORT pair for native runs.
func listenAddress() string {
	if address := os.Getenv("LISTEN_ADDRESS"); address != "" {
		return address
	}
	port := env("HTTP_PORT", "8080")
	if host := os.Getenv("LISTEN_HOST"); host != "" {
		return net.JoinHostPort(host, port)
	}
	return ":" + port
}
