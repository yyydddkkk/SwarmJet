package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"swarmjet/internal/buildproxy"
	"swarmjet/internal/domain"
	"swarmjet/internal/imagebuild"
	"swarmjet/internal/networkconfig"
	"swarmjet/internal/registrycatalog"
	"swarmjet/internal/registryconfig"
	"swarmjet/internal/runtimeadmin"
	"swarmjet/internal/secretbox"
	"swarmjet/internal/store"
	"time"
)

func runBuilder(ctx context.Context, s *store.Store, box *secretbox.Box) error {
	// Session lock prevents competing managers from recovering or executing the same build.
	conn, e := s.Pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	var locked bool
	if e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(84261304)").Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return fmt.Errorf("another builder owns the image manager")
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(84261304)")
	data, e := filepath.Abs(env("DATA_DIR", "./data"))
	if e != nil {
		return e
	}
	proxy := &buildproxy.Proxy{}
	configure := func(m *imagebuild.Manager) error {
		if e := m.UseStateStore(func(state *imagebuild.State) error {
			e := s.Setting(ctx, "image_build_state", state)
			if errors.Is(e, store.ErrNotFound) {
				return nil
			}
			return e
		}, func(state imagebuild.State) error { return s.SetSetting(ctx, "image_build_state", state) }); e != nil {
			return e
		}
		m.Resolve = func(cctx context.Context, c *imagebuild.Config) (imagebuild.Prepared, error) {
			reg, access, e := registryconfig.Load(cctx, s, box, true)
			if e != nil {
				return imagebuild.Prepared{}, fmt.Errorf("请先配置镜像仓库")
			}
			repository := reg.Address + "/" + reg.Repository
			if c.Repository != "" && c.Repository != repository {
				return imagebuild.Prepared{}, fmt.Errorf("构建只能发布到当前配置的仓库路径")
			}
			c.Repository = repository
			if c.Platform == "" {
				c.Platform = "linux/" + runtime.GOARCH
			}
			if c.Platform != "linux/"+runtime.GOARCH {
				return imagebuild.Prepared{}, fmt.Errorf("当前构建服务仅支持本机架构 %s", runtime.GOARCH)
			}
			ref, e := s.ResolveNetwork(cctx, c.Network)
			if e != nil {
				return imagebuild.Prepared{}, e
			}
			c.NetworkVersion = ref
			vars, e := networkconfig.Resolve(cctx, s, box, ref)
			if e == nil {
				proxy.Use(vars)
			}
			return imagebuild.Prepared{Env: vars, Registry: access}, e
		}
		m.ResolvePlatform = func(cctx context.Context, tag string) (imagebuild.Prepared, string, error) {
			reg, access, err := registryconfig.Load(cctx, s, box, true)
			if err != nil || reg.Mode != "builtin" {
				return imagebuild.Prepared{}, "", fmt.Errorf("请先启用内置镜像仓库")
			}
			reg.Repository = "swarmjet/platform"
			images, err := registrycatalog.New(reg, access).List(cctx)
			if err != nil {
				return imagebuild.Prepared{}, "", fmt.Errorf("无法读取平台镜像版本，请检查仓库连接")
			}
			for _, image := range images {
				for _, existing := range image.Tags {
					if existing == tag {
						return imagebuild.Prepared{}, "", fmt.Errorf("该平台镜像版本标签已存在，请使用新标签")
					}
				}
			}
			ref, err := s.ResolveNetwork(cctx, domain.NetworkChoice{Mode: "default"})
			if err != nil {
				return imagebuild.Prepared{}, "", err
			}
			vars, err := networkconfig.Resolve(cctx, s, box, ref)
			if err != nil {
				return imagebuild.Prepared{}, "", err
			}
			proxy.Use(vars)
			return imagebuild.Prepared{Env: vars, Registry: access}, reg.Address + "/swarmjet/platform", nil
		}
		return nil
	}
	secret := required("RUNTIME_ADMIN_SECRET")
	if len(secret) < 32 {
		return fmt.Errorf("RUNTIME_ADMIN_SECRET too short")
	}
	handler := runtimeadmin.New(runtimeadmin.Config{DataDir: data, Secret: secret, ImageRunner: imagebuild.RemoteRunner(required("BUILDKIT_HOST")), ConfigureImages: configure, ImagesOnly: true, PlatformSourceDir: os.Getenv("SWARMJET_PLATFORM_SOURCE_DIR")})
	srv := &http.Server{Addr: env("BUILDER_LISTEN_ADDRESS", ":8093"), Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	healthCtx, stopHealth := context.WithCancel(ctx)
	healthDone := make(chan struct{})
	defer func() { stopHealth(); <-healthDone }()
	// Loss of lock ownership must stop accepting builds; do not silently reconnect.
	go func() {
		defer close(healthDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-healthCtx.Done():
				_ = srv.Close()
				return
			case <-ticker.C:
				check, cancel := context.WithTimeout(healthCtx, 3*time.Second)
				err := conn.Conn().Ping(check)
				cancel()
				if err != nil {
					_ = srv.Close()
					return
				}
			}
		}
	}()
	proxyServer := &http.Server{Addr: env("BUILDER_PROXY_LISTEN", ":8094"), Handler: proxy, ReadHeaderTimeout: 15 * time.Second}
	defer proxyServer.Close()
	go func() {
		if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			_ = srv.Close()
		}
	}()
	e = srv.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
