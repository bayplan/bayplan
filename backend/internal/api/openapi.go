package api

import (
	_ "embed"
	"net/http"
	"sync"

	"sigs.k8s.io/yaml"
)

// openapiYAML describes the panel's public API (the same one the frontend
// uses). It is maintained by hand: the net/http mux does not expose the routes
// and generating it from annotations would force another dependency for
// documentation that changes little. If you add a route meant for scripts, add
// it here.
//
//go:embed openapi.yaml
var openapiYAML []byte

var (
	openapiOnce sync.Once
	openapiJSON []byte
	openapiErr  error
)

// OpenAPI serves the specification as JSON (what any client or the panel's own
// /api-docs page expects). Requires a session: the specification enumerates
// internal routes and there is no need for it to be public.
func OpenAPI(w http.ResponseWriter, _ *http.Request) {
	openapiOnce.Do(func() { openapiJSON, openapiErr = yaml.YAMLToJSON(openapiYAML) })
	if openapiErr != nil {
		http.Error(w, "invalid specification: "+openapiErr.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(openapiJSON)
}
