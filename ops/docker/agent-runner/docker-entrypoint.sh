#!/usr/bin/env bash
set -Eeuo pipefail

log() { printf '%s agent-runner: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

if [[ -z "${LLM_BASE_URL:-}" ]]; then
  log "ERROR LLM_BASE_URL is not set; name the OpenAI-compatible gateway, for example https://litellm.example/v1"
  exit 64
fi
export LLM_MODEL="${LLM_MODEL:-litellm_proxy/deepseek-chat}"
export LITELLM_ADMIN_URL="${LITELLM_ADMIN_URL:-${LLM_BASE_URL%/v1}}"
export LITELLM_KEY_BUDGET="${LITELLM_KEY_BUDGET:-5}"
export LITELLM_KEY_DURATION="${LITELLM_KEY_DURATION:-2h}"
key_model="${LLM_MODEL#litellm_proxy/}"; key_model="${key_model#openai/}"
export LITELLM_KEY_MODELS="${LITELLM_KEY_MODELS:-${key_model}}"
export LITELLM_KEY_SOURCE=openhands-runner

if [[ -z "${LLM_TRACE_ID:-}" ]]; then
  uuid="$(cat /proc/sys/kernel/random/uuid)"
  uuid="${uuid//-/}"
  LLM_TRACE_ID="openhands-$(date -u +%Y%m%dT%H%M%SZ)-${uuid:0:8}"
fi
export LLM_EXTRA_HEADERS="{\"x-litellm-trace-id\":\"${LLM_TRACE_ID}\"}"
export LITELLM_TRACE_ID="${LLM_TRACE_ID}"

minted_key=""
revoke_key() {
  [[ -n "${minted_key}" ]] || return 0
  if LITELLM_REVOKE_KEY="${minted_key}" litellm-key revoke; then
    log "revoked per-run key (alias ${LLM_TRACE_ID})"
  else
    log "WARN could not revoke per-run key (alias ${LLM_TRACE_ID}); it expires in ${LITELLM_KEY_DURATION}"
  fi
}

if [[ -n "${LLM_API_KEY:-}" ]]; then
  log "using caller-supplied LLM_API_KEY (mint/revoke owned by the caller)"
elif [[ -n "${LITELLM_MASTER_KEY:-}" ]]; then
  if ! minted_key="$(LITELLM_KEY_ALIAS="${LLM_TRACE_ID}" litellm-key mint)"; then
    log "ERROR could not mint a per-run key (see the error above)"
    exit 69
  fi
  export LLM_API_KEY="${minted_key}"
  trap revoke_key EXIT
  log "minted per-run key (models=[${LITELLM_KEY_MODELS}] budget=${LITELLM_KEY_BUDGET} ttl=${LITELLM_KEY_DURATION})"
else
  log "ERROR no LLM_API_KEY and no LITELLM_MASTER_KEY; cannot authenticate."
  exit 64
fi

log "model=${LLM_MODEL} base=${LLM_BASE_URL} trace-id=${LLM_TRACE_ID}"
log "$(cat /etc/openhands-version 2>/dev/null || openhands --version 2>/dev/null | head -1)"

SKILLS_DIR="${HOME:-/root}/.openhands/skills/installed"
mkdir -p "${SKILLS_DIR}"
if [[ -n "${OPENHANDS_SKILLS_PROFILE:-}" ]]; then
  if [[ -z "${OPENHANDS_SKILLS_REPO:-}" ]]; then
    log "WARN OPENHANDS_SKILLS_PROFILE is set but OPENHANDS_SKILLS_REPO is not; no profile skills added"
  else
    log "adding profile skills: ${OPENHANDS_SKILLS_PROFILE}"
    _skills_tmp="$(mktemp -d)"
    if git clone --depth 1 --branch "${OPENHANDS_SKILLS_REF:-main}" \
         "${OPENHANDS_SKILLS_REPO}" "${_skills_tmp}" >/dev/null 2>&1; then
      IFS=',' read -ra _want <<< "${OPENHANDS_SKILLS_PROFILE}"
      for _s in "${_want[@]}"; do
        _s="${_s// /}"
        [[ -z "${_s}" ]] && continue
        if [[ -d "${_skills_tmp}/skills/${_s}" ]]; then
          cp -r "${_skills_tmp}/skills/${_s}" "${SKILLS_DIR}/" && echo "  + ${_s}" >&2
        else
          echo "  WARN profile skill '${_s}' is not in ${OPENHANDS_SKILLS_REPO}; skipped" >&2
        fi
      done
    else
      log "WARN could not clone ${OPENHANDS_SKILLS_REPO}; continuing without profile skills"
    fi
    rm -rf "${_skills_tmp}"
  fi
fi
log "skills available: $(ls -1 "${SKILLS_DIR}" 2>/dev/null | tr '\n' ' ')"

openhands --override-with-envs "$@" &
agent_pid=$!

forward_signal() {
  log "forwarding $1 to agent (pid ${agent_pid})"
  kill "-$1" "${agent_pid}" 2>/dev/null || true
}
trap 'forward_signal TERM' TERM
trap 'forward_signal INT' INT

rc=0
while :; do
  if wait "${agent_pid}"; then rc=0; else rc=$?; fi
  if (( rc > 128 )) && kill -0 "${agent_pid}" 2>/dev/null; then
    continue
  fi
  break
done

log "agent exited rc=${rc}"
exit "${rc}"
