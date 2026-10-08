package join

import (
	"context"
	"fmt"
	"net"
	"path/filepath"

	"github.com/project-ai-services/ai-services/internal/pkg/proxy"
	workerconstants "github.com/project-ai-services/ai-services/internal/pkg/worker/constants"
)

const (
	// liteLLMEgressRouteID is the stable Caddy @id for the worker-side LiteLLM
	// egress route. There is exactly one per worker — it is idempotent.
	liteLLMEgressRouteID = "litellm--worker-egress"

	// liteLLMPathPrefix is the path prefix matched by both the worker egress
	// route (:8080) and the CP ingress route (:8443).
	liteLLMPathPrefix = "/litellm"
)

// LiteLLMEgressURL is the URL that service pods deployed on a worker should use
// to reach LiteLLM. It points at the worker's local Caddy egress (:8080) which
// tunnels the request over mTLS to CP Caddy :8443 → litellm:4000.
const LiteLLMEgressURL = "http://" + workerconstants.WorkerCaddyPodName + ":8080" + liteLLMPathPrefix

// registerLiteLLMEgressRoute registers a static egress route on worker Caddy :8080:
//
//	match:   /litellm/*
//	forward: full path (no strip) → mTLS → CP Caddy :8443
//
// CP Caddy :8443 has a matching ingress route that strips /litellm and dials
// litellm:4000, so the LiteLLM pod always receives the bare /v1/... path.
//
// gatewayAddr (e.g. "catalog-worker-gateway.10.x.x.x.nip.io:9191") is used to
// derive the CP hostname; the mTLS port is always DefaultMTLSPort (:8443).
func registerLiteLLMEgressRoute(ctx context.Context, gatewayAddr, tlsDir string) error {
	// Derive CP dial address: same host as gRPC gateway, port 8443.
	cpHost, _, err := net.SplitHostPort(gatewayAddr)
	if err != nil {
		return fmt.Errorf("parse gateway address %q: %w", gatewayAddr, err)
	}
	cpDialAddr := cpHost + ":" + proxy.DefaultMTLSPort

	pm, err := proxy.GetCaddyProxyManager()
	if err != nil {
		return fmt.Errorf("worker Caddy not reachable: %w", err)
	}

	return pm.RegisterEgressRoute(ctx, proxy.EgressRoute{
		ID:                liteLLMEgressRouteID,
		PathPrefix:        liteLLMPathPrefix,
		DialUpstream:      cpDialAddr,
		ClientCertPath:    filepath.Join(tlsDir, tlsCertFile),
		ClientKeyPath:     filepath.Join(tlsDir, tlsKeyPlaintextFile),
		TrustedCACertPath: filepath.Join(tlsDir, caCertFile),
	})
}
