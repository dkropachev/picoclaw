package api

import (
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// GuardPRWorkspaceCanonicalPaths prevents net/http's ServeMux from redirecting
// duplicate-slash or dot-segment aliases before the unified handler can reject
// them. It must wrap the ServeMux that owns the launcher API.
func GuardPRWorkspaceCanonicalPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noncanonicalPRWorkspacePath(r) {
			writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func noncanonicalPRWorkspacePath(r *http.Request) bool {
	if r == nil || r.URL == nil || !pathTraversesPRWorkspace(r.URL.Path) {
		return false
	}
	return r.URL.Path != path.Clean(r.URL.Path) || r.URL.EscapedPath() != r.URL.Path
}

func pathTraversesPRWorkspace(requestPath string) bool {
	segments := make([]string, 0, strings.Count(requestPath, "/"))
	for _, segment := range strings.Split(requestPath, "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			if len(segments) > 0 {
				segments = segments[:len(segments)-1]
			}
		default:
			segments = append(segments, segment)
		}
		current := "/" + strings.Join(segments, "/")
		if current == prWorkspaceAPIPath || strings.HasPrefix(current, prWorkspaceAPIPath+"/") {
			return true
		}
	}
	return false
}

const (
	prWorkspaceAPIPath        = "/api/pr-workspaces"
	prWorkspaceRuntimePath    = "/runtime/eventing/pr-workspaces"
	prWorkspaceMaxBodyBytes   = 1 << 20
	prWorkspaceMaxQueryBytes  = 8 << 10
	prWorkspaceReadTimeout    = 10 * time.Second
	// Review and implementation deliberately run several bounded AI rounds in
	// one mutation. A ten-minute reverse-proxy deadline can cancel a healthy
	// lifecycle run just before its durable aggregate mutation, especially when
	// a provider is slow. Keep the transport bounded, but leave enough room for
	// the domain's own stage and nudge caps to finish.
	prWorkspaceAIWriteTimeout = 30 * time.Minute
)

// registerPRWorkspaceRoutes exposes one launcher contract for review and
// implementation. Runtime validation owns the exact per-route body schema;
// this boundary protects the process bearer and applies hard transport bounds.
func (h *Handler) registerPRWorkspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc(prWorkspaceAPIPath, h.handlePRWorkspaceProxy)
	mux.HandleFunc(prWorkspaceAPIPath+"/", h.handlePRWorkspaceProxy)
}

func (h *Handler) handlePRWorkspaceProxy(w http.ResponseWriter, r *http.Request) {
	setPRWorkspaceResponseHeaders(w)
	if r == nil || r.URL == nil || r.URL.Fragment != "" || r.URL.ForceQuery ||
		r.URL.EscapedPath() != r.URL.Path ||
		(r.URL.Path != prWorkspaceAPIPath && !strings.HasPrefix(r.URL.Path, prWorkspaceAPIPath+"/")) ||
		strings.Contains(r.URL.Path, "//") || strings.Contains(r.URL.Path, "/./") ||
		strings.Contains(r.URL.Path, "/../") || len(r.URL.RawQuery) > prWorkspaceMaxQueryBytes {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost &&
		r.Method != http.MethodPut && r.Method != http.MethodPatch {
		w.Header().Set("Allow", "GET, POST, PUT, PATCH")
		writePRWorkspaceAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var body []byte
	if r.Method == http.MethodGet {
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	} else {
		if r.URL.RawQuery != "" {
			writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if prWorkspaceMutationCrossSite(r) {
			writePRWorkspaceAPIError(w, http.StatusForbidden, "cross_site_request")
			return
		}
		if validateEventReplayHeaders(r.Header) != nil {
			writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_content_type")
			return
		}
		if r.Body == nil || r.ContentLength > prWorkspaceMaxBodyBytes {
			writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, prWorkspaceMaxBodyBytes+1))
		if err != nil || len(body) == 0 || len(body) > prWorkspaceMaxBodyBytes {
			writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	upstream := prWorkspaceRuntimePath + strings.TrimPrefix(r.URL.Path, prWorkspaceAPIPath)
	timeout := prWorkspaceReadTimeout
	if r.Method != http.MethodGet {
		timeout = prWorkspaceAIWriteTimeout
	}
	h.proxyPRWorkspaceGateway(w, r, r.Method, upstream, r.URL.RawQuery, body, timeout)
}

func writePRWorkspaceAPIError(w http.ResponseWriter, status int, code string) {
	setPRWorkspaceResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + strings.ReplaceAll(code, "_", " ") + `"}`))
}
