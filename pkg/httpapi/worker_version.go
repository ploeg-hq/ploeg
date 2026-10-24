package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const workerVersionUnsupported = "worker_version_unsupported"

type releaseLine struct {
	major, minor int
}

func (l releaseLine) String() string {
	return strconv.Itoa(l.major) + "." + strconv.Itoa(l.minor)
}

func parseReleaseLine(version string) (releaseLine, bool) {
	core, prerelease, _ := strings.Cut(strings.TrimPrefix(version, "v"), "-")
	core, _, _ = strings.Cut(core, "+")
	if prerelease != "" && !strings.HasPrefix(prerelease, "rc.") {
		return releaseLine{}, false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return releaseLine{}, false
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return releaseLine{}, false
		}
		numbers[i] = n
	}
	return releaseLine{major: numbers[0], minor: numbers[1]}, true
}

type workerVersionRefusal struct {
	Error        string `json:"error"`
	Message      string `json:"message"`
	Ploegd       string `json:"ploegd"`
	Worker       string `json:"worker"`
	OldestWorker string `json:"oldestWorker"`
	NewestWorker string `json:"newestWorker"`
	Upgrade      string `json:"upgrade"`
}

func (r workerVersionRefusal) upgradeProtocols() string {
	if r.OldestWorker == r.NewestWorker {
		return "ploeg-worker/" + r.NewestWorker
	}
	return "ploeg-worker/" + r.OldestWorker + ", ploeg-worker/" + r.NewestWorker
}

func workerVersionRefusalFor(ploegdVersion, workerVersion string) (workerVersionRefusal, bool) {
	served, ok := parseReleaseLine(ploegdVersion)
	if !ok {
		return workerVersionRefusal{}, false
	}
	worker, ok := parseReleaseLine(workerVersion)
	if !ok {
		return workerVersionRefusal{}, false
	}
	oldest := releaseLine{major: served.major, minor: max(served.minor-1, 0)}
	if worker.major == served.major && worker.minor >= oldest.minor && worker.minor <= served.minor {
		return workerVersionRefusal{}, false
	}
	refusal := workerVersionRefusal{
		Error:        workerVersionUnsupported,
		Ploegd:       ploegdVersion,
		Worker:       workerVersion,
		OldestWorker: oldest.String(),
		NewestWorker: served.String(),
		Upgrade:      "worker",
	}
	order := "upgrade the workers to ploegd's release"
	if worker.major > served.major || (worker.major == served.major && worker.minor > served.minor) {
		refusal.Upgrade = "ploegd"
		order = "upgrade ploegd first, then the workers"
	}
	refusal.Message = fmt.Sprintf("ploegd %s serves ploeg-worker %s to %s, and this worker is %s: %s",
		ploegdVersion, refusal.OldestWorker, refusal.NewestWorker, workerVersion, order)
	return refusal, true
}

func (s *Server) refuseUnservedWorker(w http.ResponseWriter, r *http.Request, req claimRequest) bool {
	refusal, refused := workerVersionRefusalFor(s.Version, req.WorkerVersion)
	if !refused {
		return false
	}
	s.Log.Warn("claim refused: ploegd does not serve this worker version", "team", req.Team, "role", req.Role,
		"worker_version", req.WorkerVersion, "ploegd_version", s.Version, "upgrade", refusal.Upgrade)
	if r.ProtoMajor == 1 {
		w.Header().Set("Upgrade", refusal.upgradeProtocols())
	}
	writeJSON(w, http.StatusUpgradeRequired, refusal)
	return true
}
