package worker

import (
	"path/filepath"
	"strings"
)

func harnessEnvironment(source []string, home, scratch string, writes bool, forgeToken, baseURL, model string) []string {
	allowed := map[string]bool{
		"PATH": true, "LANG": true, "LC_ALL": true, "TZ": true, "TERM": true,
		"DOCKER_HOST": true, "DOCKER_CERT_PATH": true, "DOCKER_TLS_VERIFY": true,
		"LITELLM_LOCAL_MODEL_COST_MAP": true, "OPENHANDS_SUPPRESS_BANNER": true,
	}
	values := map[string]string{}
	for _, kv := range source {
		key, value, ok := strings.Cut(kv, "=")
		if ok && allowed[key] {
			values[key] = value
		}
	}
	var env []string
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "TERM", "DOCKER_HOST", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "LITELLM_LOCAL_MODEL_COST_MAP", "OPENHANDS_SUPPRESS_BANNER"} {
		if value, ok := values[key]; ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "HOME="+home, "TMPDIR="+scratch, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "LLM_BASE_URL="+baseURL, "LLM_MODEL="+model)
	if writes && forgeToken != "" {
		env = append(env, "AGENT_BUILDER_TOKEN="+forgeToken)
	}
	return env
}

func harnessModelName(cfg Config) string {
	if cfg.LLMModel != "" {
		return cfg.LLMModel
	}
	if len(cfg.LLMModels) > 0 {
		return cfg.LLMModels[0]
	}
	return ""
}
