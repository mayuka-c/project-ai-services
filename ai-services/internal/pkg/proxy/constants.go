package proxy

// DefaultHTTPSPort is the standard HTTPS port used as a fallback when
// CADDY_HTTPS_PORT is not set and when building external route URLs.
const DefaultHTTPSPort = "443"

// DefaultMTLSPort is the default mTLS port used for cross-VM ingress communication.
const DefaultMTLSPort = "8443"

// DefaultEgressPort is the default local egress proxy port.
const DefaultEgressPort = "8080"

// Caddy-related environment variable names shared across packages.
const (
	// DomainSuffixEnvVar is the env var that holds the domain suffix used
	// when building route hostnames (e.g. "example.com" or "10.0.0.1.nip.io").
	DomainSuffixEnvVar = "DOMAIN_SUFFIX"

	// CaddyHTTPSPortEnvVar is the env var that holds the Caddy HTTPS listener port.
	CaddyHTTPSPortEnvVar = "CADDY_HTTPS_PORT"

	// CaddyMTLSPortEnvVar is the env var that holds the Caddy mTLS ingress listener port.
	CaddyMTLSPortEnvVar = "CADDY_MTLS_PORT"

	// CaddyAdminURLEnvVar is the env var that holds the Caddy admin API URL
	// (e.g. "http://ai-services--caddy:2019").
	CaddyAdminURLEnvVar = "CADDY_ADMIN_URL"
)
