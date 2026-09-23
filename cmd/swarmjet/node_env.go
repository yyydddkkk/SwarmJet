package main

import (
	"os"
	"strings"
)

func nodeAgentEnv() map[string]string {
	out := map[string]string{"GIT_AUTHOR_NAME": env("GIT_AUTHOR_NAME", "swarmjet"), "GIT_AUTHOR_EMAIL": env("GIT_AUTHOR_EMAIL", "swarmjet@localhost"), "GIT_COMMITTER_NAME": env("GIT_COMMITTER_NAME", env("GIT_AUTHOR_NAME", "swarmjet")), "GIT_COMMITTER_EMAIL": env("GIT_COMMITTER_EMAIL", env("GIT_AUTHOR_EMAIL", "swarmjet@localhost"))}
	for _, k := range strings.Split(env("AGENT_ENV_ALLOWLIST", "OPENAI_API_KEY,CODEX_API_KEY,ANTHROPIC_API_KEY,CLAUDE_CODE_OAUTH_TOKEN,DEEPSEEK_API_KEY,GEMINI_API_KEY,AGY_ADC_AUTH,GOOGLE_GEMINI_BASE_URL,OPENCODE_CONFIG_CONTENT"), ",") {
		k = strings.TrimSpace(k)
		if k != "" && os.Getenv(k) != "" {
			out[k] = os.Getenv(k)
		}
	}
	return out
}
