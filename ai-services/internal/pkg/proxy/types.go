package proxy

import "context"

// ProxyManager defines the interface for managing reverse proxy routes.
type ProxyManager interface {
	// RegisterRoute registers a new route with the proxy on the public HTTPS server
	// and returns the fully-qualified external URL for the route.
	RegisterRoute(ctx context.Context, route Route) (string, error)

	// RegisterMTLSRoute registers a host-based route on the private mTLS ingress
	// server (:8443) and returns the fully-qualified mTLS URL for the route.
	RegisterMTLSRoute(ctx context.Context, route Route) (string, error)

	// RegisterMTLSPathRoute registers a catch-all reverse-proxy route on the
	// worker's mTLS ingress server (:8443). It matches "/*" and forwards directly
	// to Upstream. The CP egress is the sole place that strips the namespace path
	// prefix — the worker ingress just reverse-proxies whatever arrives.
	RegisterMTLSPathRoute(ctx context.Context, route Route) error

	// RegisterEgressRoute registers an outbound mTLS egress route on the private mTLS egress server.
	RegisterEgressRoute(ctx context.Context, route EgressRoute) error

	// UnregisterRoute removes a route from the proxy by its ID
	UnregisterRoute(ctx context.Context, routeID string) error

	// HealthCheck verifies the proxy is available and responding
	HealthCheck(ctx context.Context) error

	// GetRouteByID retrieves a specific route by its ID from the proxy
	GetRouteByID(ctx context.Context, routeID string) (*Route, error)
}

// EgressRoute represents an outbound proxy route from local plain HTTP to upstream mTLS.
type EgressRoute struct {
	ID                string
	PathPrefix        string
	DialUpstream      string
	ClientCertPath    string
	ClientKeyPath     string
	TrustedCACertPath string
}

// Route represents a reverse proxy route configuration.
type Route struct {
	// ID is the unique identifier for the route
	ID string

	// Domain is the hostname to match (e.g., "service.example.com")
	Domain string

	// PathPrefix is the URL path prefix to match for path-based routes
	// (used by RegisterMTLSPathRoute; empty for host-based routes).
	PathPrefix string

	// Upstream is the backend service address (e.g., "pod-name:8080")
	Upstream string

	// Terminal indicates if route matching should stop after this route
	Terminal bool

	// Type indicates the endpoint type
	Type string

	// ExternalURL is the fully-qualified HTTPS URL for this route
	// (e.g., "https://service.example.com" or "https://service.example.com:8443").
	// Populated by RegisterRoutesForAppAndReturn; empty when routes are built
	// without a known HTTPS port.
	ExternalURL string
}

// Made with Bob
