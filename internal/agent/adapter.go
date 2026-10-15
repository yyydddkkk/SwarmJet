package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/yyydddkkk/SwarmJet/internal/agentcli"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// cliAdapter owns command, environment and event differences. The Runtime owns
// Docker lifecycle, private mounts, result validation and cancellation.
type cliAdapter struct{ agentcli.Spec }

func adapterFor(id string) (cliAdapter, error) {
	spec, ok := agentcli.Lookup(id)
	if !ok {
		return cliAdapter{}, fmt.Errorf("unsupported agent adapter %q", id)
	}
	return cliAdapter{spec}, nil
}

func (a cliAdapter) command(req domain.AgentRequest, promptPath, profileHome string) ([]string, error) {
	if req.Role == "init" && a.ID != "opencode" {
		return nil, fmt.Errorf("static initialization requires the opencode adapter")
	}
	prompt := "Read the task instructions from " + strconv.Quote(promptPath) + ". Execute them and write the required result.json artifact."
	var args []string
	switch a.ID {
	case "opencode":
		args = []string{"run", "Execute the instructions in the attached task file. Write the required result artifact.", "--format", "json", "--auto", "--dir", req.Workspace}
		// Model is retained only for legacy task snapshots and static initialization.
		if req.Config.Model != "" && (!req.Config.UseCLIDefaults || req.Role == "init") {
			args = append(args, "-m", req.Config.Model)
		}
		args = append(args, "--file", promptPath)
		if req.SessionID != "" {
			args = append(args, "--session", req.SessionID)
		}
	case "codex":
		args = []string{"exec"}
		if req.SessionID != "" {
			args = append(args, "resume")
		}
		args = append(args, "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check")
		if req.SessionID != "" {
			args = append(args, req.SessionID)
		}
		args = append(args, prompt)
	case "dst":
		// Requires the pinned DSH 0.1.6-alpha.2 release (older rc lacks these flags).
		args = []string{"--profile", "headless"}
		if profileHome != "" {
			patchPath := filepath.Join(profileHome, dstModelPatchName)
			if _, present, err := readProfileFile(patchPath); err != nil {
				return nil, err
			} else if present {
				args = append(args, "--patch", patchPath)
			}
		}
		args = append(args, "--json")
		if req.SessionID != "" {
			args = append(args, "--session-id", req.SessionID)
		}
		args = append(args, prompt)
	case "agy":
		args = []string{"--output-format", "stream-json", "--dangerously-skip-permissions", "--print-timeout", strconv.Itoa(req.Config.TimeoutSeconds) + "s"}
		if req.SessionID != "" {
			args = append(args, "--conversation", req.SessionID)
		}
		args = append(args, "-p", prompt)
	case "claude":
		// acceptEdits plus a Bash allow rule works for root and non-root containers;
		// --dangerously-skip-permissions is rejected by Claude when running as root.
		args = []string{"--print", prompt, "--verbose", "--output-format", "stream-json", "--permission-mode", "acceptEdits", "--add-dir", filepath.Dir(promptPath), "--allowedTools", "Bash,Read,Edit,Write,Glob,Grep"}
		if req.SessionID != "" {
			args = append(args, "--resume", req.SessionID)
		}
	}
	return args, nil
}

func (a cliAdapter) environment(home string) []string {
	env := []string{"HOME=" + home, "XDG_DATA_HOME=" + filepath.Join(home, ".local/share"), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_STATE_HOME=" + filepath.Join(home, ".local/state")}
	switch a.ID {
	case "codex":
		env = append(env, "CODEX_HOME="+filepath.Join(home, ".codex"))
	case "dst":
		env = append(env, "DSH_HOME="+filepath.Join(home, ".dsh"), "DSH_PERMISSION_MODE=danger-full-access")
	}
	return env
}

func (a cliAdapter) session(raw []byte) string {
	if a.ID == "opencode" {
		_, id := ParseJSONL(raw, nil)
		return id
	}
	s := bufio.NewScanner(bytes.NewReader(raw))
	s.Buffer(make([]byte, 4096), 1<<20)
	id := ""
	for s.Scan() {
		var event map[string]any
		if json.Unmarshal(s.Bytes(), &event) != nil {
			continue
		}
		key := ""
		switch a.ID {
		case "codex":
			if event["type"] == "thread.started" {
				key = "thread_id"
			}
		case "dst":
			if event["type"] == "session" {
				key = "sessionId"
			}
		case "claude":
			if event["type"] == "system" && event["subtype"] == "init" || event["type"] == "result" {
				key = "session_id"
			}
		case "agy":
			if event["event"] == "init" {
				key = "conversation_id"
			}
			if event["event"] == "result" {
				if result, ok := event["result"].(map[string]any); ok {
					event = result
					key = "conversation_id"
				}
			}
		}
		if value, ok := event[key].(string); ok && value != "" {
			id = value
		}
	}
	return id
}

// A structured terminal failure must not be mistaken for success even when a
// CLI returns zero and managed to write an artifact before failing.
func (a cliAdapter) terminalFailure(raw []byte) string {
	s := bufio.NewScanner(bytes.NewReader(raw))
	s.Buffer(make([]byte, 4096), 1<<20)
	failure := ""
	for s.Scan() {
		var event map[string]any
		if json.Unmarshal(s.Bytes(), &event) != nil {
			continue
		}
		switch a.ID {
		case "claude":
			if event["type"] == "result" {
				failure = ""
				if event["is_error"] == true {
					failure = "Claude Code reported a failed result"
				}
			}
		case "agy":
			if event["event"] == "result" {
				if result, ok := event["result"].(map[string]any); ok {
					failure = ""
					if result["status"] != "SUCCESS" {
						failure = "Antigravity reported a non-success terminal result"
					}
				}
			}
		case "codex":
			if event["type"] == "turn.failed" {
				failure = "Codex reported a failed turn"
			}
			if event["type"] == "turn.completed" {
				failure = ""
			}
		}
	}
	return failure
}
