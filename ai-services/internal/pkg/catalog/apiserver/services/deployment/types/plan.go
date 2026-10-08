package types

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// DeploymentPlan represents the complete deployment plan for an application.
type DeploymentPlan struct {
	ApplicationID   uuid.UUID                 // Generated application ID
	ApplicationName string                    // Application name
	Namespace       string                    // Namespace derived from ApplicationID
	CatalogID       string                    // Architecture or service catalog ID
	Version         string                    // Application version from request
	IsArchitecture  bool                      // true for architecture, false for standalone service
	Components      map[string]*ComponentPlan // Key: component hash, Value: component plan
	Services        map[string]*ServicePlan   // Key: service ID, Value: service plan
	SpyreCardPool   *SpyreCardPool            // Allocated Spyre card pool (set after allocation)
	// WorkerName identifies the target worker. Always set by PlanDeployment:
	// workerconstants.LocalWorkerName for local deployments, or the actual
	// worker name for remote ones.
	WorkerName string
	// RuntimeType is the resolved runtime for this deployment (e.g. "podman" or
	// "openshift"). For worker deployments it is the worker's registered runtime;
	// for local deployments it is the server's configured runtime. Set by
	// PlanDeployment and used as the single source of truth by the executor and
	// deployer, avoiding any implicit reads of vars.RuntimeFactory.
	RuntimeType string
	// PostComponentHook is an optional callback invoked by the deployer after all
	// component pods are running but before service pods are started. It is used
	// by the application create flow to register LiteLLM routes for newly deployed
	// managed-model components, generate virtual keys, and inject them into service
	// Values so that the pod Secret template is rendered with the correct key.
	// A non-nil error aborts the deployment.
	PostComponentHook func(ctx context.Context) error
}

// ComponentPlan represents a single component deployment.
type ComponentPlan struct {
	Hash           string         // Unique hash identifying this component configuration
	ComponentType  string         // e.g., "vector_db", "llm", "embedding"
	ProviderID     string         // e.g., "opensearch", "vllm"
	CatalogPath    string         // Dynamic catalog path (e.g., "components/llm/vllm-cpu/podman")
	DatabaseID     uuid.UUID      // Database UUID for this component record (set after DB insertion)
	Version        string         // Component version
	Params         map[string]any // Component parameters
	UsedByServices []string       // List of service IDs that use this component
	Values         map[string]any // Structured values from LoadComponentValues
	Endpoints      map[string]any // Extracted endpoints after deployment (populated by deployer)
	// PreDeployed is true when a running managed model component already exists for this
	// type+provider. The deployer skips pod creation and uses the existing Endpoints directly.
	PreDeployed bool
	// ModelName is the model identifier (e.g. "ibm-granite-3-3-8b-instruct") extracted
	// from the component's params or metadata. Set when PreDeployed=true so the async
	// deploy path can register the LiteLLM route and populate service Values.
	ModelName string
}

// ServicePlan represents a single service deployment.
type ServicePlan struct {
	CatalogID        string            // Service catalog ID (e.g., "chat", "digitize")
	CatalogPath      string            // Dynamic catalog path (e.g., "services/chat/podman")
	DatabaseID       uuid.UUID         // Database UUID for this service record (set after DB insertion)
	Version          string            // Service version
	ComponentRefs    []string          // List of component hashes this service uses
	Values           map[string]any    // Structured values from LoadServiceValues + component values
	Routes           map[string]string // Routes extracted during deployment: podName -> routes annotation
	InternalEndpoint string            // Internal pod-to-pod endpoint URL (http://podname:port), populated by deployer
}

// LiteLLMInfo holds the LiteLLM route and per-application virtual key that is generated
// during the async deploy flow and injected into every service pod via Values["litellm"].
type LiteLLMInfo struct {
	// RouteID is the LiteLLM model_name (e.g. "granite-3.3-8b-instruct--vllm-cpu").
	RouteID string
	// ModelName is the bare HuggingFace/local model identifier (e.g. "ibm-granite/granite-3.3-8b-instruct").
	ModelName string
	// VirtualKey is the per-application sk-... token scoped to RouteID. It is
	// generated fresh for every application and written into the pod Secret.
	VirtualKey string
	// FirstRegistration is true when the LiteLLM route was registered during this
	// application create flow (i.e. no pre-existing model key existed in the DB).
	FirstRegistration bool
}

// SpyreCardPool manages allocation of PCI addresses to components.
type SpyreCardPool struct {
	Addresses []string
	mutex     sync.Mutex
}

// Allocate takes n addresses from the pool and returns them.
func (p *SpyreCardPool) Allocate(n int) ([]string, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.Addresses) < n {
		return nil, ErrInsufficientSpyreCards{Need: n, Have: len(p.Addresses)}
	}

	allocated := make([]string, n)
	copy(allocated, p.Addresses[:n])
	p.Addresses = p.Addresses[n:]

	return allocated, nil
}

// ErrInsufficientSpyreCards is returned when there are not enough Spyre cards available.
type ErrInsufficientSpyreCards struct {
	Need int
	Have int
}

func (e ErrInsufficientSpyreCards) Error() string {
	return fmt.Sprintf("insufficient Spyre cards in pool: need %d, have %d", e.Need, e.Have)
}

// Made with Bob
