package httpapi

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

const credentialIsolationRequired = "credential_isolation_required"

// credentialModesOutsideHarness are the worker isolation values under which
// a Run's forge token and model key stay out of its harness's reach
// (ADR-0034). The credential gateway of ADR-0197 joins this list when it is
// built.
var credentialModesOutsideHarness = []string{ForgeTokenIsolationProxy}

// isolationQualifiedHarnesses are the harnesses qualified to run with both
// credential proxies on; the chart's ploeg.isolationQualifiedHarnesses is
// the same list.
var isolationQualifiedHarnesses = []string{"openhands", "acp/openhands", "exec"}

func keepsCredentialsFromHarness(mode string) bool {
	return slices.Contains(credentialModesOutsideHarness, mode)
}

// credentialIsolationMissing names what keeps a claiming worker from holding
// its Run's credentials outside the harness's reach, or nothing when it does.
func (r claimRequest) credentialIsolationMissing() []string {
	var missing []string
	switch key := store.HarnessKey(r.Harness, r.HarnessProfile); {
	case r.Harness == "":
		missing = append(missing, "the worker names no harness, so ploegd cannot tell whether it is qualified")
	case !slices.Contains(isolationQualifiedHarnesses, key):
		missing = append(missing, fmt.Sprintf("harness %s is not qualified to run with its credentials isolated (qualified: %s)", key, strings.Join(isolationQualifiedHarnesses, ", ")))
	}
	if !keepsCredentialsFromHarness(r.ForgeTokenIsolation) {
		missing = append(missing, fmt.Sprintf("forge token isolation is %s, not proxy (PLOEG_FORGE_TOKEN_ISOLATION)", isolationValue(r.ForgeTokenIsolation)))
	}
	if !keepsCredentialsFromHarness(r.LLMKeyIsolation) {
		missing = append(missing, fmt.Sprintf("model key isolation is %s, not proxy (PLOEG_LLM_KEY_ISOLATION)", isolationValue(r.LLMKeyIsolation)))
	}
	return missing
}

func isolationValue(v string) string {
	if v == "" {
		return "off"
	}
	return fmt.Sprintf("%q", v)
}

// refuseUnisolatedCredentials refuses a claim from a worker that would hand
// its harness the Run's forge token or model key, before any Run is claimed,
// so the Work Item stays queued for a worker that isolates them.
func (s *Server) refuseUnisolatedCredentials(w http.ResponseWriter, req claimRequest) bool {
	missing := req.credentialIsolationMissing()
	if len(missing) == 0 {
		return false
	}
	who := req.Team
	if req.Role != "" {
		who += "/" + req.Role
	}
	s.Log.Warn("claim refused: credential isolation is required", "team", req.Team, "role", req.Role, "missing", strings.Join(missing, "; "))
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
		"code":    credentialIsolationRequired,
		"message": fmt.Sprintf("%s: %s cannot claim a Run: every Role runs with its forge token and model key isolated from its harness (ADR-0034), and %s. Run the Role on a qualified harness with executor.forgeTokenIsolation and executor.litellm.keyIsolation proxy and no DinD", credentialIsolationRequired, who, strings.Join(missing, "; ")),
	}})
	return true
}
