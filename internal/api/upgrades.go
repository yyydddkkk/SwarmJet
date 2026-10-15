package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/imagebuild"
	"github.com/yyydddkkk/SwarmJet/internal/networkconfig"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type upgradeTarget struct {
	Tag       string `json:"tag"`
	Reference string `json:"reference,omitempty"`
	Digest    string `json:"digest,omitempty"`
	CommitSHA string `json:"commitSHA,omitempty"`
	Platform  string `json:"platform,omitempty"`
}

var upgradeReleaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func newerRelease(target, current string) bool {
	if !upgradeReleaseTag.MatchString(current) {
		return true /* bootstrap from an unversioned installation */
	}
	if !upgradeReleaseTag.MatchString(target) {
		return false
	}
	oldParts, newParts := strings.Split(current[1:], "."), strings.Split(target[1:], ".")
	for i := 0; i < 3; i++ {
		oldNumber, oldErr := strconv.ParseUint(oldParts[i], 10, 64)
		newNumber, newErr := strconv.ParseUint(newParts[i], 10, 64)
		if oldErr != nil || newErr != nil {
			return false
		}
		if newNumber != oldNumber {
			return newNumber > oldNumber
		}
	}
	return false
}

type upgradeCheck struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type upgradeNode struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	CurrentImage string     `json:"currentImage,omitempty"`
	TargetImage  string     `json:"targetImage,omitempty"`
	OperationID  string     `json:"operationId,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	Error        string     `json:"error,omitempty"`
}

type upgradePlan struct {
	ID                 string         `json:"id"`
	Tag                string         `json:"tag"`
	Status             string         `json:"status"`
	Stage              string         `json:"stage,omitempty"`
	Current            upgradeTarget  `json:"current"`
	Target             upgradeTarget  `json:"target"`
	Preflight          []upgradeCheck `json:"preflight"`
	Nodes              []upgradeNode  `json:"nodes"`
	BuildID            string         `json:"buildId,omitempty"`
	ControlOperationID string         `json:"controlOperationId,omitempty"`
	ControlStartedAt   *time.Time     `json:"controlStartedAt,omitempty"`
	BackupDir          string         `json:"backupDir,omitempty"`
	NeedsRestore       bool           `json:"needsRestore,omitempty"`
	Error              string         `json:"error,omitempty"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	StartedAt          *time.Time     `json:"startedAt,omitempty"`
}

func decodeUpgrade(record store.UpgradeRecord) (upgradePlan, error) {
	var plan upgradePlan
	if err := json.Unmarshal(record.Body, &plan); err != nil {
		return plan, err
	}
	plan.ID, plan.Status, plan.CreatedAt, plan.UpdatedAt = record.ID, record.Status, record.CreatedAt, record.UpdatedAt
	return plan, nil
}

func (a *server) saveUpgrade(ctx context.Context, plan *upgradePlan, active, maintenance bool) error {
	plan.UpdatedAt = time.Now().UTC()
	return a.s.SaveUpgrade(ctx, plan.ID, plan.Status, active, maintenance, plan)
}

func (a *server) upgradeRequest(ctx context.Context, base, secret, method, path string, input, output any, node bool) error {
	if base == "" || secret == "" {
		return errors.New("升级器未安装或凭据未配置")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("升级器地址无效")
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if node {
		req.Header.Set("X-SwarmJet-Node-Secret", secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	timeout := 20 * time.Second
	if path == "/v1/preflight" {
		timeout = 5 * time.Minute
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&problem)
		if problem.Error == "" {
			problem.Error = resp.Status
		}
		return fmt.Errorf("升级器: %s", problem.Error)
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(output)
}

func (a *server) builderRequest(ctx context.Context, method, path string, input, output any) error {
	if a.cfg.BuilderURL == "" || a.cfg.RuntimeSecret == "" {
		return errors.New("构建服务未配置")
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.BuilderURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-SwarmJet-Runtime-Secret", a.cfg.RuntimeSecret)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	timeout := 45 * time.Second
	if path == "/images/platform/releases/builds" {
		timeout = 3 * time.Minute
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&problem)
		if problem.Error == "" {
			problem.Error = resp.Status
		}
		return errors.New(problem.Error)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(output)
}

func (a *server) controlUpdater(ctx context.Context, method, path string, input, output any) error {
	return a.upgradeRequest(ctx, a.cfg.UpdaterURL, a.cfg.UpdaterSecret, method, path, input, output, false)
}

func (a *server) nodeUpdater(ctx context.Context, id, method, path string, input, output any) error {
	node, err := a.s.Node(ctx, id)
	if err != nil {
		return err
	}
	secret, err := networkconfig.NodeSecret(ctx, a.s, a.cfg.Box, id, a.cfg.NodeSecret)
	if err != nil {
		return err
	}
	return a.upgradeRequest(ctx, strings.TrimRight(node.Address, "/")+"/upgrade", secret, method, path, input, output, true)
}

func (a *server) upgrades(w http.ResponseWriter, r *http.Request) {
	if a.s == nil {
		problem(w, 503, "升级记录存储不可用")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/system/upgrades")
	switch {
	case path == "/releases" && r.Method == http.MethodGet:
		var releases struct {
			Tags []struct {
				Tag string `json:"tag"`
			} `json:"tags"`
		}
		catalogErr := a.builderRequest(r.Context(), "GET", "/images/platform/releases", nil, &releases)
		list := make([]upgradeTarget, 0, len(releases.Tags))
		for _, tag := range releases.Tags {
			list = append(list, upgradeTarget{Tag: tag.Tag})
		}
		current := upgradeTarget{Tag: a.cfg.PlatformVersion, CommitSHA: a.cfg.PlatformCommit, Reference: a.cfg.PlatformImage}
		if i := strings.Index(a.cfg.PlatformImage, "@sha256:"); i >= 0 {
			current.Digest = a.cfg.PlatformImage[i+1:]
		}
		statuses, _ := a.s.NodeStatuses(r.Context())
		nodes := []map[string]any{}
		for _, status := range statuses {
			nodes = append(nodes, map[string]any{"id": status.Node.ID, "online": status.Online, "platformImage": status.Node.Readiness.PlatformImage, "version": status.Node.Version})
		}
		view := map[string]any{"current": current, "releases": list, "nodes": nodes}
		if catalogErr != nil {
			view["catalogError"] = catalogErr.Error()
		}
		respond(w, 200, view)
	case path == "" && r.Method == http.MethodGet:
		records, err := a.s.Upgrades(r.Context(), 20)
		if err != nil {
			failure(w, err)
			return
		}
		plans := []upgradePlan{}
		for _, record := range records {
			plan, err := decodeUpgrade(record)
			if err != nil {
				failure(w, err)
				return
			}
			plans = append(plans, plan)
		}
		respond(w, 200, map[string]any{"upgrades": plans})
	case path == "/prepare" && r.Method == http.MethodPost:
		a.prepareUpgrade(w, r)
	case strings.HasPrefix(path, "/"):
		if r.Method == http.MethodPost {
			a.upgradeMu.Lock()
			defer a.upgradeMu.Unlock()
		}
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
			problem(w, 404, "升级记录不存在")
			return
		}
		record, err := a.s.Upgrade(r.Context(), parts[0])
		if err != nil {
			failure(w, err)
			return
		}
		plan, err := decodeUpgrade(record)
		if err != nil {
			failure(w, err)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			respond(w, 200, plan)
			return
		}
		if len(parts) != 2 || r.Method != http.MethodPost {
			problem(w, 405, "method not allowed")
			return
		}
		switch parts[1] {
		case "start":
			if plan.Status != "ready" || plan.StartedAt != nil {
				problem(w, 409, "升级尚未准备完成")
				return
			}
			checks, ok := a.upgradePreflight(r.Context(), &plan)
			plan.Preflight = checks
			if !ok {
				_ = a.saveUpgrade(r.Context(), &plan, true, false)
				problem(w, 409, "升级预检未通过")
				return
			}
			now := time.Now().UTC()
			plan.StartedAt, plan.Status, plan.Stage = &now, "running", "draining"
			if err = a.saveUpgrade(r.Context(), &plan, true, true); err != nil {
				failure(w, err)
				return
			}
			respond(w, 202, plan)
		case "resume":
			if plan.Status != "failed" || plan.StartedAt == nil {
				problem(w, 409, "升级不可继续")
				return
			}
			if plan.NeedsRestore {
				problem(w, 409, "数据迁移后失败，请先按备份记录恢复数据")
				return
			}
			if plan.Stage == "upgrading_control" {
				var previous updaterOperation
				if err := a.controlUpdater(r.Context(), "GET", "/v1/status?operationId="+url.QueryEscape(plan.ControlOperationID), nil, &previous); err != nil {
					problem(w, 503, "无法核对控制面升级器状态: "+err.Error())
					return
				}
				if previous.Status == "complete" {
					plan.Stage = "upgrading_nodes"
				} else if previous.NeedsRestore || (previous.CompletedStep("quiesce") && !previous.RolledBack) {
					problem(w, 409, "控制面未安全回退，需先在宿主机处理失败状态")
					return
				} else {
					plan.ControlOperationID = ""
					plan.ControlStartedAt = nil
					plan.Stage = "draining"
				}
			}
			if plan.Stage == "upgrading_nodes" {
				for i := range plan.Nodes {
					if plan.Nodes[i].Status == "failed" {
						var previous updaterOperation
						if err := a.nodeUpdater(r.Context(), plan.Nodes[i].ID, "GET", "/v1/status?operationId="+url.QueryEscape(plan.Nodes[i].OperationID), nil, &previous); err != nil {
							problem(w, 503, "无法核对节点升级器状态: "+err.Error())
							return
						}
						if previous.Status == "complete" {
							fresh, err := a.s.Node(r.Context(), plan.Nodes[i].ID)
							if err != nil || fresh.Readiness.PlatformImage != plan.Target.Reference || time.Since(fresh.LastSeen) > domain.NodeOnlineAfter {
								problem(w, 409, "节点升级器已完成，但目标版本未恢复心跳")
								return
							}
							plan.Nodes[i].Status, plan.Nodes[i].Error = "succeeded", ""
							break
						}
						if previous.CompletedStep("quiesce") && !previous.RolledBack {
							problem(w, 409, "节点未安全回退，需先在节点宿主机修复")
							return
						}
						plan.Nodes[i].OperationID = ""
						plan.Nodes[i].Status = "pending"
						plan.Nodes[i].Error = ""
						plan.Nodes[i].StartedAt = nil
						break
					}
				}
			}
			plan.Status, plan.Error = "running", ""
			if err = a.saveUpgrade(r.Context(), &plan, true, true); err != nil {
				failure(w, err)
				return
			}
			respond(w, 202, plan)
		case "cancel":
			if plan.StartedAt != nil || plan.Status == "succeeded" {
				problem(w, 409, "已开始切换的升级不可取消")
				return
			}
			plan.Status, plan.Stage = "cancelled", "cancelled"
			if err = a.saveUpgrade(r.Context(), &plan, false, false); err != nil {
				failure(w, err)
				return
			}
			respond(w, 200, plan)
		case "abort":
			if plan.StartedAt == nil || plan.Status != "running" || plan.Stage != "draining" {
				problem(w, 409, "升级已进入切换阶段，不能取消")
				return
			}
			plan.Status, plan.Stage = "cancelled", "cancelled"
			if err = a.saveUpgrade(r.Context(), &plan, false, false); err != nil {
				failure(w, err)
				return
			}
			respond(w, 200, plan)
		default:
			problem(w, 404, "升级操作不存在")
		}
	default:
		problem(w, 405, "method not allowed")
	}
}

func (a *server) prepareUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag string `json:"tag"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Tag) > 64 || !upgradeReleaseTag.MatchString(req.Tag) {
		problem(w, 400, "发布标签无效")
		return
	}
	id, err := newID()
	if err != nil {
		failure(w, err)
		return
	}
	now := time.Now().UTC()
	plan := upgradePlan{ID: id, Tag: req.Tag, Status: "preparing", Stage: "building", Current: upgradeTarget{Tag: a.cfg.PlatformVersion, CommitSHA: a.cfg.PlatformCommit, Reference: a.cfg.PlatformImage}, Target: upgradeTarget{Tag: req.Tag}, Preflight: []upgradeCheck{}, Nodes: []upgradeNode{}, CreatedAt: now, UpdatedAt: now}
	if err = a.s.CreateUpgrade(r.Context(), id, plan.Status, plan); err != nil {
		failure(w, err)
		return
	}
	var build imagebuild.Build
	if err = a.builderRequest(r.Context(), "POST", "/images/platform/releases/builds", map[string]string{"tag": req.Tag}, &build); err != nil {
		plan.Status, plan.Error = "failed", err.Error()
		_ = a.saveUpgrade(r.Context(), &plan, true, false)
		problem(w, 503, err.Error())
		return
	}
	plan.BuildID = build.ID
	if err = a.saveUpgrade(r.Context(), &plan, true, false); err != nil {
		failure(w, err)
		return
	}
	respond(w, 202, plan)
}

func (a *server) upgradePreflight(ctx context.Context, plan *upgradePlan) ([]upgradeCheck, bool) {
	checks := []upgradeCheck{}
	add := func(id, label string, err error) {
		check := upgradeCheck{ID: id, Label: label, Status: "passed", Message: "检查通过"}
		if err != nil {
			check.Status, check.Message = "blocked", err.Error()
		}
		checks = append(checks, check)
	}
	if !domain.RepositoryDigest.MatchString(plan.Target.Reference) {
		add("target", "目标镜像摘要", errors.New("尚未构建出固定摘要的平台镜像"))
	} else {
		add("target", "目标镜像摘要", nil)
	}
	if !newerRelease(plan.Tag, a.cfg.PlatformVersion) {
		add("version", "升级方向", errors.New("目标版本必须高于正在运行的版本"))
	} else {
		add("version", "升级方向", nil)
	}
	if plan.Current.Reference != a.cfg.PlatformImage {
		add("current", "当前部署版本", errors.New("准备升级后控制面镜像已变化，请重新准备"))
	} else {
		add("current", "当前部署版本", nil)
	}
	var health map[string]any
	add("control-updater", "控制面升级器", a.controlUpdater(ctx, "GET", "/healthz", nil, &health))
	if plan.Target.Reference != "" {
		var hostPreflight map[string]any
		add("host-preflight", "宿主机备份与发布兼容性", a.controlUpdater(ctx, "POST", "/v1/preflight", map[string]string{"image": plan.Target.Reference}, &hostPreflight))
	}
	statuses, err := a.s.NodeStatuses(ctx)
	add("nodes", "执行节点清单", err)
	if err == nil {
		plan.Nodes = []upgradeNode{}
		for _, status := range statuses {
			address, _ := url.Parse(status.Node.Address)
			if status.Node.ID == "local" || address != nil && address.Hostname() == "local-node" ||
				status.Config != nil && (status.Config.Archived || !status.Config.Enabled) {
				continue
			}
			entry := upgradeNode{ID: status.Node.ID, Status: "pending", CurrentImage: status.Node.Readiness.PlatformImage, TargetImage: plan.Target.Reference}
			plan.Nodes = append(plan.Nodes, entry)
			if !status.Online {
				add("node-"+entry.ID, "节点 "+entry.ID, errors.New("节点已启用但当前离线"))
				continue
			}
			if status.Node.ProtocolVersion < 2 {
				add("node-"+entry.ID, "节点 "+entry.ID, errors.New("节点协议版本不支持升级"))
				continue
			}
			if status.Node.Readiness.PlatformImage != plan.Target.Reference && !newerRelease(plan.Tag, status.Node.Version) {
				add("node-"+entry.ID, "节点 "+entry.ID, errors.New("目标版本不高于节点当前版本"))
				continue
			}
			if plan.Target.Platform != "" && plan.Target.Platform != "linux/"+status.Node.Readiness.Architecture {
				add("node-"+entry.ID, "节点 "+entry.ID, errors.New("目标镜像不支持节点架构"))
				continue
			}
			var nodeHealth map[string]any
			add("node-"+entry.ID, "节点 "+entry.ID, a.nodeUpdater(ctx, entry.ID, "GET", "/healthz", nil, &nodeHealth))
		}
	}
	ok := true
	for _, check := range checks {
		if check.Status == "blocked" {
			ok = false
		}
	}
	return checks, ok
}

func (a *server) runUpgradeCoordinator(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		a.advanceActiveUpgrade(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *server) advanceActiveUpgrade(parent context.Context) {
	a.upgradeMu.Lock()
	defer a.upgradeMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 6*time.Minute)
	defer cancel()
	record, err := a.s.ActiveUpgrade(ctx)
	if err != nil {
		return
	}
	plan, err := decodeUpgrade(record)
	if err != nil {
		return
	}
	if plan.Status != "preparing" && plan.Status != "running" {
		return
	}
	if err = a.advanceUpgrade(ctx, &plan); err != nil {
		plan.Status, plan.Error = "failed", err.Error()
		_ = a.saveUpgrade(ctx, &plan, true, record.Maintenance)
	}
}

type updaterOperation struct {
	OperationID  string   `json:"operationId"`
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
	Step         string   `json:"step,omitempty"`
	BackupDir    string   `json:"backupDir,omitempty"`
	NeedsRestore bool     `json:"needsRestore,omitempty"`
	RolledBack   bool     `json:"rolledBack,omitempty"`
	Completed    []string `json:"completed,omitempty"`
}

func (o updaterOperation) CompletedStep(name string) bool {
	for _, completed := range o.Completed {
		if completed == name {
			return true
		}
	}
	return false
}

func (a *server) advanceUpgrade(ctx context.Context, plan *upgradePlan) error {
	switch plan.Status {
	case "preparing":
		if plan.BuildID == "" {
			return nil
		}
		var state struct {
			Builds []imagebuild.Build `json:"builds"`
		}
		if err := a.builderRequest(ctx, "GET", "/images/platform", nil, &state); err != nil {
			return err
		}
		for _, build := range state.Builds {
			if build.ID != plan.BuildID {
				continue
			}
			switch build.Status {
			case "building", "cancelling":
				return nil
			case "succeeded":
				if build.Release == nil || build.Release.Reference == "" {
					return errors.New("构建缺少发布镜像摘要")
				}
				plan.Target = upgradeTarget{Tag: plan.Tag, Reference: build.Release.Reference, Digest: build.Release.Digest, CommitSHA: build.Release.CommitSHA, Platform: build.Release.Platform}
				plan.Preflight, _ = a.upgradePreflight(ctx, plan)
				plan.Status, plan.Stage = "ready", "ready"
				return a.saveUpgrade(ctx, plan, true, false)
			default:
				return fmt.Errorf("平台镜像构建%s: %s", build.Status, build.Error)
			}
		}
		return errors.New("平台镜像构建记录不存在")
	case "running":
		switch plan.Stage {
		case "draining":
			count, err := a.s.UpgradeDrain(ctx)
			if err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			var operation updaterOperation
			if err = a.controlUpdater(ctx, "POST", "/v1/plan", map[string]string{"image": plan.Target.Reference}, &operation); err != nil {
				return err
			}
			plan.ControlOperationID, plan.Stage = operation.OperationID, "upgrading_control"
			now := time.Now().UTC()
			plan.ControlStartedAt = &now
			plan.BackupDir = operation.BackupDir
			if err = a.saveUpgrade(ctx, plan, true, true); err != nil {
				return err
			}
			return a.controlUpdater(ctx, "POST", "/v1/apply", map[string]string{"operationId": operation.OperationID}, &operation)
		case "upgrading_control":
			var operation updaterOperation
			path := "/v1/status?operationId=" + url.QueryEscape(plan.ControlOperationID)
			if err := a.controlUpdater(ctx, "GET", path, nil, &operation); err != nil {
				if plan.ControlStartedAt != nil && time.Since(*plan.ControlStartedAt) < 5*time.Minute {
					return nil
				}
				return err
			}
			if operation.Status == "failed" || operation.Status == "needs_restore" {
				plan.NeedsRestore = operation.NeedsRestore || operation.Status == "needs_restore"
				if operation.BackupDir != "" {
					plan.BackupDir = operation.BackupDir
				}
				return fmt.Errorf("控制面升级失败: %s", operation.Error)
			}
			if operation.Status != "complete" {
				return nil
			}
			plan.Stage = "upgrading_nodes"
			return a.saveUpgrade(ctx, plan, true, true)
		case "upgrading_nodes":
			for i := range plan.Nodes {
				node := &plan.Nodes[i]
				if node.Status == "succeeded" {
					continue
				}
				if node.OperationID == "" {
					var operation updaterOperation
					if err := a.nodeUpdater(ctx, node.ID, "POST", "/v1/plan", map[string]string{"image": plan.Target.Reference}, &operation); err != nil {
						node.Error = err.Error()
						_ = a.saveUpgrade(ctx, plan, true, true)
						return err
					}
					node.OperationID, node.Status = operation.OperationID, "running"
					now := time.Now().UTC()
					node.StartedAt = &now
					if err := a.saveUpgrade(ctx, plan, true, true); err != nil {
						return err
					}
					return a.nodeUpdater(ctx, node.ID, "POST", "/v1/apply", map[string]string{"operationId": operation.OperationID}, &operation)
				}
				var operation updaterOperation
				path := "/v1/status?operationId=" + url.QueryEscape(node.OperationID)
				if err := a.nodeUpdater(ctx, node.ID, "GET", path, nil, &operation); err != nil {
					if node.StartedAt != nil && time.Since(*node.StartedAt) > 5*time.Minute {
						node.Status, node.Error = "failed", "节点升级器超时: "+err.Error()
						_ = a.saveUpgrade(ctx, plan, true, true)
						return errors.New(node.Error)
					}
					return nil /* Node is briefly unavailable while Compose replaces it. */
				}
				if operation.Status == "failed" || operation.Status == "needs_restore" {
					node.Status, node.Error = "failed", operation.Error
					_ = a.saveUpgrade(ctx, plan, true, true)
					return fmt.Errorf("节点 %s 升级失败: %s", node.ID, operation.Error)
				}
				if operation.Status != "complete" {
					return nil
				}
				fresh, err := a.s.Node(ctx, node.ID)
				if err != nil {
					return err
				}
				if fresh.Readiness.PlatformImage != plan.Target.Reference || time.Since(fresh.LastSeen) > domain.NodeOnlineAfter {
					if node.StartedAt != nil && time.Since(*node.StartedAt) > 5*time.Minute {
						node.Status, node.Error = "failed", "节点未以目标镜像恢复心跳"
						_ = a.saveUpgrade(ctx, plan, true, true)
						return errors.New(node.Error)
					}
					return nil
				}
				node.Status, node.Error = "succeeded", ""
				return a.saveUpgrade(ctx, plan, true, true)
			}
			plan.Stage, plan.Status = "completed", "succeeded"
			return a.saveUpgrade(ctx, plan, false, false)
		}
	}
	return nil
}
