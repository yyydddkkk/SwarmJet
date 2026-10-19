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
	"github.com/yyydddkkk/SwarmJet/internal/forager"
	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/mcp"
	"github.com/yyydddkkk/SwarmJet/internal/observability"
	"github.com/yyydddkkk/SwarmJet/internal/onboarding"
	"github.com/yyydddkkk/SwarmJet/internal/router"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/service"
	"github.com/yyydddkkk/SwarmJet/internal/store"
	"github.com/yyydddkkk/SwarmJet/internal/verify"
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

	// Independent verification: the Router calls OnHead for platform-published
	// PR heads and submit_verdict hands conclusions to the same dispatcher. Both
	// are wired only when the store implements the round-4 verdict contract
	// (task A); until then verification stays off instead of half-working.
	verificationRepos := newGitHubRepositories(ghProvider)
	var verifier *verify.Dispatcher
	if verifyStore, ok := any(s).(verify.Store); ok {
		var verifierGitHub verify.GitHub = verificationRepos
		if ghApp == nil {
			// Without an App the platform cannot read the PR head or post the
			// verdict comment; keep the store-only behavior instead.
			verifierGitHub = nil
		}
		verifier = verify.New(verify.Options{
			Store:  verifyStore,
			GitHub: verifierGitHub,
			Logger: slog.Default(),
		})
	} else {
		slog.Warn("verification scheduling is disabled: the store does not implement the round-4 verdict methods")
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
	setupManager, err := onboarding.NewManager(onboarding.ManagerConfig{
		Provider:     ghProvider,
		Store:        s,
		Box:          box,
		PublicOrigin: env("PUBLIC_ORIGIN", "http://localhost:8180"),
		Tunnel:       tunnel,
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
	})
	svc := &service.Service{Store: s, Webhooks: s, AppSlugSource: ghProvider.Slug}
	routerOptions := router.Options{AppSlugSource: ghProvider.Slug}
	if verifier != nil {
		routerOptions.OnHead = verifier.HeadChanged
	}
	svc.HiveRouter = router.NewWithOptions(s, routerOptions)

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
	}, jobs.Config{})
	if err != nil {
		return err
	}
	s.Jobs = queue
	go func() {
		if err := queue.Start(ctx); err != nil && ctx.Err() == nil {
			slog.Error("job queue stopped", "error", err)
		}
	}()

	routes := http.NewServeMux()
	routes.Handle("/api/webhooks/github", svc.GitHubWebhookDynamic(ghProvider.WebhookSecret))
	// MCP authenticates with a bearer token only; it never uses the console
	// session (ADR section 12).
	mcpOptions := mcp.Options{Logger: slog.Default()}
	if verifier != nil {
		mcpOptions.SubmitVerdict = verifier.SubmitVerdict
	}
	routes.Handle("/mcp", mcp.NewHandler(s, mcpOptions))
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
	slog.Info("console listening", "address", server.Addr)
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
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
