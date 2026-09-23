package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/registrycatalog"
	"swarmjet/internal/registryconfig"
	"swarmjet/internal/store"
)

func (a *server) registryImages(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/registry/images")
	if path == "" && r.Method == http.MethodGet {
		config, access, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, false)
		if errors.Is(err, store.ErrNotFound) {
			respond(w, 200, map[string]any{"repository": "", "images": []registrycatalog.Image{}})
			return
		}
		if err != nil {
			failure(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		images, err := registrycatalog.New(config, access).List(ctx)
		if err != nil {
			problem(w, 502, err.Error())
			return
		}
		respond(w, 200, map[string]any{"repository": config.Address + "/" + config.Repository, "images": images})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || !strings.HasPrefix(path, "/") || !strings.HasPrefix(parts[0], "sha256:") || len(parts[0]) != 71 {
		problem(w, 404, "镜像版本不存在")
		return
	}
	config, _, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, false)
	if err != nil {
		failure(w, err)
		return
	}
	reference := config.Address + "/" + config.Repository + "@" + parts[0]
	if !domain.RepositoryDigest.MatchString(reference) {
		problem(w, 400, "镜像摘要无效")
		return
	}
	if len(parts) == 2 && parts[1] == "usage" && r.Method == http.MethodGet {
		usage, err := a.s.ImageUsage(r.Context(), reference)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, map[string]any{"references": usage})
		return
	}
	if len(parts) != 1 || r.Method != http.MethodDelete {
		problem(w, 405, "method not allowed")
		return
	}
	var req struct {
		ExpectedTags []string `json:"expectedTags"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.ExpectedTags) == 0 {
		problem(w, 400, "请刷新仓库清单后再删除")
		return
	}
	sort.Strings(req.ExpectedTags)
	_, push, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, true)
	if err != nil {
		failure(w, err)
		return
	}
	usage, err := a.s.WithImageDeletionGuard(r.Context(), reference, func() error {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		images, e := registrycatalog.New(config, push).List(ctx)
		if e != nil {
			return e
		}
		for _, image := range images {
			if image.Digest != parts[0] {
				continue
			}
			if !reflect.DeepEqual(image.Tags, req.ExpectedTags) {
				return errors.Join(store.ErrConflict, errors.New("镜像标签已变化，请刷新后重试"))
			}
			return registrycatalog.New(config, push).Delete(r.Context(), parts[0])
		}
		return store.ErrNotFound
	})
	if err != nil {
		if len(usage) > 0 {
			problem(w, 409, "镜像仍被平台使用："+strings.Join(usage, "、"))
		} else if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			failure(w, err)
		} else {
			problem(w, 502, err.Error())
		}
		return
	}
	_ = a.s.Event(r.Context(), "", "image_deleted", "管理员删除仓库镜像 "+reference)
	respond(w, 204, nil)
}
