package runtimeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type socketWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *socketWriter) write(kind int, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteMessage(kind, b)
}
func (s *socketWriter) json(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.write(websocket.TextMessage, b)
}
func (s *server) serveTerminal(w http.ResponseWriter, r *http.Request) {
	select {
	case s.terminal <- struct{}{}:
		defer func() { <-s.terminal }()
	default:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "此运行环境已有终端会话，请先关闭其他终端。"})
		return
	}
	if e := ensureProfileHome(s.dirs.Home); e != nil {
		writeJSON(w, 503, map[string]string{"error": "运行环境配置目录不可用。"})
		return
	}
	lock, e := os.OpenFile(filepath.Join(filepath.Dir(s.dirs.Home), ".terminal.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		writeJSON(w, 503, map[string]string{"error": "无法获取运行环境终端锁。"})
		return
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "此运行环境已有终端会话，请先关闭其他终端。"})
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()
	if e := s.ensure(ctx); e != nil {
		writeJSON(w, 503, map[string]string{"error": e.Error()})
		return
	}
	// Closing the Docker client alone can leave its exec child alive. Stop only
	// this ownership-checked setup container when the terminal session ends.
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = s.stop(stopCtx)
	}()
	upgrade := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(*http.Request) bool { return true }} // Private shared-secret route; API proxy enforces browser origin.
	conn, e := upgrade.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(65536)
	writer := &socketWriter{conn: conn}
	args := []string{"exec", "-it", "-e", "TERM=xterm-256color", "-e", "HISTFILE=/dev/null", "-e", "HISTSIZE=0", "-e", "HISTFILESIZE=0", "--workdir", "/runtime-home", s.name, "/bin/bash", "--noprofile", "--norc"}
	env := processEnv(nil)
	var command *exec.Cmd
	if s.cfg.TerminalCommand != nil {
		command = s.cfg.TerminalCommand(ctx, args, env)
	} else {
		command = exec.CommandContext(ctx, "docker", args...)
		command.Env = env
	}
	if command == nil {
		_ = writer.json(map[string]string{"type": "error", "message": "无法创建终端进程。"})
		return
	}
	terminal, e := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if e != nil {
		_ = writer.json(map[string]string{"type": "error", "message": "无法启动终端进程。"})
		return
	}
	defer terminal.Close()
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buffer := make([]byte, 32768)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				if e := writer.write(websocket.BinaryMessage, buffer[:n]); e != nil {
					cancel()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	processDone := make(chan struct{})
	go func() {
		defer close(processDone)
		err := command.Wait()
		select {
		case <-outputDone:
		case <-time.After(time.Second):
		}
		code := 0
		if err != nil {
			code = -1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			}
		}
		_ = writer.json(map[string]any{"type": "exit", "code": code})
		cancel()
		_ = conn.Close()
	}()
	go func() { <-ctx.Done(); _ = conn.Close(); _ = terminal.Close() }()
	defer func() {
		cancel()
		_ = terminal.Close()
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			if command.Process != nil {
				_ = command.Process.Kill()
			}
		}
	}()
	for {
		var message struct {
			Type       string `json:"type"`
			Data       string `json:"data"`
			Cols, Rows int
		}
		if e = conn.ReadJSON(&message); e != nil {
			return
		}
		switch message.Type {
		case "input":
			if len(message.Data) > 32768 {
				_ = writer.json(map[string]string{"type": "error", "message": "终端输入内容过长。"})
				return
			}
			if _, e = terminal.Write([]byte(message.Data)); e != nil {
				return
			}
		case "resize":
			if message.Cols < 10 || message.Cols > 400 || message.Rows < 2 || message.Rows > 200 {
				_ = writer.json(map[string]string{"type": "error", "message": "终端尺寸超出支持范围。"})
				continue
			}
			if e = pty.Setsize(terminal, &pty.Winsize{Cols: uint16(message.Cols), Rows: uint16(message.Rows)}); e != nil {
				_ = writer.json(map[string]string{"type": "error", "message": "终端尺寸调整失败。"})
			}
		default:
			_ = writer.json(map[string]string{"type": "error", "message": "不支持此终端消息类型。"})
		}
		if ctx.Err() != nil {
			return
		}
	}
}
