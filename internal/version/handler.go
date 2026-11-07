package version

import (
	"encoding/json"
	"net/http"
)

// Handler godoc
//
//	@Summary		The running tenure's build
//	@Description	The API's version, commit, build time, whether it is a release build, the Go toolchain, the platform, backendkit and the last embedded migration; the tenure-web release pinned in the binary. Any signed-in user; /healthz keeps only the version for monitoring.
//	@Tags			meta
//	@Produce		json
//	@Success		200	{object}	View
//	@Failure		401	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/version [get]
func Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(Get())
}
