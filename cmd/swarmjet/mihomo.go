package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"swarmjet/internal/mihomo"
	"swarmjet/internal/secretbox"
	"time"
)

func runMihomo(ctx context.Context) error {
	box, e := secretbox.New(required("SWARMJET_MASTER_KEY"))
	if e != nil {
		return e
	}
	secret := required("MIHOMO_ADMIN_SECRET")
	if len(secret) < 32 {
		return fmt.Errorf("MIHOMO_ADMIN_SECRET must have at least 32 characters")
	}
	data, e := filepath.Abs(env("MIHOMO_DATA_DIR", "./data/mihomo"))
	if e != nil {
		return e
	}
	runtimeDir := env("MIHOMO_RUNTIME_DIR", filepath.Join(os.TempDir(), "swarmjet-mihomo"))
	manager, e := mihomo.New(data, env("MIHOMO_PUBLIC_URL", "http://127.0.0.1:17890"), box, &mihomo.Kernel{Binary: env("MIHOMO_BINARY", "/usr/local/bin/mihomo"), RuntimeDir: runtimeDir})
	if e != nil {
		return e
	}
	defer manager.Close()
	admin := &http.Server{Addr: env("MIHOMO_ADMIN_LISTEN", ":8095"), Handler: &mihomo.Handler{Manager: manager, Secret: secret}, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	gateway := &http.Server{Addr: env("MIHOMO_PROXY_LISTEN", ":7890"), Handler: http.HandlerFunc(manager.Gateway), ReadHeaderTimeout: 15 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer admin.Close()
	defer gateway.Close()
	go manager.Run(ctx)
	failures := make(chan error, 2)
	go func() { failures <- admin.ListenAndServe() }()
	go func() { failures <- gateway.ListenAndServe() }()
	select {
	case <-ctx.Done():
		return nil
	case e := <-failures:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}
