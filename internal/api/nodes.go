package api

import (
	"errors"
	"net/http"
	"strings"
	"swarmjet/internal/networkconfig"
	"swarmjet/internal/nodeenvironment"
	"swarmjet/internal/registryconfig"
	"swarmjet/internal/store"
	"time"
	"unicode"

	"swarmjet/internal/domain"
)

// nodeHeartbeat lets an execution node register or refresh itself. It uses the
// shared node secret instead of a console session and is reached before the
// browser CSRF check because nodes are not browsers.
func (a *server) nodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if a.s == nil {
		problem(w, 503, "execution node registration is unavailable")
		return
	}
	if a.cfg.NodeSecret == "" && a.cfg.Box == nil {
		problem(w, 503, "节点注册未配置")
		return
	}
	var n domain.Node
	if !decode(w, r, &n) {
		return
	}
	if !domain.IDPattern.MatchString(n.ID) {
		problem(w, 401, "node authentication required")
		return
	}
	secret, e := networkconfig.NodeSecret(r.Context(), a.s, a.cfg.Box, n.ID, a.cfg.NodeSecret)
	if e != nil || secret == "" || !secureEqual(r.Header.Get("X-SwarmJet-Node-Secret"), secret) {
		problem(w, 401, "node authentication required")
		return
	}
	if !domain.IDPattern.MatchString(n.ID) || n.Address == "" || n.Capacity < 1 || n.Capacity > 128 {
		problem(w, 400, "valid node id, address and capacity are required")
		return
	}
	if len(n.Address) > 512 || strings.IndexFunc(n.Address, unicode.IsSpace) >= 0 {
		problem(w, 400, "invalid node address")
		return
	}
	if !strings.HasPrefix(n.Address, "http://") && !strings.HasPrefix(n.Address, "https://") {
		problem(w, 400, "node address must be an http(s) URL")
		return
	}
	n.LastSeen = time.Now().UTC()
	if err := a.s.HeartbeatNode(r.Context(), n); err != nil {
		failure(w, err)
		return
	}
	reply := nodeenvironment.Reply{Registered: true}
	c, e := a.s.NodeConfig(r.Context(), n.ID)
	if errors.Is(e, store.ErrNotFound) {
		c, e = a.s.SaveNodeConfig(r.Context(), n.ID, domain.NodeConfig{Name: n.ID, Capacity: n.Capacity, Network: domain.NetworkChoice{Mode: "default"}})
	}
	if e == nil && !c.Archived {
		ref, err := a.s.ResolveNetwork(r.Context(), c.Network)
		if err == nil {
			env, err := networkconfig.Resolve(r.Context(), a.s, a.cfg.Box, ref)
			if err == nil {
				_, access, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, false)
				if err == nil {
					reply.Desired = &nodeenvironment.Desired{Config: c, Network: ref, Env: env, Registry: access}
				}
			}
		}
	}
	respond(w, 200, reply)
}

func (a *server) nodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	if a.s == nil {
		problem(w, 503, "execution node registry is unavailable")
		return
	}
	statuses, err := a.s.NodeStatuses(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, statuses)
}

func (a *server) manageNode(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/")
	if len(parts) != 2 || !domain.IDPattern.MatchString(parts[0]) {
		problem(w, 404, "not found")
		return
	}
	id, action := parts[0], parts[1]
	switch {
	case action == "config" && r.Method == "PUT":
		var c domain.NodeConfig
		if !decode(w, r, &c) {
			return
		}
		if e := c.Validate(); e != nil {
			problem(w, 400, e.Error())
			return
		}
		if _, e := a.s.ResolveNetwork(r.Context(), c.Network); e != nil {
			problem(w, 400, "网络配置不可用")
			return
		}
		c, e := a.s.SaveNodeConfig(r.Context(), id, c)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, c)
	case action == "credential" && r.Method == "POST":
		if a.cfg.Box == nil {
			problem(w, 503, "请配置 SWARMJET_MASTER_KEY")
			return
		}
		token, e := opaqueToken()
		if e != nil {
			failure(w, e)
			return
		}
		sealed, e := a.cfg.Box.Seal(token)
		if e != nil {
			failure(w, e)
			return
		}
		if e = a.s.SaveNodeCredential(r.Context(), id, sealed, false); e != nil {
			failure(w, e)
			return
		}
		respond(w, 201, map[string]string{"nodeId": id, "secret": token})
	case action == "deployment" && r.Method == http.MethodPost:
		a.nodeDeployment(w, r, id)
	case action == "credential" && r.Method == "DELETE":
		sealed, _, e := a.s.NodeCredential(r.Context(), id)
		if e != nil {
			failure(w, e)
			return
		}
		if e = a.s.SaveNodeCredential(r.Context(), id, sealed, true); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	default:
		problem(w, 405, "method not allowed")
	}
}
