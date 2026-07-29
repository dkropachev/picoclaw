package channels

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// dynamicServeMux is an http.Handler that supports dynamic registration
// and unregistration of handlers without recreating the server.
type dynamicServeMux struct {
	mu       sync.RWMutex
	handlers map[string]dynamicRoute
}

type dynamicRoute struct {
	handler http.Handler
	owner   *dynamicRouteOwner
}

// dynamicRouteOwner gives registrations created through registerOwned an
// identity. Its non-zero size keeps distinct allocations observably distinct.
type dynamicRouteOwner byte

func newDynamicServeMux() *dynamicServeMux {
	return &dynamicServeMux{
		handlers: make(map[string]dynamicRoute),
	}
}

// Handle registers the handler for the given pattern.
func (dm *dynamicServeMux) Handle(pattern string, handler http.Handler) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	for existingPattern, route := range dm.handlers {
		if route.owner != nil &&
			dynamicRoutePatternsOverlap(pattern, existingPattern) {
			// Additive owned routes are reservations. Legacy registrations keep
			// their historical overwrite behavior for other legacy routes but
			// cannot replace or shadow an independently owned feature route.
			return
		}
	}
	dm.handlers[pattern] = dynamicRoute{handler: handler}
}

// HandleFunc registers the handler function for the given pattern.
func (dm *dynamicServeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	dm.Handle(pattern, http.HandlerFunc(handler))
}

// Unhandle removes the handler for the given pattern.
func (dm *dynamicServeMux) Unhandle(pattern string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	if route, ok := dm.handlers[pattern]; ok && route.owner == nil {
		delete(dm.handlers, pattern)
	}
}

// registerOwned adds a collision-safe route and returns an idempotent release
// function. A release removes only the exact registration that created it, so
// a delayed release cannot delete a later replacement.
func (dm *dynamicServeMux) registerOwned(
	pattern string,
	handler http.Handler,
) (func(), error) {
	if err := validateDynamicRoutePattern(pattern); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, fmt.Errorf("HTTP route handler is required")
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	conflict := ""
	for existingPattern := range dm.handlers {
		if dynamicRoutePatternsOverlap(pattern, existingPattern) &&
			(conflict == "" || existingPattern < conflict) {
			conflict = existingPattern
		}
	}
	if conflict != "" {
		return nil, fmt.Errorf(
			"%w: pattern %q overlaps %q",
			ErrHTTPRouteConflict,
			pattern,
			conflict,
		)
	}

	owner := new(dynamicRouteOwner)
	dm.handlers[pattern] = dynamicRoute{handler: handler, owner: owner}
	return func() {
		dm.mu.Lock()
		defer dm.mu.Unlock()
		current, ok := dm.handlers[pattern]
		if ok && current.owner == owner {
			delete(dm.handlers, pattern)
		}
	}, nil
}

// validateAvailable reports whether every candidate route can be added without
// overlapping an existing route or another candidate. It reserves nothing;
// callers that need atomicity must hold their own registration coordinator
// across validation and legacy registration.
func (dm *dynamicServeMux) validateAvailable(patterns ...string) error {
	for _, pattern := range patterns {
		if err := validateDynamicRoutePattern(pattern); err != nil {
			return err
		}
	}
	for left := range patterns {
		for right := left + 1; right < len(patterns); right++ {
			if dynamicRoutePatternsOverlap(patterns[left], patterns[right]) {
				return fmt.Errorf(
					"%w: patterns %q and %q overlap",
					ErrHTTPRouteConflict,
					patterns[left],
					patterns[right],
				)
			}
		}
	}

	dm.mu.RLock()
	defer dm.mu.RUnlock()
	for _, pattern := range patterns {
		for existingPattern := range dm.handlers {
			if dynamicRoutePatternsOverlap(pattern, existingPattern) {
				return fmt.Errorf(
					"%w: pattern %q overlaps %q",
					ErrHTTPRouteConflict,
					pattern,
					existingPattern,
				)
			}
		}
	}
	return nil
}

func validateDynamicRoutePattern(pattern string) error {
	switch {
	case pattern == "":
		return fmt.Errorf("HTTP route pattern is required")
	case !strings.HasPrefix(pattern, "/"):
		return fmt.Errorf("HTTP route pattern %q must start with /", pattern)
	case strings.ContainsAny(pattern, "?#"):
		return fmt.Errorf("HTTP route pattern %q must not contain a query or fragment", pattern)
	default:
		return nil
	}
}

// dynamicRoutePatternsOverlap reports whether requests can match both
// patterns. Exact routes overlap only an identical route or a subtree prefix
// containing them. A trailing slash denotes a subtree prefix, matching the
// existing dynamic mux behavior.
func dynamicRoutePatternsOverlap(left, right string) bool {
	if left == right {
		return true
	}
	if strings.HasSuffix(left, "/") && strings.HasPrefix(right, left) {
		return true
	}
	return strings.HasSuffix(right, "/") && strings.HasPrefix(left, right)
}

// ServeHTTP dispatches the request to the handler whose pattern best matches
// the request URL path. It supports both exact path matches and subtree
// (trailing-slash) prefix matches, choosing the longest prefix on collision.
func (dm *dynamicServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler, found := dm.handlerForPath(r.URL.Path)
	if found {
		handler.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (dm *dynamicServeMux) handlerForPath(path string) (http.Handler, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	// Exact match first.
	if route, ok := dm.handlers[path]; ok {
		return route.handler, true
	}

	// Longest subtree prefix match (patterns ending with "/").
	var bestLen int
	var bestHandler http.Handler
	for pattern, route := range dm.handlers {
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(path, pattern) {
			if len(pattern) > bestLen {
				bestLen = len(pattern)
				bestHandler = route.handler
			}
		}
	}
	return bestHandler, bestLen > 0
}
