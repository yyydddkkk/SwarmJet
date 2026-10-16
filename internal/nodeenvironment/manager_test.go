package nodeenvironment

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplyDoesNotExecuteAgentAndReportsReadyOnlyAfterPull(t *testing.T) {
	m := New(t.TempDir(), "test-node")
	gate := make(chan struct{})
	var pulls atomic.Int32
	m.Command = func(ctx context.Context, args []string) ([]byte, error) {
		cmd := args
		switch cmd[0] {
		case "network":
			return []byte("ok"), nil
		case "pull":
			pulls.Add(1)
			<-gate
			return nil, nil
		case "image":
			return []byte(runtime.GOARCH), nil
		case "run":
			joined := strings.Join(cmd, " ")
			if !strings.Contains(joined, "--network=none") || !strings.Contains(joined, "command -v codex") {
				t.Error("unsafe CLI probe")
			}
			return []byte("codex\nopencode\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	d := Desired{Config: domain.NodeConfig{Version: 1, Image: "ghcr.io/example/swarmjet/agent:v1.0.0"}}
	m.Apply(context.Background(), d)
	m.Apply(context.Background(), d)
	if m.Active() != nil {
		t.Fatal("activated before pull")
	}
	close(gate)
	deadline := time.Now().Add(time.Second)
	for m.Status().Status != "ready" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Status().Status != "ready" || m.Active() == nil || pulls.Load() != 1 {
		t.Fatal(m.Status(), pulls.Load())
	}
	if len(m.Status().Agents) != 2 {
		t.Fatal(m.Status())
	}
	m.Apply(context.Background(), d)
	if pulls.Load() != 1 {
		t.Fatal("pulled applied version again")
	}
}
func TestFailedImagePreservesPreviouslyAppliedEnvironment(t *testing.T) {
	m := New(t.TempDir(), "a")
	old := Desired{Config: domain.NodeConfig{Version: 1}}
	m.active = &old
	m.Command = func(context.Context, []string) ([]byte, error) { return nil, errors.New("failure") }
	m.Apply(context.Background(), Desired{Config: domain.NodeConfig{Version: 2, Image: "bad"}})
	deadline := time.Now().Add(time.Second)
	for m.Status().Status != "failed" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Status().Status != "failed" || m.Active().Config.Version != 1 {
		t.Fatal(m.Status())
	}
}

func TestLocallyBuiltImageIsUsedWhenPullFails(t *testing.T) {
	m := New(t.TempDir(), "local")
	m.Command = func(ctx context.Context, args []string) ([]byte, error) {
		switch args[0] {
		case "network":
			return []byte("ok"), nil
		case "pull":
			return nil, errors.New("pull access denied")
		case "image":
			if args[len(args)-2] == "{{.Id}}" {
				return []byte("sha256:local"), nil
			}
			return []byte(runtime.GOARCH), nil
		case "run":
			return []byte("opencode\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	m.Apply(context.Background(), Desired{Config: domain.NodeConfig{Version: 1, Image: "swarmjet-agent:local"}})
	deadline := time.Now().Add(time.Second)
	for m.Status().Status == "pulling" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Status().Status != "ready" {
		t.Fatal(m.Status())
	}
}

func TestMissingImageFailsWhenPullFails(t *testing.T) {
	m := New(t.TempDir(), "missing")
	m.Command = func(ctx context.Context, args []string) ([]byte, error) {
		if args[0] == "network" {
			return []byte("ok"), nil
		}
		return nil, errors.New("not found")
	}
	m.Apply(context.Background(), Desired{Config: domain.NodeConfig{Version: 1, Image: "ghcr.io/example/missing:v1"}})
	deadline := time.Now().Add(time.Second)
	for m.Status().Status == "pulling" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Status().Status != "failed" || !strings.Contains(m.Status().Error, "拉取失败") {
		t.Fatal(m.Status())
	}
}
