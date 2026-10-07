package modelservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	texttemplate "text/template"

	"github.com/google/uuid"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog"
	apimodels "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/models"
	podmandeployer "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/deployment/repository/podman"
	podmandeletion "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/deletion/repository/podman"
	deploymenttypes "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/deployment/types"
	catalogconstants "github.com/project-ai-services/ai-services/internal/pkg/catalog/constants"
	dbmodels "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/models"
	dbrepo "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/repository"
	catalogtypes "github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/validators"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/helpers"
	"github.com/project-ai-services/ai-services/internal/pkg/image"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime"
	remoteruntime "github.com/project-ai-services/ai-services/internal/pkg/runtime/remote"
	runtimetypes "github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/utils"
	workerconstants "github.com/project-ai-services/ai-services/internal/pkg/worker/constants"
	"github.com/project-ai-services/ai-services/internal/pkg/worker/payload"
	workerpb "github.com/project-ai-services/ai-services/internal/pkg/worker/proto"
	"github.com/project-ai-services/ai-services/internal/pkg/worker/stream"
)

const (
	// litellmURLEnv is the environment variable that holds the LiteLLM gateway base URL.
	litellmURLEnv = "LITELLM_URL"
	// litellmMasterKeyEnv is the environment variable that holds the LiteLLM master key.
	litellmMasterKeyEnv = "LITELLM_MASTER_KEY"

	// defaultLiteLLMURL is the in-cluster address used when LITELLM_URL is not set.
	defaultLiteLLMURL = "http://litellm:4000"

	// allowedRouteChars is the pattern used to sanitise model names in route IDs.
	allowedRouteChars = `[^a-zA-Z0-9-]`
)

// ValidationError is re-exported so callers can use a single type.
type ValidationError = validators.ValidationError

// validModelTypes contains the set of allowed component role values.
var validModelTypes = map[string]bool{
	"llm":       true,
	"embedding": true,
	"reranker":  true,
}

// sanitiseRouteSegment replaces any character that is not a letter, digit, or hyphen with
// a single hyphen, and collapses runs of three or more hyphens down to one.
// Double hyphens are preserved — they are the segment separator used in route IDs
// and pod names throughout the platform (e.g. "granite-3.3-8b-instruct--vllm-cpu").
func sanitiseRouteSegment(s string) string {
	re := regexp.MustCompile(allowedRouteChars)
	s = re.ReplaceAllString(s, "-")
	// Collapse triple-or-more hyphens to a single hyphen, but leave "--" intact.
	tripleHyphen := regexp.MustCompile(`-{3,}`)
	s = tripleHyphen.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// buildRouteID returns the LiteLLM route ID for a model.
// Convention (matches config.yaml): {sanitised_model_name}--{provider_id}
// Example: "granite-3.3-8b-instruct--vllm-cpu"
func buildRouteID(modelName, providerID string) string {
	return sanitiseRouteSegment(modelName) + "--" + sanitiseRouteSegment(providerID)
}

// ModelService implements the model-deploy business logic:
//   - Validate the request against the catalog.
//   - Persist a Deploying component row.
//   - Kick off async pod creation, LiteLLM route registration, and virtual key provisioning.
//   - Support list, get, and delete (undeploy) operations.
type ModelService struct {
	componentRepo   dbrepo.ComponentRepository
	keyRepo         dbrepo.KeyRepository
	workerRepo      dbrepo.WorkerRepository
	catalogProvider *catalog.CatalogProvider
	// runtimeFactory creates the local runtime on demand (lazily on first deploy).
	runtimeFactory *runtime.RuntimeFactory
	// workerRegistry is used to build a RemoteRuntime for worker-targeted deploys.
	workerRegistry stream.WorkerRegistry
}

// NewModelService creates a new ModelService.
func NewModelService(
	componentRepo dbrepo.ComponentRepository,
	keyRepo dbrepo.KeyRepository,
	workerRepo dbrepo.WorkerRepository,
	catalogProvider *catalog.CatalogProvider,
	runtimeFactory *runtime.RuntimeFactory,
	workerRegistry stream.WorkerRegistry,
) *ModelService {
	return &ModelService{
		componentRepo:   componentRepo,
		keyRepo:         keyRepo,
		workerRepo:      workerRepo,
		catalogProvider: catalogProvider,
		runtimeFactory:  runtimeFactory,
		workerRegistry:  workerRegistry,
	}
}

// litellmURL returns the LiteLLM gateway base URL from the environment, falling back to
// the default in-cluster address.
func litellmURL() string {
	if v := os.Getenv(litellmURLEnv); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultLiteLLMURL
}

// litellmMasterKey returns the LiteLLM admin key from the environment.
func litellmMasterKey() string {
	return os.Getenv(litellmMasterKeyEnv)
}

// DeployModel validates the request, inserts a Deploying component row, returns 202,
// and kicks off the async deployment goroutine.
func (s *ModelService) DeployModel(ctx context.Context, req apimodels.DeployModelRequest) (*apimodels.DeployModelResponse, error) {
	// 1. Validate type.
	if !validModelTypes[req.Type] {
		return nil, &ValidationError{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("unknown type %q: must be one of llm, embedding, reranker", req.Type),
		}
	}

	// 2. Validate provider exists in catalog.
	comp, err := s.catalogProvider.LoadComponent(req.Type, req.ProviderID)
	if err != nil {
		return nil, &ValidationError{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("unknown provider_id %q for type %q", req.ProviderID, req.Type),
		}
	}

	// 3. Validate required params (model must be present for supported providers).
	if err := validateModelParams(req.Params); err != nil {
		return nil, err
	}

	// 4. Validate worker_selector if provided.
	if req.WorkerSelector != "" {
		worker, err := s.workerRepo.GetByName(ctx, req.WorkerSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to look up worker: %w", err)
		}
		if worker == nil {
			return nil, &ValidationError{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("worker %q not found", req.WorkerSelector),
			}
		}
		if worker.Status != dbmodels.WorkerStatusReady {
			return nil, &ValidationError{
				Code:    http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("worker %q is not ready (status: %s)", req.WorkerSelector, worker.Status),
			}
		}
	}

	// 5. Conflict check — reject if a managed component of this type is already running or deploying.
	active, err := s.componentRepo.ExistsByTypeAndActiveStatus(ctx, req.Type)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing deployments: %w", err)
	}
	if active {
		return nil, &ValidationError{
			Code:    http.StatusConflict,
			Message: fmt.Sprintf("a %s component is already Running or Deploying", req.Type),
		}
	}

	// 6. Insert component row in Deploying state.
	modelName, _ := req.Params["model"].(string)
	name := req.Name
	createdBy := req.CreatedBy
	var workerSelector *string
	if req.WorkerSelector != "" {
		ws := req.WorkerSelector
		workerSelector = &ws
	}

	component := &dbmodels.Component{
		Type:           req.Type,
		Provider:       req.ProviderID,
		Status:         dbmodels.ComponentStatusDeploying,
		Metadata:       map[string]any{"model": modelName},
		Name:           &name,
		CreatedBy:      &createdBy,
		WorkerSelector: workerSelector,
	}

	if err := s.componentRepo.Insert(ctx, component); err != nil {
		return nil, fmt.Errorf("failed to insert component: %w", err)
	}

	// 7. Kick off async deployment.
	go s.deployAsync(context.Background(), component.ID, req, comp)

	return &apimodels.DeployModelResponse{ID: component.ID}, nil
}

// validateModelParams checks that mandatory fields are present in params.
func validateModelParams(params map[string]any) error {
	if modelName, _ := params["model"].(string); modelName == "" {
		return &ValidationError{
			Code:    http.StatusBadRequest,
			Message: `params.model is required`,
		}
	}
	return nil
}

// deployAsync is the background goroutine that drives the full deployment sequence:
//  1. Pull the provider container image (skip if already present)
//  2. Download model weights to the local models directory
//  3. Create the model pod (control-plane Podman or remote worker via gRPC)
//  4. Register the LiteLLM route once the pod is reachable
//  5. Generate and persist a per-model virtual key
//  6. Mark the component Running
func (s *ModelService) deployAsync(ctx context.Context, componentID uuid.UUID, req apimodels.DeployModelRequest, _ *catalogtypes.Component) {
	log := func(msg string, args ...any) {
		logger.InfofCtx(ctx, "[modelmanager] component %s: "+msg, append([]any{componentID}, args...)...)
	}
	fail := func(msg string) {
		logger.ErrorfCtx(ctx, "[modelmanager] component %s: deployment failed: %s", componentID, msg)
		_ = s.componentRepo.UpdateStatus(ctx, componentID, dbmodels.ComponentStatusError, msg)
	}

	modelName, _ := req.Params["model"].(string)
	routeID := buildRouteID(modelName, req.ProviderID)

	// Build the runtime once — reused for image pull, model download, and pod creation.
	rt, err := s.buildRuntime(req.WorkerSelector)
	if err != nil {
		fail(fmt.Sprintf("failed to build runtime: %v", err))
		return
	}

	// ── Step 1: pull container image ──────────────────────────────────────────
	log("pulling container image for provider %q", req.ProviderID)

	if err := s.pullProviderImage(ctx, rt, req.Type, req.ProviderID); err != nil {
		fail(fmt.Sprintf("image pull failed: %v", err))
		return
	}

	// ── Step 2: download model weights ────────────────────────────────────────
	log("downloading model weights %q", modelName)

	if err := s.downloadModel(ctx, rt, modelName); err != nil {
		fail(fmt.Sprintf("model download failed: %v", err))
		return
	}

	// ── Step 3: deploy the model pod ──────────────────────────────────────────
	log("deploying pod for provider %q model %q", req.ProviderID, modelName)

	podHost, err := s.deployModelPod(ctx, componentID, req, rt)
	if err != nil {
		fail(fmt.Sprintf("pod creation failed: %v", err))
		return
	}

	// ── Step 2: determine the LiteLLM api_base ────────────────────────────────
	// For a worker-targeted deploy we route via the worker Caddy to avoid
	// requiring the catalog API to have direct connectivity to the worker pod.
	var apiBase string
	if req.WorkerSelector != "" {
		worker, err := s.workerRepo.GetByName(ctx, req.WorkerSelector)
		if err != nil || worker == nil {
			fail(fmt.Sprintf("failed to resolve worker %q after pod creation: %v", req.WorkerSelector, err))
			return
		}
		if addr, ok := worker.Metadata["address"].(string); ok && addr != "" {
			apiBase = strings.TrimRight(addr, "/") + "/v1"
		} else {
			fail(fmt.Sprintf("worker %q has no address registered", req.WorkerSelector))
			return
		}
	} else {
		// Control-plane: LiteLLM reaches the pod directly using the pod DNS name
		// returned by deployModelPod (host = podSpec.Name from the rendered template).
		apiBase = fmt.Sprintf("http://%s:8000/v1", podHost)
	}

	// ── Step 4: register the LiteLLM route ────────────────────────────────────
	log("registering LiteLLM route %q api_base=%s", routeID, apiBase)

	if err := s.registerLiteLLMRoute(ctx, routeID, modelName, apiBase); err != nil {
		fail(fmt.Sprintf("LiteLLM route registration failed: %v", err))
		return
	}

	// ── Step 5: generate and persist the per-model virtual key ────────────────
	log("generating virtual key for route %q", routeID)

	virtualKey, err := s.generateVirtualKey(ctx, routeID)
	if err != nil {
		fail(fmt.Sprintf("virtual key generation failed: %v", err))
		return
	}

	if err := s.keyRepo.Insert(ctx, &dbmodels.Key{
		ComponentID: componentID,
		VirtualKey:  virtualKey,
		RouteID:     routeID,
	}); err != nil {
		fail(fmt.Sprintf("failed to persist virtual key: %v", err))
		return
	}

	// ── Step 6: mark Running ──────────────────────────────────────────────────
	log("deployment complete; setting status Running")

	if err := s.componentRepo.UpdateStatus(ctx, componentID, dbmodels.ComponentStatusRunning, "Model running"); err != nil {
		logger.ErrorfCtx(ctx, "[modelmanager] component %s: failed to set Running status: %v", componentID, err)
	}
}

// buildRuntime constructs the appropriate Runtime for a given worker selector.
//
// For a named remote worker — RemoteRuntime over the gRPC stream to that worker daemon.
// For the local control-plane — RemoteRuntime pointing at LocalWorkerName (same as the
// DeploymentPlanner) so that Spyre-card discovery works; falls back to runtimeFactory
// when the local worker daemon is not connected (non-Spyre providers only).
func (s *ModelService) buildRuntime(workerSelector string) (runtime.Runtime, error) {
	if workerSelector != "" {
		runtimeTypeStr, ok := s.workerRegistry.WorkerRuntimeType(workerSelector)
		if !ok {
			return nil, fmt.Errorf("worker %q is not connected", workerSelector)
		}
		return remoteruntime.New(workerSelector, runtimetypes.RuntimeType(runtimeTypeStr), s.workerRegistry), nil
	}

	runtimeTypeStr, ok := s.workerRegistry.WorkerRuntimeType(workerconstants.LocalWorkerName)
	if !ok {
		// Local worker daemon not connected — fall back to direct runtime creation.
		// Spyre card discovery will not be available; non-Spyre providers (vllm-cpu) still work.
		if s.runtimeFactory == nil {
			return nil, fmt.Errorf("local runtime factory not configured")
		}
		rt, err := s.runtimeFactory.Create("")
		if err != nil {
			return nil, fmt.Errorf("failed to create local runtime: %w", err)
		}
		return rt, nil
	}

	return remoteruntime.New(workerconstants.LocalWorkerName, runtimetypes.RuntimeType(runtimeTypeStr), s.workerRegistry), nil
}

// pullProviderImage pulls the container image declared in the provider's values.yaml.
// Mirrors pullImagesForDeployment in the application deploy path.
func (s *ModelService) pullProviderImage(ctx context.Context, rt runtime.Runtime, componentType, providerID string) error {
	values, err := s.catalogProvider.LoadComponentValues(componentType, providerID, nil)
	if err != nil {
		return fmt.Errorf("failed to load component values: %w", err)
	}

	img, _ := values["image"].(string)
	if img == "" {
		logger.InfofCtx(ctx, "[modelmanager] no image declared for %s/%s, skipping pull", componentType, providerID)
		return nil
	}

	imgHelper := &image.Images{Runtime: rt}
	if err := imgHelper.IfNotPresent(ctx, []string{img}); err != nil {
		return fmt.Errorf("failed to pull image %q: %w", img, err)
	}

	return nil
}

// downloadModel downloads the model weights to the host models directory.
// Mirrors downloadModels in the application deploy path:
// - Remote worker: forwards COMMAND_TYPE_DOWNLOAD_MODEL over the gRPC stream.
// - Local control-plane: calls helpers.DownloadModelContainer directly.
func (s *ModelService) downloadModel(ctx context.Context, rt runtime.Runtime, modelName string) error {
	if remoteRT, ok := rt.(*remoteruntime.RemoteRuntime); ok {
		_, err := remoteRT.Send(ctx, workerpb.CommandType_COMMAND_TYPE_DOWNLOAD_MODEL, payload.DownloadModel{
			Model: modelName,
		})
		if err != nil {
			return fmt.Errorf("remote model download failed: %w", err)
		}
		return nil
	}

	if err := helpers.DownloadModelContainer(ctx, modelName, utils.GetModelsPath()); err != nil {
		return fmt.Errorf("local model download failed: %w", err)
	}

	return nil
}

// deployModelPod renders the catalog templates for the component and creates the pod via
// the provided runtime (local Podman or remote worker over gRPC).
// It returns the pod hostname (podSpec.Name from the rendered template) which the caller
// uses to construct the LiteLLM api_base for control-plane deploys.
func (s *ModelService) deployModelPod(ctx context.Context, componentID uuid.UUID, req apimodels.DeployModelRequest, rt runtime.Runtime) (string, error) {
	modelName, _ := req.Params["model"].(string)

	// Load catalog values for this provider so the templates render correctly.
	// Params.model maps directly to the "model" key in values.yaml — no remapping needed.
	// All scalar param types are stringified so numeric overrides (e.g. maxModelLen)
	// are applied correctly — matching the FlattenMapWithValues behaviour in the app path.
	paramOverrides := make(map[string]string, len(req.Params))
	for k, v := range req.Params {
		switch sv := v.(type) {
		case string:
			paramOverrides[k] = sv
		case bool, int, int64, float64:
			paramOverrides[k] = fmt.Sprintf("%v", sv)
		}
	}

	values, err := s.catalogProvider.LoadComponentValues(req.Type, req.ProviderID, paramOverrides)
	if err != nil {
		return "", fmt.Errorf("failed to load component values: %w", err)
	}

	// Build the ComponentPlan that PodmanDeployer.deployComponentPods expects.
	compPlan := &deploymenttypes.ComponentPlan{
		ComponentType: req.Type,
		ProviderID:    req.ProviderID,
		DatabaseID:    componentID,
		Params:        req.Params,
		Values:        values,
	}

	// Minimal DeploymentPlan — only the WorkerName is used by getEnvParamsForComponent
	// when deciding how to allocate Spyre cards.
	workerName := workerconstants.LocalWorkerName
	if req.WorkerSelector != "" {
		workerName = req.WorkerSelector
	}
	plan := &deploymenttypes.DeploymentPlan{
		WorkerName:  workerName,
		RuntimeType: rt.Type().String(),
	}

	// Construct a PodmanDeployer scoped to this runtime.
	// appRepo and serviceRepo are nil because this deployer path never touches
	// application or service rows — it manages only the component pod.
	deployer := podmandeployer.NewPodmanDeployer(rt, s.catalogProvider, nil, nil, s.componentRepo)

	// Load runtime metadata and pod templates from the catalog — the same resources
	// loadComponentResources fetches inside the full application deploy path.
	metadata, tmpls, catalogPath, err := deployer.LoadComponentResources(req.Type, req.ProviderID)
	if err != nil {
		return "", fmt.Errorf("failed to load component resources from catalog: %w", err)
	}

	// Allocate Spyre cards before pod creation.
	// getEnvParamsForComponent (called inside DeployComponentPods) reads
	// plan.SpyreCardPool to inject PCI addresses into the rendered pod YAML.
	// If the template declares any spyre-card annotations and the pool is nil,
	// DeployComponentPods will return an error — so we must populate it first.
	if err := s.allocateSpyreCards(ctx, compPlan, plan, tmpls, rt); err != nil {
		return "", fmt.Errorf("spyre card allocation failed: %w", err)
	}

	// DeployComponentPods renders templates, calls podman kube play (or the gRPC
	// equivalent for remote workers), waits for the liveness probe to pass, and
	// populates compPlan.Endpoints with {"<type>": {"host": "<podName>", "port": "8000"}}.
	if err := deployer.DeployComponentPods(ctx, compPlan, metadata, tmpls, catalogPath, plan); err != nil {
		return "", fmt.Errorf("failed to deploy component pods: %w", err)
	}

	// Extract the pod hostname from the populated endpoints map.
	if ep, ok := compPlan.Endpoints[req.Type]; ok {
		if epMap, ok := ep.(map[string]any); ok {
			if host, ok := epMap["host"].(string); ok && host != "" {
				return host, nil
			}
		}
	}

	// If no endpoint was extracted (e.g. template has no ports), fall back to a
	// deterministic name derived from the model name — same convention as the
	// templates themselves use.
	return sanitiseRouteSegment(fmt.Sprintf("vllm-%s", strings.ReplaceAll(modelName, "/", "-"))), nil
}

// allocateSpyreCards checks whether the component's pod templates declare any Spyre card
// requirements (via ai-services.io/<container>--spyre-cards annotations) and, if so,
// discovers the available PCI addresses on the target host and stores them in plan.SpyreCardPool.
//
// This mirrors what DeploymentPlanner.calculateAndAllocateSpyreCards does in the full
// application deploy path. It must be called before DeployComponentPods — if any template
// requires cards and SpyreCardPool is nil, getEnvParamsForComponent returns an error.
//
// For providers that require no Spyre cards (e.g. vllm-cpu) this is a fast no-op.
func (s *ModelService) allocateSpyreCards(
	ctx context.Context,
	compPlan *deploymenttypes.ComponentPlan,
	plan *deploymenttypes.DeploymentPlan,
	tmpls map[string]*texttemplate.Template,
	rt runtime.Runtime,
) error {
	// Scope the catalog provider to the target runtime so CollectSpyreCardsFromTemplates
	// uses the same template set as DeployComponentPods will.
	scopedProvider, err := s.catalogProvider.WithRuntime(plan.RuntimeType)
	if err != nil {
		return fmt.Errorf("failed to scope catalog provider for runtime %q: %w", plan.RuntimeType, err)
	}

	totalRequired, err := scopedProvider.CollectSpyreCardsFromTemplates(ctx, tmpls, compPlan.Values)
	if err != nil {
		return fmt.Errorf("failed to count required Spyre cards: %w", err)
	}

	if totalRequired == 0 {
		// Non-Spyre provider — nothing to do.
		return nil
	}

	logger.InfofCtx(ctx, "[modelmanager] component %s: provider %q requires %d Spyre card(s)",
		compPlan.DatabaseID, compPlan.ProviderID, totalRequired)

	// FindFreeSpyreCards is only available on RemoteRuntime (which wraps both local
	// and remote workers over the gRPC CommandStream).
	remoteRT, ok := rt.(*remoteruntime.RemoteRuntime)
	if !ok {
		return fmt.Errorf("runtime does not support Spyre card discovery (type %T)", rt)
	}

	pciAddresses, err := remoteRT.FindFreeSpyreCards(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover free Spyre cards: %w", err)
	}

	if len(pciAddresses) < totalRequired {
		return fmt.Errorf("insufficient Spyre cards: required %d, available %d", totalRequired, len(pciAddresses))
	}

	plan.SpyreCardPool = &deploymenttypes.SpyreCardPool{
		Addresses: pciAddresses,
	}

	logger.InfofCtx(ctx, "[modelmanager] component %s: allocated %d Spyre card(s) from pool of %d",
		compPlan.DatabaseID, totalRequired, len(pciAddresses))

	return nil
}

// registerLiteLLMRoute calls POST /model/new on the LiteLLM Admin API.
//
// The payload matches config.yaml exactly:
//   - model_name:  "{sanitised_model_name}--{provider_id}"  (double-dash separator)
//   - model:       "hosted_vllm/{modelName}"  (provider prefix inline — no separate custom_llm_provider field)
//   - api_base:    internal pod URL or worker Caddy URL
//   - api_key absent for local vLLM (open endpoint, no auth required)
func (s *ModelService) registerLiteLLMRoute(ctx context.Context, routeID, modelName, apiBase string) error {
	payload := map[string]any{
		"model_name": routeID,
		"litellm_params": map[string]any{
			"model":    "hosted_vllm/" + modelName,
			"api_base": apiBase,
		},
	}

	return s.litellmPost(ctx, "/model/new", payload)
}

// deleteLiteLLMRoute calls DELETE /model/delete on the LiteLLM Admin API.
func (s *ModelService) deleteLiteLLMRoute(ctx context.Context, routeID string) error {
	return s.litellmPost(ctx, "/model/delete", map[string]any{"id": routeID})
}

// generateVirtualKey calls POST /key/generate on the LiteLLM Admin API and returns the
// virtual key string.
func (s *ModelService) generateVirtualKey(ctx context.Context, routeID string) (string, error) {
	payload := map[string]any{
		"key_name": routeID,
		"models":   []string{routeID},
		"duration": nil,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal key/generate payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, litellmURL()+"/key/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create key/generate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+litellmMasterKey())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("key/generate HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("key/generate returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode key/generate response: %w", err)
	}
	if result.Key == "" {
		return "", fmt.Errorf("key/generate response contained no key")
	}

	return result.Key, nil
}

// revokeVirtualKey calls POST /key/delete on the LiteLLM Admin API.
func (s *ModelService) revokeVirtualKey(ctx context.Context, virtualKey string) error {
	return s.litellmPost(ctx, "/key/delete", map[string]any{"keys": []string{virtualKey}})
}

// litellmPost marshals payload and POSTs it to the LiteLLM Admin API.
func (s *ModelService) litellmPost(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload for %s: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, litellmURL()+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+litellmMasterKey())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", path, resp.StatusCode)
	}

	return nil
}

// ListModels returns a paginated list of managed local model components.
func (s *ModelService) ListModels(ctx context.Context, req apimodels.ListModelsRequest) (*apimodels.ListModelsResponse, error) {
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 || req.PageSize > catalogconstants.MaxPageSize {
		req.PageSize = catalogconstants.DefaultPageSize
	}

	offset := (req.Page - 1) * req.PageSize
	components, total, err := s.componentRepo.ListManaged(ctx, req.Type, offset, req.PageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to list managed components: %w", err)
	}

	items := make([]apimodels.ModelListItem, 0, len(components))
	for _, c := range components {
		items = append(items, s.toModelListItem(ctx, c))
	}

	totalPages := int(math.Ceil(float64(total) / float64(req.PageSize)))
	if totalPages < 1 {
		totalPages = 1
	}

	return &apimodels.ListModelsResponse{
		Data: items,
		Pagination: catalogtypes.PaginationMetadata{
			Page:       req.Page,
			PageSize:   req.PageSize,
			TotalItems: total,
			TotalPages: totalPages,
			HasNext:    req.Page < totalPages,
			HasPrev:    req.Page > 1,
		},
	}, nil
}

// GetModel returns the full details of a managed local model.
func (s *ModelService) GetModel(ctx context.Context, id uuid.UUID) (*apimodels.GetModelResponse, error) {
	c, err := s.componentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch component: %w", err)
	}
	if c == nil || c.CreatedBy == nil {
		return nil, &ValidationError{Code: http.StatusNotFound, Message: "model not found"}
	}

	// Resolve provider display name.
	providerName := c.Provider
	if catalogComp, loadErr := s.catalogProvider.LoadComponent(c.Type, c.Provider); loadErr == nil {
		providerName = catalogComp.Name
	}

	// Resolve worker info.
	workerInfo := s.resolveWorkerInfo(ctx, c.WorkerSelector)

	// Collect linked applications.
	appRefs, err := s.componentRepo.GetApplicationsByComponentID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch linked applications: %w", err)
	}
	apps := make([]apimodels.ModelApplicationRef, 0, len(appRefs))
	for _, ref := range appRefs {
		apps = append(apps, apimodels.ModelApplicationRef{ID: ref.ID.String(), Name: ref.Name})
	}

	// Build endpoints list.
	endpoints := make([]apimodels.ModelEndpoint, 0, len(c.Endpoints))
	for _, ep := range c.Endpoints {
		epType, _ := ep["type"].(string)
		epURL, _ := ep["url"].(string)
		endpoints = append(endpoints, apimodels.ModelEndpoint{Type: epType, URL: epURL})
	}

	name := ""
	if c.Name != nil {
		name = *c.Name
	}
	createdBy := ""
	if c.CreatedBy != nil {
		createdBy = *c.CreatedBy
	}

	return &apimodels.GetModelResponse{
		ID:   c.ID,
		Name: name,
		Type: c.Type,
		Provider: apimodels.ModelProviderInfo{
			ID:   c.Provider,
			Name: providerName,
		},
		Worker:       workerInfo,
		Metadata:     c.Metadata,
		Status:       string(c.Status),
		Message:      c.Message,
		Endpoints:    endpoints,
		Applications: apps,
		CreatedBy:    createdBy,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
	}, nil
}

// UndeployModel initiates async undeployment of a managed local model.
// It verifies ownership and that no active applications are using the model before proceeding.
// keepData=true preserves host volumes (model weights on disk); keepData=false deletes everything.
func (s *ModelService) UndeployModel(ctx context.Context, id uuid.UUID, userID string, keepData bool) (*apimodels.UndeployModelResponse, error) {
	c, err := s.componentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch component: %w", err)
	}
	if c == nil || c.CreatedBy == nil {
		return nil, &ValidationError{Code: http.StatusNotFound, Message: "model not found"}
	}

	// Ownership check.
	if *c.CreatedBy != userID {
		return nil, &ValidationError{
			Code:    http.StatusForbidden,
			Message: "only the deploying user may undeploy this model",
		}
	}

	// Check model is not in use by active applications.
	appRefs, err := s.componentRepo.GetApplicationsByComponentID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to check linked applications: %w", err)
	}
	if len(appRefs) > 0 {
		return nil, &ValidationError{
			Code:    http.StatusConflict,
			Message: "model is in use by one or more active applications",
		}
	}

	// Kick off async teardown.
	go s.undeployAsync(context.Background(), c, keepData)

	return &apimodels.UndeployModelResponse{
		ID:      id.String(),
		Message: "Undeploy initiated",
	}, nil
}

// undeployAsync drives the full teardown sequence:
//  1. Deregister the LiteLLM route
//  2. Revoke and delete the virtual key
//  3. Stop and delete the pod + secrets (+ volumes unless keepData=true) via PodmanDeletion
func (s *ModelService) undeployAsync(ctx context.Context, c *dbmodels.Component, keepData bool) {
	log := func(msg string, args ...any) {
		logger.InfofCtx(ctx, "[modelmanager] component %s: undeploy: "+msg, append([]any{c.ID}, args...)...)
	}

	modelName, _ := c.Metadata["model"].(string)
	routeID := buildRouteID(modelName, c.Provider)

	// Step 1: deregister LiteLLM route.
	log("deregistering LiteLLM route %q", routeID)
	if err := s.deleteLiteLLMRoute(ctx, routeID); err != nil {
		logger.WarningfCtx(ctx, "[modelmanager] component %s: failed to deregister route %q (continuing): %v", c.ID, routeID, err)
	}

	// Step 2: revoke virtual key.
	key, err := s.keyRepo.GetByComponentID(ctx, c.ID)
	if err != nil {
		logger.WarningfCtx(ctx, "[modelmanager] component %s: failed to fetch virtual key (continuing): %v", c.ID, err)
	}
	if key != nil {
		log("revoking virtual key for route %q", routeID)
		if err := s.revokeVirtualKey(ctx, key.VirtualKey); err != nil {
			logger.WarningfCtx(ctx, "[modelmanager] component %s: failed to revoke virtual key (continuing): %v", c.ID, err)
		}
		// Step 3: delete keys row.
		if err := s.keyRepo.DeleteByComponentID(ctx, c.ID); err != nil {
			logger.WarningfCtx(ctx, "[modelmanager] component %s: failed to delete keys row (continuing): %v", c.ID, err)
		}
	}

	// Step 4: stop pod + delete secrets/volumes + delete component DB row.
	// PodmanDeletion.deleteOrphanedComponents finds pods by the ai-services.io/template=<componentID>
	// label stamped at deploy time, mirrors the application deletion path exactly.
	log("deleting pod resources (keepData=%v)", keepData)

	workerSelector := ""
	if c.WorkerSelector != nil {
		workerSelector = *c.WorkerSelector
	}

	rt, err := s.buildRuntime(workerSelector)
	if err != nil {
		logger.ErrorfCtx(ctx, "[modelmanager] component %s: failed to build runtime for deletion: %v", c.ID, err)
		// Fall back to DB-only cleanup so the row is not left dangling.
		if dbErr := s.componentRepo.Delete(ctx, c.ID); dbErr != nil {
			logger.ErrorfCtx(ctx, "[modelmanager] component %s: failed to delete component row: %v", c.ID, dbErr)
		}
		return
	}

	podmandeletion.NewPodmanDeletion(rt, nil, nil, s.componentRepo, nil).
		DeleteComponent(ctx, c.ID, keepData)
}

// GetModelKey returns the virtual key for a deployed local model.
func (s *ModelService) GetModelKey(ctx context.Context, componentID uuid.UUID) (*apimodels.GetModelKeyResponse, error) {
	c, err := s.componentRepo.GetByID(ctx, componentID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch component: %w", err)
	}
	if c == nil || c.CreatedBy == nil {
		return nil, &ValidationError{Code: http.StatusNotFound, Message: "model not found"}
	}

	key, err := s.keyRepo.GetByComponentID(ctx, componentID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch virtual key: %w", err)
	}
	if key == nil {
		return nil, &ValidationError{Code: http.StatusNotFound, Message: "virtual key not yet provisioned for this model"}
	}

	return &apimodels.GetModelKeyResponse{
		ComponentID: componentID.String(),
		VirtualKey:  key.VirtualKey,
		RouteID:     key.RouteID,
	}, nil
}

// resolveWorkerInfo returns the worker info for a component's WorkerSelector.
// When WorkerSelector is nil/empty (control-plane deploy) it returns a synthetic
// "Local" entry. For remote workers it looks up the full record from the workers table.
func (s *ModelService) resolveWorkerInfo(ctx context.Context, workerSelector *string) *apimodels.ModelWorkerInfo {
	if workerSelector == nil || *workerSelector == "" {
		return &apimodels.ModelWorkerInfo{ID: workerconstants.LocalWorkerName}
	}
	worker, err := s.workerRepo.GetByName(ctx, *workerSelector)
	if err != nil || worker == nil {
		// Worker not found in DB — return the selector ID as-is.
		return &apimodels.ModelWorkerInfo{ID: *workerSelector}
	}
	return &apimodels.ModelWorkerInfo{
		ID:          worker.Name,
		RuntimeType: string(worker.RuntimeType),
		Status:      string(worker.Status),
	}
}

// toModelListItem converts a DB component to a ModelListItem.
func (s *ModelService) toModelListItem(ctx context.Context, c dbmodels.Component) apimodels.ModelListItem {
	providerName := c.Provider
	if catalogComp, err := s.catalogProvider.LoadComponent(c.Type, c.Provider); err == nil {
		providerName = catalogComp.Name
	}

	name := ""
	if c.Name != nil {
		name = *c.Name
	}

	return apimodels.ModelListItem{
		ID:   c.ID,
		Name: name,
		Type: c.Type,
		Provider: apimodels.ModelProviderInfo{
			ID:   c.Provider,
			Name: providerName,
		},
		Worker:    s.resolveWorkerInfo(ctx, c.WorkerSelector),
		Metadata:  c.Metadata,
		Status:    string(c.Status),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// Made with Bob
