package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"swarmjet/internal/agent"
	"swarmjet/internal/api"
	"swarmjet/internal/credentials"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/initflow"
	"swarmjet/internal/integration"
	"swarmjet/internal/maintenance"
	"swarmjet/internal/managedmeta"
	"swarmjet/internal/networkconfig"
	"swarmjet/internal/nodeenvironment"
	"swarmjet/internal/pipeline"
	"swarmjet/internal/runtimeadmin"
	"swarmjet/internal/secretbox"
	"swarmjet/internal/service"
	"swarmjet/internal/store"
	"swarmjet/internal/workspace"
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

// runNode starts an execution node: it owns its DATA_DIR and Docker, and the
// control plane reaches it over the authenticated HTTP protocol. It does not
// need PostgreSQL or Temporal; it only registers with the control plane.
func runNode(ctx context.Context) error {
	id := required("NODE_ID")
	if !domain.IDPattern.MatchString(id) {
		return fmt.Errorf("invalid NODE_ID")
	}
	secret := required("NODE_SECRET")
	if len(secret) < 32 {
		return fmt.Errorf("NODE_SECRET must contain at least 32 characters")
	}
	advertise := required("NODE_ADVERTISE_URL")
	control := env("CONTROL_PLANE_URL", "http://127.0.0.1:8080")
	data, e := filepath.Abs(env("DATA_DIR", "./data"))
	if e != nil {
		return e
	}
	if e = os.MkdirAll(data, 0700); e != nil {
		return e
	}
	capacity, e := strconv.Atoi(env("NODE_CAPACITY", "2"))
	if e != nil || capacity < 1 {
		return fmt.Errorf("invalid NODE_CAPACITY")
	}
	wm := workspace.New(data, env("META_COMMAND", "meta"))
	wm.Env = map[string]string{"GIT_ASKPASS": env("SWARMJET_GIT_ASKPASS", "/usr/local/bin/swarmjet-askpass")}
	im := integration.New(data)
	im.Env = map[string]string{}
	for _, k := range strings.Split(os.Getenv("INTEGRATION_ENV_ALLOWLIST"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			im.Env[k] = os.Getenv(k)
		}
	}
	environment := nodeenvironment.New(data, id)
	local := &execution.LocalNode{NodeID: id, AgentBackend: &execution.EnvironmentBackend{AgentBackend: agent.New(data), Env: nodeAgentEnv()}, Workspace: wm, Integration: im, DataDir: data}
	local.StaticAnalysis = func(ctx context.Context, p execution.AnalysisParams) (execution.AnalysisResult, error) {
		return initflow.AnalyzeAtNode(ctx, data, local.AgentBackend, p, nil)
	}
	// MaxBody accommodates a base64 managed Meta-Repo bundle in addition to the
	// small agent and integration requests.
	rpc := &execution.Handler{Node: local, Secret: secret, MaxBody: 32 << 20}
	var adminMu sync.Mutex
	var admin http.Handler
	adminRequests := 0
	adminVersion := ""
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/runtime/") {
			rpc.ServeHTTP(w, r)
			return
		}
		active := environment.Active()
		ready := active != nil
		if active == nil {
			if r.URL.Path != "/runtime/network-test" {
				http.Error(w, "节点镜像尚未就绪", 503)
				return
			}
			active = environment.Desired()
			if active == nil {
				http.Error(w, "节点配置尚未下发", 503)
				return
			}
		}
		key := fmt.Sprintf("%t/%d/%s/%d", ready, active.Config.Version, active.Network.ID, active.Network.Version)
		adminMu.Lock()
		busy := adminRequests > 0
		if h, ok := admin.(interface{ TerminalActive() bool }); ok {
			busy = busy || h.TerminalActive()
		}
		if admin == nil || adminVersion != key && !busy {
			vars := nodeAgentEnv()
			for k, v := range active.Env {
				vars[k] = v
			}
			admin = runtimeadmin.New(runtimeadmin.Config{DataDir: data, Image: active.Config.Image, Secret: secret, Env: vars, DockerNetwork: "sj-node-" + id, Revision: key, DisableImages: true})
			adminVersion = key
		}
		h := admin
		adminRequests++
		defer func() { adminMu.Lock(); adminRequests--; adminMu.Unlock() }()
		adminMu.Unlock()
		http.StripPrefix("/runtime", h).ServeHTTP(w, r)
	})
	server := &http.Server{Addr: env("NODE_LISTEN_ADDRESS", ":8092"), Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	heartbeat := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	go func() {
		send := func() {
			b, _ := json.Marshal(domain.Node{ID: id, Address: advertise, Capacity: capacity, Version: "swarmjet-node-v2", Readiness: environment.Status()})
			hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(hctx, http.MethodPost, strings.TrimRight(control, "/")+"/api/nodes", bytes.NewReader(b))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-SwarmJet-Node-Secret", secret)
			resp, err := heartbeat.Do(req)
			if err != nil {
				slog.Warn("node heartbeat failed", "error", err)
				return
			}
			if resp.StatusCode == http.StatusOK {
				var reply nodeenvironment.Reply
				if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply) == nil && reply.Desired != nil {
					environment.Apply(ctx, *reply.Desired)
				}
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				slog.Warn("node heartbeat rejected", "status", resp.StatusCode)
			}
		}
		send()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				send()
			}
		}
	}()
	slog.Info("execution node listening", "id", id, "address", server.Addr, "advertise", advertise, "capacity", capacity)
	e = server.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "help" || mode == "--help" {
		fmt.Println("swarmjet serve | worker | node | builder | mihomo | migrate | doctor | legacy-inventory <state.json>")
		return nil
	}
	if mode == "legacy-inventory" {
		if len(os.Args) != 3 {
			return fmt.Errorf("provide legacy state.json")
		}
		b, e := os.ReadFile(os.Args[2])
		if e != nil {
			return e
		}
		var old map[string]any
		if e = json.Unmarshal(b, &old); e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"mode": "manual-takeover", "entries": old, "instructions": "Inspect existing branches and MRs before removing ai:* labels or enabling autonomous; existing executions are not automatically resumed."})
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if mode == "mihomo" {
		return runMihomo(ctx)
	}
	if mode == "node" {
		return runNode(ctx)
	}
	s, e := store.Open(ctx, required("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer s.Close()
	if mode == "migrate" {
		return s.Migrate(ctx)
	}
	// The master key is deployment-managed and must be stable: a new key on
	// startup would make previously saved connection tokens undecryptable.
	box, boxErr := secretbox.New(os.Getenv("SWARMJET_MASTER_KEY"))
	if boxErr != nil {
		slog.Warn("saved GitLab connections disabled; set SWARMJET_MASTER_KEY to a stable 32-byte key", "reason", boxErr.Error())
		box = nil
	}
	nodeSecret := func(ctx context.Context, id string) (string, error) {
		return networkconfig.NodeSecret(ctx, s, box, id, os.Getenv("NODE_SECRET"))
	}
	if mode == "builder" {
		return runBuilder(ctx, s, box)
	}
	resolver := &credentials.Resolver{Store: s, Box: box, Env: os.Getenv}
	// bound resolves a project token and refuses to send a saved connection to a
	// GitLab address different from the one it was verified against.
	bound := func(p domain.Project, merge bool) (string, error) {
		ref := p.CredentialReference()
		if merge {
			ref = p.MergeCredentialReference()
		}
		return resolver.ResolveFor(ref, p.GitLabURL)
	}
	boundProject := func(p domain.Project) (string, error) { return bound(p, false) }
	tc, e := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", "localhost:7233"), Namespace: env("TEMPORAL_NAMESPACE", "default")})
	if e != nil {
		return e
	}
	defer tc.Close()
	if mode == "doctor" {
		_, e = tc.CheckHealth(ctx, &client.CheckHealthRequest{})
		if e != nil {
			return e
		}
		fmt.Println("PostgreSQL and Temporal reachable; use console project checks for repository configuration")
		return nil
	}
	data, e := filepath.Abs(env("DATA_DIR", "./data"))
	if e != nil {
		return e
	}
	if e = os.MkdirAll(data, 0700); e != nil {
		return e
	}
	if mode == "serve" {
		adminUser := env("ADMIN_USERNAME", "admin")
		password := required("ADMIN_PASSWORD")
		if len(password) < 12 {
			return fmt.Errorf("ADMIN_PASSWORD must contain at least 12 characters")
		}
		initNodes := &execution.Pool{Lookup: s, Secret: os.Getenv("NODE_SECRET"), SecretFor: nodeSecret}
		initializer := &initflow.Manager{Store: s, DataDir: data, Managed: managedmeta.New(data), Credential: resolver.Resolve, CredentialFor: boundProject, Logger: slog.Default()}
		initializer.CancelAnalysis = func(ctx context.Context, in domain.Initialization) error {
			n, e := initNodes.Node(ctx, in.NodeID)
			if e != nil {
				return e
			}
			return n.CancelTask(ctx, in.ProjectID)
		}
		labelsWake := make(chan struct{}, 1)
		handler := api.New(s, api.Config{Username: adminUser, Password: password, AllowedOrigin: env("PUBLIC_ORIGIN", "http://localhost:8180"), DataDir: data, StaticDir: env("STATIC_DIR", "web/dist"), SecureCookies: env("SECURE_COOKIES", "false") == "true", ResolveCredential: resolver.Resolve, Box: box, Initializer: initializer, DefaultAgentImage: env("AGENT_IMAGE", "swarmjet-agent:local"), RuntimeURL: os.Getenv("RUNTIME_ADMIN_URL"), BuilderURL: os.Getenv("BUILDER_ADMIN_URL"), MihomoURL: os.Getenv("MIHOMO_ADMIN_URL"), MihomoSecret: os.Getenv("MIHOMO_ADMIN_SECRET"), RuntimeSecret: os.Getenv("RUNTIME_ADMIN_SECRET"), NodeSecret: os.Getenv("NODE_SECRET"), RequireNode: os.Getenv("EXECUTION_MODE") == "distributed", Nodes: &execution.Pool{Lookup: s, Secret: os.Getenv("NODE_SECRET"), SecretFor: nodeSecret}, LabelsWake: labelsWake})
		svc := &service.Service{DataDir: data, Store: s, Temporal: tc, ResolveCredential: resolver.Resolve, ProjectCredential: bound, Initializer: initializer, RequireNode: os.Getenv("EXECUTION_MODE") == "distributed", Wake: make(chan struct{}, 1), LabelsWake: labelsWake}
		go svc.Run(ctx)
		go svc.SyncLabels(ctx)
		a := &pipeline.Activities{Store: s, Credential: resolver.Resolve, ProjectCredential: bound}
		go svc.Sync(ctx, a)
		routes := http.NewServeMux()
		routes.Handle("/api/webhooks/gitlab", svc.Webhook(os.Getenv("GITLAB_WEBHOOK_SECRET")))
		routes.Handle("/", handler)
		server := &http.Server{Addr: env("LISTEN_ADDRESS", ":8080"), Handler: routes, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
		go func() {
			<-ctx.Done()
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(c)
		}()
		slog.Info("console listening", "address", server.Addr)
		e = server.ListenAndServe()
		if e == http.ErrServerClosed {
			return nil
		}
		return e
	}
	if mode != "worker" {
		return fmt.Errorf("unknown mode %s", mode)
	}
	wm := workspace.New(data, env("META_COMMAND", "meta"))
	wm.Env = map[string]string{"GIT_ASKPASS": env("SWARMJET_GIT_ASKPASS", "/usr/local/bin/swarmjet-askpass")}
	im := integration.New(data)
	im.Env = map[string]string{}
	for _, k := range strings.Split(os.Getenv("INTEGRATION_ENV_ALLOWLIST"), ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			im.Env[k] = os.Getenv(k)
		}
	}
	n, e := strconv.Atoi(env("TASK_CONCURRENCY", "2"))
	if e != nil || n < 1 {
		return fmt.Errorf("invalid TASK_CONCURRENCY")
	}
	agentEnv := map[string]string{}
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if v := os.Getenv(k); v != "" {
			agentEnv[k] = v
		}
	}
	for _, k := range strings.Split(env("AGENT_ENV_ALLOWLIST", "OPENAI_API_KEY,CODEX_API_KEY,ANTHROPIC_API_KEY,CLAUDE_CODE_OAUTH_TOKEN,DEEPSEEK_API_KEY,GEMINI_API_KEY,AGY_ADC_AUTH,GOOGLE_GEMINI_BASE_URL,OPENCODE_CONFIG_CONTENT"), ",") {
		k = strings.TrimSpace(k)
		if v := os.Getenv(k); k != "" && v != "" {
			agentEnv[k] = v
		}
	}
	localAgents := agent.New(data)
	nodes := &execution.Pool{Lookup: s, Secret: os.Getenv("NODE_SECRET"), SecretFor: nodeSecret}
	a := &pipeline.Activities{Store: s, Workspace: wm, Agents: localAgents, Integration: im, Nodes: nodes, IntegrationEnv: im.Env, DataDir: data, Concurrency: n, Credential: resolver.Resolve, ProjectCredential: bound, AgentEnv: agentEnv, NetworkEnv: func(ctx context.Context, ref domain.NetworkVersion) (map[string]string, error) {
		return networkconfig.Resolve(ctx, s, box, ref)
	}}
	// Initialization is resumed here so a worker restart recovers running jobs.
	initMgr := &initflow.Manager{Store: s, DataDir: data, Managed: managedmeta.New(data), Credential: resolver.Resolve, CredentialFor: boundProject, Agents: localAgents, VerifyImage: os.Getenv("INIT_VERIFY_IMAGE"), Logger: slog.Default()}
	if os.Getenv("EXECUTION_MODE") == "distributed" || os.Getenv("BUILDER_ADMIN_URL") != "" {
		initMgr.RemoteAnalysis = func(ctx context.Context, in *domain.Initialization, p domain.Project) (execution.AnalysisResult, error) {
			assigned, e := s.AssignInitializationNode(ctx, *in, p)
			if e != nil {
				return execution.AnalysisResult{}, e
			}
			*in = assigned
			n, e := nodes.Node(ctx, in.NodeID)
			if e != nil {
				return execution.AnalysisResult{}, e
			}
			remote, ok := n.(interface {
				Analyze(context.Context, execution.AnalysisParams) (execution.AnalysisResult, error)
			})
			if !ok {
				return execution.AnalysisResult{}, fmt.Errorf("节点不支持静态初始化")
			}
			vars, e := networkconfig.Resolve(ctx, s, box, in.Network)
			if e != nil {
				return execution.AnalysisResult{}, e
			}
			token, e := bound(p, false)
			if e != nil {
				return execution.AnalysisResult{}, e
			}
			vars["GITLAB_TOKEN"] = token
			return remote.Analyze(ctx, execution.AnalysisParams{Initialization: *in, Project: p, Env: vars})
		}
	}
	initMgr.CancelAnalysis = func(ctx context.Context, in domain.Initialization) error {
		n, e := nodes.Node(ctx, in.NodeID)
		if e != nil {
			return e
		}
		return n.CancelTask(ctx, in.ProjectID)
	}
	go initMgr.Run(ctx)
	if secret := os.Getenv("RUNTIME_ADMIN_SECRET"); secret != "" {
		if len(secret) < 32 {
			return fmt.Errorf("RUNTIME_ADMIN_SECRET must contain at least 32 characters")
		}
		admin := &http.Server{Addr: env("RUNTIME_ADMIN_LISTEN", ":8091"), Handler: runtimeadmin.New(runtimeadmin.Config{DataDir: data, Image: env("AGENT_IMAGE", "swarmjet-agent:local"), Secret: secret, Env: agentEnv, DisableImages: os.Getenv("BUILDER_ADMIN_URL") != ""}), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
		go func() {
			if err := admin.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("runtime admin stopped", "error", err)
				stop()
			}
		}()
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = admin.Shutdown(c)
		}()
	}
	failedDays, _ := strconv.Atoi(env("FAILED_KEEP_DAYS", "7"))
	reportDays, _ := strconv.Atoi(env("REPORT_KEEP_DAYS", "30"))
	if failedDays < 1 || reportDays < 1 {
		return fmt.Errorf("retention days must be positive")
	}
	cleaner := &maintenance.Manager{Store: s, Workspace: wm, Agents: localAgents, Nodes: nodes, DataDir: data, FailedDays: failedDays, ReportDays: reportDays}
	go cleaner.Run(ctx)
	w := worker.New(tc, pipeline.Queue, worker.Options{MaxConcurrentActivityExecutionSize: n*8 + 8, WorkerStopTimeout: 30 * time.Second})
	w.RegisterWorkflowWithOptions(pipeline.WorkTask, workflow.RegisterOptions{Name: pipeline.WorkflowName})
	w.RegisterActivityWithOptions(a.Step, activity.RegisterOptions{Name: "Step"})
	w.RegisterActivityWithOptions(a.SaveTask, activity.RegisterOptions{Name: "SaveTask"})
	if e = w.Start(); e != nil {
		return e
	}
	defer w.Stop()
	<-ctx.Done()
	return nil
}
