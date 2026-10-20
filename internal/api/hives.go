package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// Hive console REST endpoints (ADR section 18). They read and write the Hive
// data layer directly; this round deliberately keeps them under the existing
// session login instead of adding a second authentication scheme.
const (
	maxHivePage        = 100
	maxForagerPage     = 100
	maxMessagePage     = 100
	defaultMessagePage = 50
	maxHiveNameRunes   = 200
	maxPresetRunes     = 100
	maxBranchRunes     = 200
	maxRepositoryRunes = 200
)

// repositoryPattern is the owner/name shape GitHub accepts for a repository.
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

type hiveOverview struct {
	Hive             domain.Hive `json:"hive"`
	ActiveForagers   int         `json:"activeForagers"`
	PendingApprovals int         `json:"pendingApprovals"`
	InboxBacklog     int         `json:"inboxBacklog"`
}

// hives routes every /api/hives and /api/foragers request. The API server is a
// plain http.HandlerFunc, so this is the smallest routing change that keeps the
// new endpoints out of the legacy tables.
func (a *server) hives(w http.ResponseWriter, r *http.Request) {
	if a.s == nil {
		problem(w, http.StatusServiceUnavailable, "hive storage unavailable")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "hives":
		a.hiveCollection(w, r)
	case len(parts) == 2 && parts[0] == "hives":
		a.hiveItem(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "hives" && parts[2] == "queen":
		a.hiveQueen(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "hives" && parts[2] == "foragers":
		a.hiveForagers(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "hives" && parts[2] == "messages":
		a.hiveMessages(w, r, parts[1])
	case len(parts) >= 3 && parts[0] == "hives" && parts[2] == "methodology":
		a.methodology(w, r, parts[1], parts[3:])
	case len(parts) == 2 && parts[0] == "foragers":
		a.foragerItem(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "foragers" && parts[2] == "cancel":
		a.foragerCancel(w, r, parts[1])
	default:
		problem(w, http.StatusNotFound, "API route not found")
	}
}

func (a *server) hiveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listHives(w, r)
	case http.MethodPost:
		a.createHive(w, r)
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *server) listHives(w http.ResponseWriter, r *http.Request) {
	hives, err := a.s.Hives(r.Context())
	if err != nil {
		hiveFailure(w, err)
		return
	}
	truncated := false
	if len(hives) > maxHivePage {
		hives = hives[:maxHivePage]
		truncated = true
	}
	out := make([]hiveOverview, 0, len(hives))
	for _, hive := range hives {
		overview := hiveOverview{Hive: hive}
		foragers, err := a.s.Foragers(r.Context(), hive.ID)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		for _, forager := range foragers {
			if liveAgentState(forager.State) {
				overview.ActiveForagers++
			}
		}
		// The backlog counts unhandled Hive Inbox entries. Reading one extra row
		// tells us whether the displayed number is capped.
		inbox, err := a.s.Inbox(r.Context(), hive.ID, domain.AddressHive, "", maxHivePage+1)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		for _, message := range inbox {
			if message.State != domain.MessageHandled {
				overview.InboxBacklog++
			}
		}
		if overview.InboxBacklog > maxHivePage {
			overview.InboxBacklog = maxHivePage
		}
		// Approvals are not stored yet (round 2 table work); report zero rather
		// than inventing a number the console cannot act on.
		out = append(out, overview)
	}
	respond(w, http.StatusOK, map[string]any{
		"hives":        out,
		"localAddress": string(a.webLocalAddress(r)),
		"truncated":    truncated,
	})
}

type createHiveRequest struct {
	Name         string             `json:"name"`
	Repository   string             `json:"repository"`
	QueenEnabled bool               `json:"queenEnabled"`
	Config       *domain.HiveConfig `json:"config"`
}

func (a *server) createHive(w http.ResponseWriter, r *http.Request) {
	var req createHiveRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		problem(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > maxHiveNameRunes {
		problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("name is longer than %d characters", maxHiveNameRunes))
		return
	}
	repository := strings.TrimSpace(req.Repository)
	if len(repository) > maxRepositoryRunes || !repositoryPattern.MatchString(repository) {
		problem(w, http.StatusBadRequest, "repository must look like owner/name")
		return
	}
	config := domain.HiveConfig{}
	if req.Config != nil {
		config = *req.Config
	}
	if msg := validateHiveConfig(config); msg != "" {
		problem(w, http.StatusUnprocessableEntity, msg)
		return
	}
	hive, err := a.s.CreateHive(r.Context(), domain.Hive{
		Name:         name,
		Repository:   repository,
		QueenEnabled: req.QueenEnabled,
		Config:       config,
	})
	if err != nil {
		hiveFailure(w, err)
		return
	}
	// CreateHive always inserts the Queen disabled. Enabling it here keeps the
	// row and the Hive flag consistent; the Queen has no Cell until round 3.
	if req.QueenEnabled {
		if queen, err := a.s.Queen(r.Context(), hive.ID); err == nil {
			if err = a.s.TransitionAgent(r.Context(), queen.ID, domain.AgentDisabled, domain.AgentActive); err != nil {
				slog.Error("enable queen after hive create", "hive", hive.ID, "error", err)
			}
		}
	}
	respond(w, http.StatusCreated, map[string]any{"hive": hive, "localAddress": string(a.webLocalAddress(r))})
}

func (a *server) hiveItem(w http.ResponseWriter, r *http.Request, hiveID string) {
	if r.Method == http.MethodPatch {
		a.updateHive(w, r, hiveID)
		return
	}
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	hive, err := a.s.Hive(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"hive": hive, "localAddress": string(a.webLocalAddress(r))})
}

func (a *server) hiveForagers(w http.ResponseWriter, r *http.Request, hiveID string) {
	switch r.Method {
	case http.MethodGet:
		a.listForagers(w, r, hiveID)
	case http.MethodPost:
		a.spawnForager(w, r, hiveID)
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *server) listForagers(w http.ResponseWriter, r *http.Request, hiveID string) {
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	foragers, err := a.s.Foragers(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	truncated := false
	if len(foragers) > maxForagerPage {
		foragers = foragers[:maxForagerPage]
		truncated = true
	}
	if foragers == nil {
		foragers = []domain.Agent{}
	}
	var queen *domain.Agent
	if current, err := a.s.Queen(r.Context(), hiveID); err == nil {
		queen = &current
	} else if !errors.Is(err, domain.ErrNotFound) {
		hiveFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"queen": queen, "foragers": foragers, "truncated": truncated,
		"localAddress": string(a.webLocalAddress(r)),
	})
}

type spawnForagerRequest struct {
	Task        string `json:"task"`
	Preset      string `json:"preset"`
	CLI         string `json:"cli"`
	Branch      string `json:"branch"`
	IssueNumber int64  `json:"issueNumber"`
	ChainID     string `json:"chainId"`
}

func (a *server) spawnForager(w http.ResponseWriter, r *http.Request, hiveID string) {
	var req spawnForagerRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		problem(w, http.StatusBadRequest, "task is required")
		return
	}
	if utf8.RuneCountInString(req.Task) > domain.MaxMessageBody {
		problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("task is longer than %d characters", domain.MaxMessageBody))
		return
	}
	if utf8.RuneCountInString(req.Preset) > maxPresetRunes || utf8.RuneCountInString(req.Branch) > maxBranchRunes {
		problem(w, http.StatusUnprocessableEntity, "preset or branch is too long")
		return
	}
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	chainID := strings.TrimSpace(req.ChainID)
	if chainID == "" {
		chainID = newChainID()
	}
	if utf8.RuneCountInString(chainID) > 200 {
		problem(w, http.StatusBadRequest, "chainId is too long")
		return
	}
	created, err := a.s.CreateForager(r.Context(), domain.Agent{
		HiveID:      hiveID,
		Role:        domain.RoleForager,
		State:       domain.AgentCreating,
		Preset:      req.Preset,
		CLI:         req.CLI,
		Task:        req.Task,
		Branch:      req.Branch,
		IssueNumber: req.IssueNumber,
		ChainID:     chainID,
	})
	if err != nil {
		hiveFailure(w, err)
		return
	}
	message, err := a.s.SendMessage(r.Context(), domain.Message{
		HiveID:  hiveID,
		From:    a.webLocalAddress(r),
		To:      domain.ForagerAddress(created.ID),
		Kind:    domain.MessageTask,
		ChainID: chainID,
		Body:    req.Task,
	})
	if err != nil {
		problem(w, http.StatusInternalServerError, fmt.Sprintf("forager %s was created but its task message failed: %v", created.ID, err))
		return
	}
	respond(w, http.StatusCreated, map[string]any{
		"forager": created, "message": message, "localAddress": string(a.webLocalAddress(r)),
	})
}

func (a *server) foragerItem(w http.ResponseWriter, r *http.Request, foragerID string) {
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	forager, err := a.s.Agent(r.Context(), foragerID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if forager.Role != domain.RoleForager {
		problem(w, http.StatusBadRequest, "agent is not a forager")
		return
	}
	hive, err := a.s.Hive(r.Context(), forager.HiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"forager": forager, "hive": hive, "localAddress": string(a.webLocalAddress(r)),
	})
}

func (a *server) foragerCancel(w http.ResponseWriter, r *http.Request, foragerID string) {
	if r.Method != http.MethodPost {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	forager, err := a.s.Agent(r.Context(), foragerID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if forager.Role != domain.RoleForager {
		problem(w, http.StatusBadRequest, "agent is not a forager")
		return
	}
	if terminalAgentState(forager.State) {
		problem(w, http.StatusConflict, fmt.Sprintf("forager %s is already in terminal state %s", forager.ID, forager.State))
		return
	}
	chainID := forager.ChainID
	if chainID == "" {
		chainID = newChainID()
	}
	message, err := a.s.SendMessage(r.Context(), domain.Message{
		HiveID:  forager.HiveID,
		From:    a.webLocalAddress(r),
		To:      domain.ForagerAddress(forager.ID),
		Kind:    domain.MessageControl,
		ChainID: chainID,
		Body:    "cancel",
	})
	if err != nil {
		hiveFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]any{
		"message": message, "localAddress": string(a.webLocalAddress(r)),
	})
}

func (a *server) hiveMessages(w http.ResponseWriter, r *http.Request, hiveID string) {
	switch r.Method {
	case http.MethodGet:
		a.listMessages(w, r, hiveID)
	case http.MethodPost:
		a.sendMessage(w, r, hiveID)
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *server) listMessages(w http.ResponseWriter, r *http.Request, hiveID string) {
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	rawTo := strings.TrimSpace(r.URL.Query().Get("to"))
	if rawTo == "" {
		problem(w, http.StatusBadRequest, "to is required; use queen, hive, forager:<id> or your local address")
		return
	}
	to, err := a.readAddress(r, hiveID, rawTo)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			hiveFailure(w, err)
			return
		}
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := defaultMessagePage
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			problem(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
		if limit > maxMessagePage {
			limit = maxMessagePage
		}
	}
	messages, err := a.s.Inbox(r.Context(), hiveID, to, r.URL.Query().Get("after"), limit+1)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	truncated := len(messages) > limit
	if truncated {
		messages = messages[:limit]
	}
	cursor := ""
	for i := range messages {
		messages[i].Body = truncateRunes(messages[i].Body, domain.MaxMessageBody)
		cursor = messages[i].ID
	}
	if messages == nil {
		messages = []domain.Message{}
	}
	respond(w, http.StatusOK, map[string]any{
		"messages": messages, "cursor": cursor, "truncated": truncated, "to": string(to),
	})
}

type sendMessageRequest struct {
	To        string               `json:"to"`
	Kind      string               `json:"kind"`
	Body      string               `json:"body"`
	ChainID   string               `json:"chainId"`
	Artifacts []domain.ArtifactRef `json:"artifacts"`
}

func (a *server) sendMessage(w http.ResponseWriter, r *http.Request, hiveID string) {
	var req sendMessageRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		problem(w, http.StatusBadRequest, "body is required")
		return
	}
	if utf8.RuneCountInString(req.Body) > domain.MaxMessageBody {
		problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("body is longer than %d characters", domain.MaxMessageBody))
		return
	}
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	to, err := resolveMessageAddress(req.To)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	if foragerID, ok := foragerIDFromAddress(to); ok {
		if _, err = a.foragerInHive(r, hiveID, foragerID); err != nil {
			hiveFailure(w, err)
			return
		}
	}
	kind := domain.MessageKind(req.Kind)
	if kind == "" {
		kind = domain.MessageTask
	}
	if !validMessageKind(kind) {
		problem(w, http.StatusBadRequest, fmt.Sprintf("invalid kind %q", req.Kind))
		return
	}
	chainID := strings.TrimSpace(req.ChainID)
	if chainID == "" {
		chainID = newChainID()
	}
	if utf8.RuneCountInString(chainID) > 200 {
		problem(w, http.StatusBadRequest, "chainId is too long")
		return
	}
	message, err := a.s.SendMessage(r.Context(), domain.Message{
		HiveID:    hiveID,
		From:      a.webLocalAddress(r),
		To:        to,
		Kind:      kind,
		ChainID:   chainID,
		Body:      req.Body,
		Artifacts: req.Artifacts,
	})
	if err != nil {
		hiveFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]any{
		"message": message, "localAddress": string(a.webLocalAddress(r)),
	})
}

// readAddress resolves a "to" filter. Reading a local inbox is limited to the
// signed-in web identity; other local token inboxes are not exposed here.
func (a *server) readAddress(r *http.Request, hiveID, raw string) (domain.Address, error) {
	if strings.HasPrefix(raw, "local:") {
		if domain.Address(raw) != a.webLocalAddress(r) {
			return "", fmt.Errorf("to must be queen, hive, forager:<id> or your own local address")
		}
		return domain.Address(raw), nil
	}
	to, err := resolveMessageAddress(raw)
	if err != nil {
		return "", err
	}
	if foragerID, ok := foragerIDFromAddress(to); ok {
		if _, err = a.foragerInHive(r, hiveID, foragerID); err != nil {
			return "", err
		}
	}
	return to, nil
}

func (a *server) foragerInHive(r *http.Request, hiveID, foragerID string) (domain.Agent, error) {
	agent, err := a.s.Agent(r.Context(), foragerID)
	if err != nil {
		return domain.Agent{}, err
	}
	if agent.HiveID != hiveID || agent.Role != domain.RoleForager {
		return domain.Agent{}, domain.ErrNotFound
	}
	return agent, nil
}

// webLocalAddress is the Inbox address of the signed-in browser user. Every
// message the console writes uses it, so replies can be routed back here.
func (a *server) webLocalAddress(r *http.Request) domain.Address {
	principal := requestPrincipal(r)
	username := strings.TrimSpace(principal.Email)
	if principal.Breakglass || username == "" {
		username = strings.TrimSpace(a.cfg.Username)
	}
	if username == "" {
		username = "web"
	}
	return domain.LocalAddress("web-" + sanitizeAddressPart(username))
}

var addressPartPattern = regexp.MustCompile(`[^a-zA-Z0-9._@-]+`)

func sanitizeAddressPart(value string) string {
	value = addressPartPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "web"
	}
	if len(value) > 120 {
		value = value[:120]
	}
	return value
}

func newChainID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "chain_" + hex.EncodeToString(b)
}

func resolveMessageAddress(raw string) (domain.Address, error) {
	switch strings.TrimSpace(raw) {
	case "", string(domain.AddressQueen):
		return domain.AddressQueen, nil
	case string(domain.AddressHive):
		return domain.AddressHive, nil
	}
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "forager:") {
		id := strings.TrimPrefix(value, "forager:")
		if id == "" {
			return "", fmt.Errorf("invalid address %q: missing forager id", raw)
		}
		return domain.ForagerAddress(id), nil
	}
	return "", fmt.Errorf("invalid address %q: expected queen, hive or forager:<id>", raw)
}

func foragerIDFromAddress(address domain.Address) (string, bool) {
	const prefix = "forager:"
	if strings.HasPrefix(string(address), prefix) {
		return strings.TrimPrefix(string(address), prefix), true
	}
	return "", false
}

func validMessageKind(kind domain.MessageKind) bool {
	switch kind {
	case domain.MessageEvent, domain.MessageTask, domain.MessageReport,
		domain.MessageQuestion, domain.MessageReply, domain.MessageControl:
		return true
	default:
		return false
	}
}

func liveAgentState(state domain.AgentState) bool {
	return state == domain.AgentCreating || state == domain.AgentActive || state == domain.AgentParked
}

func terminalAgentState(state domain.AgentState) bool {
	return state == domain.AgentCompleted || state == domain.AgentFailed || state == domain.AgentCancelled
}

func validateHiveConfig(config domain.HiveConfig) string {
	limits := map[string]int{
		"maxConcurrentForagers":   config.MaxConcurrentForagers,
		"maxHops":                 config.MaxHops,
		"maxForagersPerChain":     config.MaxForagersPerChain,
		"idleParkMinutes":         config.IdleParkMinutes,
		"hibernateHours":          config.HibernateHours,
		"turnTimeoutMinutes":      config.TurnTimeoutMinutes,
		"foragerMaxLifetimeHours": config.ForagerMaxLifetimeHours,
	}
	for name, value := range limits {
		if value < 0 {
			return name + " must not be negative"
		}
	}
	for index, rule := range config.Triggers {
		switch rule.Event {
		case "issue_labeled":
			if strings.TrimSpace(rule.Label) == "" {
				return fmt.Sprintf("trigger %d: label is required for issue_labeled", index)
			}
		case "issue_comment":
			if strings.TrimSpace(rule.CommentPrefix) == "" {
				return fmt.Sprintf("trigger %d: commentPrefix is required for issue_comment", index)
			}
		default:
			return fmt.Sprintf("trigger %d: event must be issue_labeled or issue_comment", index)
		}
		if utf8.RuneCountInString(rule.Preset) > maxPresetRunes {
			return fmt.Sprintf("trigger %d: preset is too long", index)
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

// hiveFailure maps the Hive data-layer errors to HTTP status codes with a
// readable message.
func hiveFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, store.ErrNotFound):
		problem(w, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrConflict), errors.Is(err, store.ErrConflict):
		problem(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrHiveLimit), errors.Is(err, domain.ErrChainLimit):
		problem(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, domain.ErrHopLimit), errors.Is(err, domain.ErrPeerMessage), errors.Is(err, domain.ErrMessageTooLarge):
		problem(w, http.StatusUnprocessableEntity, err.Error())
	default:
		failure(w, err)
	}
}
