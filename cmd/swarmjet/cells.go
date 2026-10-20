package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/docker"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forager"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
	"github.com/yyydddkkk/SwarmJet/internal/store"
	"github.com/yyydddkkk/SwarmJet/internal/router"
)

const (
	defaultCellMCPURL = "http://swarmjet:8080/mcp"
	// tokenRefreshMargin leaves room for a clone or push that starts just
	// before the installation token expires.
	tokenRefreshMargin = time.Minute
)

// cellStack owns the Forager supervisor and the Cell runtime in the serve
// process. The supervisor must share the process with the River delivery jobs:
// store.MessageSink hands a delivered message straight to it.
type cellStack struct {
	Supervisor *forager.Supervisor
	Runtime    *docker.Runtime
	Publisher  *docker.Publisher

	logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// githubAppSource resolves the active GitHub App. Provider reads environment
// variables first and the encrypted database row otherwise, so a newly created
// App is visible without restarting serve.
type githubAppSource interface {
	App(context.Context) (*githubapp.App, error)
}

var errGitHubAppUnavailable = errors.New("GitHub App 未配置，请在接入页创建")

// newCellStack builds the runtime, publisher and supervisor. Cells still
// require the Docker socket, but not a GitHub App: serve starts with a warning
// and a Forager that needs GitHub reports errGitHubAppUnavailable.
func newCellStack(s *store.Store, apps githubAppSource, dataDir string, logger *slog.Logger) (*cellStack, error) {
	if err := requireCellDocker(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if apps == nil {
		logger.Warn("SWARMJET_CELLS=1 without a GitHub App source; Foragers will report a configuration error until the App is created in the setup page")
	}
	repos := newGitHubRepositories(apps)

	image := env("AGENT_IMAGE", "swarmjet-agent:local")
	network := env("SWARMJET_CELL_NETWORK", "")
	model := env("SWARMJET_DEFAULT_MODEL", forager.DefaultModel)
	mcpURL := env("SWARMJET_CELL_MCP_URL", defaultCellMCPURL)
	llmURL := env("SWARMJET_CELL_LLM_URL", deriveLLMURL(mcpURL))

	// Every prompt reads the active methodology through this source, so a
	// publish or rollback changes the standing rules on the next turn.
	forager.SetMethodologySource(s)

	runtime := docker.NewRuntime(dataDir)
	runtime.Config.Image = image
	runtime.Config.DockerNetwork = network
	runtime.Tokens = repos.Token
	runtime.Logger = logger

	publisher := &docker.Publisher{
		DataDir:    dataDir,
		Tokens:     repos.Token,
		Repository: repos.Resolve,
		Pulls:      repos,
	}

	recoveryRouter := router.New(s)
	supervisor, err := forager.New(forager.Options{
		ReconcileParkedPR: func(ctx context.Context, hive domain.Hive, agent domain.Agent) error {
			check, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return recoveryRouter.ReconcileParkedPR(check, hive, agent, repos.PullState)
		},
		Store:      s,
		Runtime:    runtime,
		Publisher:  publisher,
		Model:      model,
		MCPURL:     mcpURL,
		LLMBaseURL: llmURL,
		// Streamed output is debug-only this round. Never log Update.Text:
		// it may contain repository content or model output.
		OnUpdate: func(agent domain.Agent, update cell.Update) {
			logger.Debug("cell update",
				"agent", agent.ID,
				"kind", update.Kind,
				"title", update.Title,
				"chars", len([]rune(update.Text)),
			)
		},
		Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	return &cellStack{Supervisor: supervisor, Runtime: runtime, Publisher: publisher, logger: logger}, nil
}

// requireCellDocker checks the local Docker socket before any Forager can be
// woken. The base deploy/compose.yaml deliberately does not mount the socket;
// enabling Cells requires deploy/compose.cells.yaml.
func requireCellDocker() error {
	path := "/var/run/docker.sock"
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		switch {
		case strings.HasPrefix(host, "unix://"):
			path = strings.TrimPrefix(host, "unix://")
		case strings.HasPrefix(host, "unix:"):
			path = strings.TrimPrefix(host, "unix:")
		default:
			// A TCP or SSH Docker endpoint does not depend on the local socket.
			return nil
		}
	}
	if path == "" {
		return errors.New("SWARMJET_CELLS=1 requires a Docker socket; 启用 Cell 需要叠加 deploy/compose.cells.yaml")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("SWARMJET_CELLS=1 requires the Docker socket at %s; 启用 Cell 需要叠加 deploy/compose.cells.yaml: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("SWARMJET_CELLS=1 requires a Docker socket at %s; 启用 Cell 需要叠加 deploy/compose.cells.yaml", path)
	}
	return nil
}

// publicMCPURL is advertised in generated local Agent configurations. The
// console and the MCP endpoint share PUBLIC_ORIGIN.
func publicMCPURL() string {
	origin := strings.TrimRight(env("PUBLIC_ORIGIN", "http://localhost:8180"), "/")
	return origin + "/mcp"
}

// deriveLLMURL derives the Cell-facing model proxy from the MCP URL (same
// host, /llm instead of /mcp) when SWARMJET_CELL_LLM_URL is not set.
func deriveLLMURL(mcpURL string) string {
	u, err := url.Parse(mcpURL)
	if err == nil && u.Scheme != "" && u.Host != "" {
		u.Path = "/llm"
		u.RawPath = ""
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	return "http://swarmjet:8080/llm"
}

// Start launches the supervisor. It must be called after MessageSink is set and
// before the River queue starts delivering messages.
func (c *cellStack) Start(ctx context.Context) {
	child, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		if err := c.Supervisor.Run(child); err != nil && child.Err() == nil {
			c.logger.Error("forager supervisor stopped", "error", err)
		}
	}()
}

// Stop cancels the supervisor and waits for in-flight turns to unwind.
func (c *cellStack) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		<-c.done
	}
}

// githubRepositories resolves repositories and mints repository-scoped
// installation tokens. Only expiring tokens and the repository ID mapping live
// here, in memory; nothing is persisted or logged.
type githubRepositories struct {
	apps githubAppSource

	mu     sync.Mutex
	names  map[int64]string
	tokens map[string]githubapp.Token
}

func newGitHubRepositories(apps githubAppSource) *githubRepositories {
	return &githubRepositories{
		apps:   apps,
		names:  map[int64]string{},
		tokens: map[string]githubapp.Token{},
	}
}

func (r *githubRepositories) app(ctx context.Context) (*githubapp.App, error) {
	if r.apps == nil {
		return nil, errGitHubAppUnavailable
	}
	return r.apps.App(ctx)
}

// Resolve implements docker.RepositoryResolver.
func (r *githubRepositories) Resolve(ctx context.Context, fullName string) (int64, error) {
	r.mu.Lock()
	for id, name := range r.names {
		if name == fullName {
			r.mu.Unlock()
			return id, nil
		}
	}
	r.mu.Unlock()
	app, err := r.app(ctx)
	if err != nil {
		return 0, err
	}
	id, err := app.RepositoryID(ctx, fullName)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.names[id] = fullName
	r.mu.Unlock()
	return id, nil
}

// Token implements docker.TokenSource for both the first clone and publishing.
// The cache key includes the App ID so replacing the App does not keep serving
// a token minted by the old one.
func (r *githubRepositories) Token(ctx context.Context, fullName string) (string, error) {
	app, err := r.app(ctx)
	if err != nil {
		return "", err
	}
	cacheKey := fmt.Sprintf("%d|%s", app.ID(), fullName)
	r.mu.Lock()
	token, ok := r.tokens[cacheKey]
	r.mu.Unlock()
	if ok && time.Until(token.ExpiresAt) > tokenRefreshMargin {
		return token.Token, nil
	}
	token, err = app.RepositoryToken(ctx, fullName)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	if r.tokens == nil {
		r.tokens = map[string]githubapp.Token{}
	}
	r.tokens[cacheKey] = token
	r.mu.Unlock()
	return token.Token, nil
}

// CreateOrUpdatePR implements docker.PullRequests using internal/github. The
// Publisher resolves the repository immediately before calling this method, so
// the full name is already known; the client is built with a token that covers
// exactly that repository.
func (r *githubRepositories) CreateOrUpdatePR(ctx context.Context, id int64, source, target, title, body string) (forge.MergeRequest, error) {
	r.mu.Lock()
	fullName := r.names[id]
	r.mu.Unlock()
	if fullName == "" {
		return forge.MergeRequest{}, fmt.Errorf("cell: repository id %d is unknown; resolve it first", id)
	}
	token, err := r.Token(ctx, fullName)
	if err != nil {
		return forge.MergeRequest{}, err
	}
	client := github.New(func(context.Context) (string, error) { return token, nil })
	return client.CreateOrUpdatePR(ctx, id, source, target, title, body)
}

// PullState reads GitHub directly; local attestations cannot prove closure.
func (r *githubRepositories) PullState(ctx context.Context, fullName string, pr int64) (github.PullState, error) {
	id, err := r.Resolve(ctx, fullName)
	if err != nil {
		return github.PullState{}, err
	}
	token, err := r.Token(ctx, fullName)
	if err != nil {
		return github.PullState{}, err
	}
	client := github.New(func(context.Context) (string, error) { return token, nil })
	return client.GetPullState(ctx, id, pr)
}
