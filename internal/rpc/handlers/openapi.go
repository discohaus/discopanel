package handlers

import (
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/protometa"
	web "github.com/discohaus/discopanel/web/discopanel"
	"gopkg.in/yaml.v3"
)

// NewOpenAPIHandler returns an http.HandlerFunc that serves the OpenAPI spec.
// Strips Connect protocol noise and injects per-operation security overrides.
// When isAuthEnabled returns false, security schemes are removed entirely.
func NewOpenAPIHandler(log *logger.Logger, isAuthEnabled func() bool) http.HandlerFunc {
	var (
		once         sync.Once
		authEnabled  []byte
		authDisabled []byte
	)

	return func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			buildFS, err := web.BuildFS()
			if err != nil {
				log.Error("Failed to get frontend FS for OpenAPI spec: %v", err)
				return
			}

			raw, err := fs.ReadFile(buildFS, "schemav1.yaml")
			if err != nil {
				log.Error("Failed to read OpenAPI spec: %v", err)
				return
			}

			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				log.Error("Failed to parse OpenAPI spec: %v", err)
				authEnabled = raw
				authDisabled = raw
				return
			}

			// Clean up generated spec
			if paths, ok := doc["paths"].(map[string]any); ok {
				for path, pathItem := range paths {
					methods, ok := pathItem.(map[string]any)
					if !ok {
						continue
					}
					for _, methodVal := range methods {
						op, ok := methodVal.(map[string]any)
						if !ok {
							continue
						}
						// Strip Connect-* header parameters
						if params, ok := op["parameters"].([]any); ok {
							filtered := params[:0]
							for _, p := range params {
								pm, ok := p.(map[string]any)
								if !ok {
									filtered = append(filtered, p)
									continue
								}
								name, _ := pm["name"].(string)
								if name == "Connect-Protocol-Version" || name == "Connect-Timeout-Ms" {
									continue
								}
								filtered = append(filtered, p)
							}
							if len(filtered) == 0 {
								delete(op, "parameters")
							} else {
								op["parameters"] = filtered
							}
						}
						// Mark public operations as no-auth
						procedure := "/" + strings.TrimPrefix(path, "/")
						if perm := protometa.Perm(procedure); perm.GetPublic() {
							op["security"] = []any{}
						}
					}
				}
			}

			// Remove Connect-* schema definitions
			if components, ok := doc["components"].(map[string]any); ok {
				if schemas, ok := components["schemas"].(map[string]any); ok {
					delete(schemas, "connect-protocol-version")
					delete(schemas, "connect-timeout-header")
				}
			}

			enabled, err := yaml.Marshal(doc)
			if err != nil {
				log.Error("Failed to marshal auth-enabled OpenAPI spec: %v", err)
				authEnabled = raw
			} else {
				authEnabled = enabled
			}

			// Build auth-disabled variant without security fields
			delete(doc, "security")

			if components, ok := doc["components"].(map[string]any); ok {
				delete(components, "securitySchemes")
			}

			if paths, ok := doc["paths"].(map[string]any); ok {
				for _, pathItem := range paths {
					if methods, ok := pathItem.(map[string]any); ok {
						for _, methodVal := range methods {
							if op, ok := methodVal.(map[string]any); ok {
								delete(op, "security")
							}
						}
					}
				}
			}

			stripped, err := yaml.Marshal(doc)
			if err != nil {
				log.Error("Failed to marshal stripped OpenAPI spec: %v", err)
				authDisabled = raw
			} else {
				authDisabled = stripped
			}
		})

		var spec []byte
		if isAuthEnabled() {
			spec = authEnabled
		} else {
			spec = authDisabled
		}

		if spec == nil {
			http.Error(w, "OpenAPI spec not available", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(spec)
	}
}
