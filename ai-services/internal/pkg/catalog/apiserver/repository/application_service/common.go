package applicationservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog"
	apimodels "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/models"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/deletion"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/deployment"
	deploymenttypes "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/deployment/types"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/constants"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/db/models"
	dbrepo "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/repository"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
	catalogutils "github.com/project-ai-services/ai-services/internal/pkg/catalog/utils"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/validators"
	clitemplates "github.com/project-ai-services/ai-services/internal/pkg/cli/templates"
	consts "github.com/project-ai-services/ai-services/internal/pkg/constants"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/proxy"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/common"
	remoteruntime "github.com/project-ai-services/ai-services/internal/pkg/runtime/remote"
	runtimeTypes "github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/utils"
	gatewaypkg "github.com/project-ai-services/ai-services/internal/pkg/worker/gateway"
	workerconstants "github.com/project-ai-services/ai-services/internal/pkg/worker/constants"
	"github.com/project-ai-services/ai-services/internal/pkg/worker/join"
	"github.com/project-ai-services/ai-services/internal/pkg/worker/stream"
)

const (
	// litellmURLEnvApp and litellmMasterKeyEnvApp read the same env vars as model_service
	// so both paths talk to the same LiteLLM instance.
	litellmURLEnvApp        = "LITELLM_URL"
	litellmMasterKeyEnvApp  = "LITELLM_MASTER_KEY"
	defaultLiteLLMURLApp    = "http://litellm:4000"
	allowedRouteCharsApp    = `[^a-zA-Z0-9-]`

	// managedModelTypesSet is the set of component types that may be pre-deployed
	// by the model-manager API and therefore eligible for reuse.
)

// managedModelTypes lists the component roles that are managed via the model-manager
// API (POST /api/v1/models) and may therefore already be running when an application
// is created.
var managedModelTypes = map[string]bool{
	"llm":       true,
	"embedding": true,
	"reranker":  true,
}

// litellmURLApp returns the LiteLLM base URL from the environment.
func litellmURLApp() string {
	if v := os.Getenv(litellmURLEnvApp); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultLiteLLMURLApp
}

// litellmMasterKeyApp returns the LiteLLM admin key from the environment.
func litellmMasterKeyApp() string {
	return os.Getenv(litellmMasterKeyEnvApp)
}

// sanitiseRouteSegmentApp mirrors the sanitiseRouteSegment helper in model_service.
func sanitiseRouteSegmentApp(s string) string {
	re := regexp.MustCompile(allowedRouteCharsApp)
	s = re.ReplaceAllString(s, "-")
	triple := regexp.MustCompile(`-{3,}`)
	s = triple.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// buildAppRouteID returns the LiteLLM route ID for a model component.
// Convention: {sanitised_model_name}--{provider_id}
func buildAppRouteID(modelName, providerID string) string {
	return sanitiseRouteSegmentApp(modelName) + "--" + sanitiseRouteSegmentApp(providerID)
}

// litellmPostApp POSTs a JSON payload to the LiteLLM Admin API.
func litellmPostApp(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload for %s: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, litellmURLApp()+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+litellmMasterKeyApp())

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

// generateAppVirtualKey calls POST /key/generate on the LiteLLM Admin API and returns
// a virtual key scoped to the given routeID.
func generateAppVirtualKey(ctx context.Context, appRouteKeyName, routeID string) (string, error) {
	payload := map[string]any{
		"key_name": appRouteKeyName,
		"models":   []string{routeID},
		"duration": nil,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal key/generate payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, litellmURLApp()+"/key/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create key/generate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+litellmMasterKeyApp())

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

// DatasourceConnector is the minimal interface used by ApplicationServiceBase for
// post-deploy connector attachment. It exposes only ConnectDatasourcesToApplication,
// which is the single method called after a successful deployment. This narrow interface
// breaks the import cycle between the application_service and repository packages.
type DatasourceConnector interface {
	ConnectDatasourcesToApplication(ctx context.Context, applicationID uuid.UUID, datasourceIDs []uuid.UUID) (*apimodels.ConnectDatasourcesResponse, error)
}

// ValidationError represents a validation error with HTTP status code.
type ValidationError = validators.ValidationError

// ListApplicationsRequest contains parameters for listing applications.
type ListApplicationsRequest struct {
	Page           int
	PageSize       int
	DeploymentType string
	CatalogID      string
}

// DeleteApplicationResponse is the response body for a delete application request.
type DeleteApplicationResponse struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// resourceTotals holds aggregated resource information.
type resourceTotals struct {
	allocatedCPU    int
	allocatedMemory int
	usedCPU         float64
	usedMemory      uint64
	spyreCards      map[string]bool
}

// ValidatePaginationParams validates and returns pagination parameters with defaults.
func ValidatePaginationParams(page, pageSize int) (int, int, error) {
	// Apply defaults
	if page == 0 {
		page = constants.MinPage
	}
	if pageSize == 0 {
		pageSize = constants.DefaultPageSize
	}

	// Validate page
	if page < constants.MinPage {
		return 0, 0, fmt.Errorf("invalid page parameter: must be a positive integer")
	}

	// Validate page_size
	if pageSize < constants.MinPage || pageSize > constants.MaxPageSize {
		return 0, 0, fmt.Errorf("invalid page_size parameter: must be between 1 and %d", constants.MaxPageSize)
	}

	return page, pageSize, nil
}

// ApplicationServiceBase holds the fields and methods that are identical across all
// runtime implementations. The Podman and OpenShift concrete service types embed this
// struct and inherit these methods without any changes.
type ApplicationServiceBase struct {
	AppRepo               dbrepo.ApplicationRepository
	ServiceRepo           dbrepo.ServiceRepository
	ComponentRepo         dbrepo.ComponentRepository
	ServiceDependencyRepo dbrepo.ServiceDependencyRepository
	Provider              *catalog.CatalogProvider
	DeploymentPlanner     *deployment.DeploymentPlanner
	DeploymentExecutor    *deployment.DeploymentExecutor
	DeletionExecutor      *deletion.DeletionExecutor
	Validator             *validators.ApplicationValidator

	// DeploymentRegistry tracks in-flight deployments so they can be cancelled
	// by a concurrent delete request. Nil means no cancellation (e.g. OpenShift stub).
	DeploymentRegistry *DeploymentRegistry

	// DatasourceService is optional. When set, connector refs supplied in the create
	// request are propagated to eligible Digitize services after the application reaches
	// Running status. When nil, connector attachment is skipped (e.g. test environments
	// where no datasource service is configured).
	DatasourceService DatasourceConnector

	// WorkerRegistry is used to resolve a remote runtime for worker-hosted applications.
	WorkerRegistry stream.WorkerRegistry

	// KeyRepo is optional. When set, it is used during the application create flow to
	// check whether a running managed model already has a LiteLLM key (i.e. was
	// already registered), so the route is not registered twice.
	KeyRepo dbrepo.KeyRepository
}

// createRuntime returns the runtime.Runtime appropriate for app.
// Every application has a WorkerID (NOT NULL column, migration 20260801000003). It
// resolves the worker name and builds a RemoteRuntime over the gRPC CommandStream —
// this covers both the "Local" worker and actual remote workers.
func (s *ApplicationServiceBase) createRuntime(app *models.Application) (runtime.Runtime, error) {
	if app.WorkerID == nil {
		return nil, fmt.Errorf("application %s has no worker_id: every application must be deployed through a worker", app.ID)
	}

	if s.WorkerRegistry == nil {
		return nil, fmt.Errorf("worker deployment not configured on this server")
	}

	workerName, ok := s.WorkerRegistry.WorkerNameByID(*app.WorkerID)
	if !ok {
		return nil, fmt.Errorf("worker %s for application %s is not connected", app.WorkerID, app.ID)
	}

	rtStr, _ := s.WorkerRegistry.WorkerRuntimeType(workerName)
	rt, err := runtime.NewRuntimeFactory(runtimeTypes.RuntimeType(rtStr)).CreateRemote(workerName, s.WorkerRegistry, catalogutils.AppNamespace(app.ID))
	if err != nil {
		return nil, fmt.Errorf("create remote runtime for worker %q: %w", workerName, err)
	}

	return rt, nil
}

// buildWorkerInfo resolves the worker name and runtime type for the given worker UUID.
// Delegates to WorkerRegistry.WorkerInfoByID which tries the live registry first
// and falls back to the DB for disconnected workers.
func (s *ApplicationServiceBase) buildWorkerInfo(ctx context.Context, workerID uuid.UUID) (*types.ApplicationWorker, error) {
	if s.WorkerRegistry == nil {
		return nil, fmt.Errorf("worker registry not configured")
	}

	name, rtStr := s.WorkerRegistry.WorkerInfoByID(ctx, workerID)

	return &types.ApplicationWorker{
		ID:          workerID.String(),
		Name:        name,
		RuntimeType: rtStr,
	}, nil
}

// buildApplication creates an Application from a models.Application.
func (s *ApplicationServiceBase) buildApplication(ctx context.Context, app models.Application) (types.Application, error) {
	// Get type (display name) from catalog metadata
	typeName, err := s.getApplicationType(app.CatalogID, app.DeploymentType)
	if err != nil {
		return types.Application{}, fmt.Errorf("failed to get application type for catalog_id '%s': %w", app.CatalogID, err)
	}

	appData := types.Application{
		ID:             app.ID.String(),
		Name:           app.Name,
		CatalogID:      app.CatalogID,
		DeploymentType: string(app.DeploymentType),
		Type:           typeName,
		Status:         string(app.Status),
		Message:        app.Message,
		Version:        app.Version,
		CreatedAt:      app.CreatedAt.Format(constants.RFC3339WithTimezone),
		UpdatedAt:      app.UpdatedAt.Format(constants.RFC3339WithTimezone),
	}

	if app.WorkerID == nil {
		return types.Application{}, fmt.Errorf("application %s has no worker_id", app.ID)
	}

	worker, err := s.buildWorkerInfo(ctx, *app.WorkerID)
	if err != nil {
		return types.Application{}, fmt.Errorf("failed to resolve worker for application %s: %w", app.ID, err)
	}

	appData.Worker = worker

	// Add services array only for architectures (not for individual services)
	if app.DeploymentType == models.DeploymentTypeArchitectures && len(app.Services) > 0 {
		appData.Services = s.buildServiceStatuses(app.Services)
	}

	return appData, nil
}

// buildServiceStatuses creates ApplicationService array from models.Service slice.
func (s *ApplicationServiceBase) buildServiceStatuses(services []models.Service) []types.ApplicationService {
	statuses := make([]types.ApplicationService, 0, len(services))

	for _, svc := range services {
		// Get service display name from catalog metadata
		serviceDisplayName := svc.CatalogID // Default to catalog_id
		if service, err := s.Provider.LoadService(svc.CatalogID); err == nil && service.Name != "" {
			serviceDisplayName = service.Name
		}

		statuses = append(statuses, types.ApplicationService{
			ID:      svc.ID.String(),
			Type:    serviceDisplayName,
			Status:  string(svc.Status),
			Message: svc.Message,
		})
	}

	return statuses
}

// getApplicationType retrieves the application type from catalog metadata.
func (s *ApplicationServiceBase) getApplicationType(catalogID string, deploymentType models.DeploymentType) (string, error) {
	if deploymentType == models.DeploymentTypeArchitectures {
		arch, err := s.Provider.LoadArchitecture(catalogID)
		if err != nil {
			return "", fmt.Errorf("failed to load architecture metadata: %w", err)
		}

		return arch.Name, nil
	}

	// For services
	service, err := s.Provider.LoadService(catalogID)
	if err != nil {
		return "", fmt.Errorf("failed to load service metadata: %w", err)
	}

	return service.Name, nil
}

// UpdateApplication updates the display name of an existing application.
func (s *ApplicationServiceBase) UpdateApplication(ctx context.Context, id uuid.UUID, userID, newName string) (*types.Application, error) {
	existingApp, err := s.AppRepo.GetByName(ctx, newName)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing application: %w", err)
	}
	if existingApp != nil {
		// Application with this name already exists - return conflict error
		return nil, &ValidationError{
			Code:    http.StatusConflict,
			Message: fmt.Sprintf(ErrMsgApplicationNameExists, newName),
		}
	}

	app, err := s.AppRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get application: %w", err)
	}
	if app == nil {
		return nil, &ValidationError{
			Code:    http.StatusNotFound,
			Message: ErrMsgApplicationNotFound,
		}
	}
	if app.CreatedBy != userID {
		return nil, &ValidationError{
			Code:    http.StatusForbidden,
			Message: ErrMsgUserNotOwner,
		}
	}

	err = s.AppRepo.UpdateDeploymentName(ctx, id, newName)
	if err != nil {
		return nil, fmt.Errorf("failed to update name: %w", err)
	}
	updatedApp, err := s.AppRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch updated application %w", err)
	}
	if updatedApp == nil {
		return nil, &ValidationError{
			Code:    http.StatusNotFound,
			Message: ErrMsgApplicationNotFound,
		}
	}

	appData, err := s.buildApplication(ctx, *updatedApp)
	if err != nil {
		return nil, err
	}

	return &appData, nil
}

// GetApplicationByID retrieves application details by ID including all services and components.
func (s *ApplicationServiceBase) GetApplicationByID(ctx context.Context, id uuid.UUID) (*types.Application, error) {
	// Fetch application from database
	app, err := s.AppRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get application: %w", err)
	}
	if app == nil {
		return nil, &ValidationError{
			Code:    http.StatusNotFound,
			Message: ErrMsgApplicationNotFound,
		}
	}
	// Build complete response with services and components
	return s.buildGetApplicationResponse(ctx, app)
}

// buildGetApplicationResponse constructs the application response with type info and nested services.
func (s *ApplicationServiceBase) buildGetApplicationResponse(ctx context.Context, app *models.Application) (*types.Application, error) {
	// Get application type display name from catalog metadata
	typeName, err := s.getApplicationType(app.CatalogID, app.DeploymentType)
	if err != nil {
		return nil, fmt.Errorf("failed to get application type for catalog_id '%s': %w", app.CatalogID, err)
	}
	// Build base application response
	appresponse := &types.Application{
		ID:             app.ID.String(),
		Name:           app.Name,
		CatalogID:      app.CatalogID,
		DeploymentType: string(app.DeploymentType),
		Type:           typeName,
		Status:         string(app.Status),
		Message:        app.Message,
		Version:        app.Version,
		CreatedAt:      app.CreatedAt.Format(constants.RFC3339WithTimezone),
		UpdatedAt:      app.UpdatedAt.Format(constants.RFC3339WithTimezone),
	}

	if app.WorkerID == nil {
		return nil, fmt.Errorf("application %s has no worker_id", app.ID)
	}

	worker, err := s.buildWorkerInfo(ctx, *app.WorkerID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve worker for application %s: %w", app.ID, err)
	}

	appresponse.Worker = worker

	// Load services with their components if present
	if len(app.Services) > 0 {
		appresponse.Services, err = s.loadApplicationServices(ctx, app.Services)
		if err != nil {
			return nil, fmt.Errorf("failed to get application services: %w", err)
		}
	}

	return appresponse, nil
}

// loadApplicationServices transforms service models to API response objects with components.
func (s *ApplicationServiceBase) loadApplicationServices(ctx context.Context, services []models.Service) ([]types.ApplicationService, error) {
	appServices := []types.ApplicationService{}
	for _, service := range services {
		// Build application service response
		serviceDisplayName := service.CatalogID
		if service, err := s.Provider.LoadService(service.CatalogID); err == nil && service.Name != "" {
			serviceDisplayName = service.Name
		}

		appService := types.ApplicationService{
			ID:        service.ID.String(),
			Type:      serviceDisplayName,
			CatalogID: service.CatalogID,
			Endpoints: service.Endpoints,
			Version:   service.Version,
			Status:    string(service.Status),
			CreatedAt: service.CreatedAt.Format(constants.RFC3339WithTimezone),
			UpdatedAt: service.UpdatedAt.Format(constants.RFC3339WithTimezone),
		}

		// Get all dependencies for this service
		serviceDependencies, err := s.ServiceDependencyRepo.GetDependenciesByServiceID(ctx, service.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get application dependencies: %w", err)
		}

		// Load component details from dependencies
		appService.Component, err = s.loadServiceComponents(ctx, serviceDependencies)
		if err != nil {
			return nil, err
		}
		appServices = append(appServices, appService)
	}

	return appServices, nil
}

// loadServiceComponents extracts component details from service dependencies.
func (s *ApplicationServiceBase) loadServiceComponents(ctx context.Context, sd []models.ServiceDependency) ([]types.ServiceComponentResp, error) {
	components := []types.ServiceComponentResp{}
	for _, dependency := range sd {
		// Only process component-type dependencies
		if dependency.DependencyType == models.DependencyTypeComponent {
			// Fetch component details from database
			component, err := s.ComponentRepo.GetByID(ctx, dependency.DependencyID)
			if err != nil {
				return nil, fmt.Errorf("failed to get component: %w", err)
			}
			if component == nil {
				continue
			}

			// Get provider name from catalog metadata using existing LoadComponent helper
			componentMetadata, err := s.Provider.LoadComponent(component.Type, component.Provider)
			if err != nil {
				return nil, fmt.Errorf("failed to load component metadata for %s/%s: %w", component.Type, component.Provider, err)
			}

			providerName := component.Provider // Default to provider ID
			if componentMetadata != nil && componentMetadata.Name != "" {
				providerName = componentMetadata.Name
			}

			// Transform to response object
			temp := types.ServiceComponentResp{
				ID:   component.ID.String(),
				Type: component.Type,
				Provider: types.ProviderInfo{
					ID:   component.Provider,
					Name: providerName,
				},
				Status:   string(component.Status),
				Message:  component.Message,
				Metadata: component.Metadata,
			}
			components = append(components, temp)
		}
	}

	return components, nil
}

// filterComponentMetadata filters component parameters to exclude sensitive data.
func (s *ApplicationServiceBase) filterComponentMetadata(ctx context.Context, provider *catalog.CatalogProvider, componentType, providerID string, params map[string]any) (map[string]any, error) {
	if params == nil {
		return nil, nil
	}

	// Load component schema to determine which fields are sensitive
	schema, err := provider.GetComponentProviderParams(ctx, componentType, providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load schema for component %s/%s: %w", componentType, providerID, err)
	}

	// Extract properties from schema
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema for component %s/%s has no properties", componentType, providerID)
	}

	// Filter out sensitive fields recursively
	metadata, err := s.filterSensitiveFields(ctx, params, properties)
	if err != nil {
		return nil, fmt.Errorf("failed to filter sensitive fields: %w", err)
	}

	return metadata, nil
}

// filterSensitiveFields recursively filters out sensitive fields from params based on schema properties.
func (s *ApplicationServiceBase) filterSensitiveFields(ctx context.Context, params map[string]any, properties map[string]any) (map[string]any, error) {
	metadata := make(map[string]any)

	for key, value := range params {
		// Check if this field exists in the schema
		fieldSchema, exists := properties[key].(map[string]any)
		if !exists {
			// If field not in schema, skip it (don't include in metadata)
			continue
		}

		// Check if field is marked as sensitive (format: "password")
		if format, hasFormat := fieldSchema["format"].(string); hasFormat && format == "password" {
			logger.DebugfCtx(ctx, "Excluding sensitive field '%s' from component metadata", key)

			continue
		}

		// Handle nested objects recursively
		if valueMap, isMap := value.(map[string]any); isMap {
			// Check if the field schema has nested properties
			if nestedProps, hasNestedProps := fieldSchema["properties"].(map[string]any); hasNestedProps {
				// Recursively filter nested object
				filteredNested, err := s.filterSensitiveFields(ctx, valueMap, nestedProps)
				if err != nil {
					return nil, fmt.Errorf("failed to filter nested field '%s': %w", key, err)
				}
				metadata[key] = filteredNested

				continue
			}
		}

		// Include non-sensitive fields
		metadata[key] = value
	}

	return metadata, nil
}

// InsertDeploymentRecords inserts all database records for the deployment plan.
// This includes: application, services, components (new ones), and service dependencies.
func (s *ApplicationServiceBase) InsertDeploymentRecords(
	ctx context.Context,
	plan *deployment.DeploymentPlan,
	createdBy string,
) error {
	// 1. Insert application record
	if err := s.insertApplicationRecord(ctx, plan, createdBy); err != nil {
		return err
	}

	// 2. Insert component records
	componentIDMap, err := s.insertComponentRecords(ctx, plan, createdBy)
	if err != nil {
		return err
	}

	// 3. Insert service records and their dependencies
	if err := s.insertServiceRecords(ctx, plan, componentIDMap); err != nil {
		return err
	}

	return nil
}

// insertApplicationRecord inserts the application record into the database.
func (s *ApplicationServiceBase) insertApplicationRecord(
	ctx context.Context,
	plan *deployment.DeploymentPlan,
	createdBy string,
) error {
	app := &models.Application{
		ID:             plan.ApplicationID,
		Name:           plan.ApplicationName,
		CatalogID:      plan.CatalogID,
		DeploymentType: catalogutils.GetDeploymentType(plan.IsArchitecture),
		Status:         models.ApplicationStatusDownloading,
		Message:        "Initializing deployment",
		Version:        plan.Version,
		CreatedBy:      createdBy,
	}

	// Attach the worker FK. Every deployment goes through a worker (the local worker
	// is used for local deployments). WorkerName is always set by the CLI since the
	// --worker flag defaults to workerconstants.LocalWorkerName.
	if plan.WorkerName == "" {
		return fmt.Errorf("worker name is required for application deployment; use --worker to specify a worker (default: %q)", workerconstants.LocalWorkerName)
	}

	dbID, ok := s.DeploymentPlanner.WorkerDBID(plan.WorkerName)
	if !ok {
		return fmt.Errorf("worker %q is not registered or not connected; run 'worker join' first", plan.WorkerName)
	}

	app.WorkerID = &dbID

	if err := s.AppRepo.Insert(ctx, app); err != nil {
		return fmt.Errorf("failed to insert application: %w", err)
	}

	return nil
}

// insertComponentRecords inserts component records and returns a map of component hashes to UUIDs.
// For managed-model component types (llm/embedding/reranker), it first checks whether a
// running managed component already exists for the same type+provider. When one is found the
// existing row is reused: comp.PreDeployed is set to true, comp.DatabaseID is set to the
// existing UUID, and no new DB row is inserted.
func (s *ApplicationServiceBase) insertComponentRecords(
	ctx context.Context,
	plan *deployment.DeploymentPlan,
	createdBy string,
) (map[string]uuid.UUID, error) {
	componentIDMap := make(map[string]uuid.UUID)
	scopedProvider, err := s.Provider.WithRuntime(plan.RuntimeType)
	if err != nil {
		return nil, fmt.Errorf("failed to scope catalog provider for runtime %q: %w", plan.RuntimeType, err)
	}

	// Resolve worker DB UUID once — used for managed-model components so they
	// appear in ListManaged (which filters on created_by IS NOT NULL).
	var workerDBID *uuid.UUID
	if dbID, ok := s.DeploymentPlanner.WorkerDBID(plan.WorkerName); ok {
		workerDBID = &dbID
	}

	for hash, comp := range plan.Components {
		// For managed-model types, check whether a running instance already exists.
		if managedModelTypes[comp.ComponentType] {
			existing, lookupErr := s.ComponentRepo.GetRunningByTypeAndProvider(ctx, comp.ComponentType, comp.ProviderID)
			if lookupErr != nil {
				return nil, fmt.Errorf("failed to look up running component for %s/%s: %w", comp.ComponentType, comp.ProviderID, lookupErr)
			}
			if existing != nil {
				// Reuse the existing component — no new DB row needed.
				comp.PreDeployed = true
				comp.DatabaseID = existing.ID
				componentIDMap[hash] = existing.ID
				continue
			}
		}

		instanceUUID := uuid.New()

		// Filter metadata to exclude sensitive data based on schema
		metadata, err := s.filterComponentMetadata(ctx, scopedProvider, comp.ComponentType, comp.ProviderID, comp.Params)
		if err != nil {
			return nil, fmt.Errorf("failed to filter component metadata for %s: %w", hash, err)
		}

		component := &models.Component{
			ID:       instanceUUID,
			Type:     comp.ComponentType,
			Provider: comp.ProviderID,
			Status:   models.ComponentStatusInitializing,
			Version:  comp.Version,
			Metadata: metadata,
		}

		// For managed-model types (llm/embedding/reranker), populate the fields that
		// model_service sets — created_by, worker_id, and a human-readable name derived
		// from the model param. Without these, ListManaged (which filters created_by IS NOT NULL)
		// would not return this component, making it invisible to GET /api/v1/models.
		if managedModelTypes[comp.ComponentType] {
			component.CreatedBy = &createdBy
			component.WorkerID = workerDBID
			// Build a name from model param (same convention used by model_service).
			modelName, _ := comp.Params["model"].(string)
			if modelName == "" {
				modelName, _ = comp.Values["model"].(string)
			}
			if modelName != "" {
				name := modelName
				component.Name = &name
			}
		}

		if err := s.ComponentRepo.Insert(ctx, component); err != nil {
			return nil, fmt.Errorf("failed to insert component %s: %w", hash, err)
		}

		componentIDMap[hash] = instanceUUID
		comp.DatabaseID = instanceUUID
	}

	return componentIDMap, nil
}

// insertServiceRecords inserts service records and their dependencies.
func (s *ApplicationServiceBase) insertServiceRecords(
	ctx context.Context,
	plan *deployment.DeploymentPlan,
	componentIDMap map[string]uuid.UUID,
) error {
	for serviceID, svc := range plan.Services {
		service := &models.Service{
			ID:        uuid.Nil,
			AppID:     plan.ApplicationID,
			CatalogID: svc.CatalogID,
			Status:    models.ServiceStatusInitializing,
			Version:   svc.Version,
		}

		if err := s.ServiceRepo.Insert(ctx, service); err != nil {
			return fmt.Errorf("failed to insert service %s: %w", serviceID, err)
		}

		svc.DatabaseID = service.ID

		if err := s.insertServiceDependencies(ctx, service.ID, svc.ComponentRefs, componentIDMap); err != nil {
			return err
		}
	}

	return nil
}

// insertServiceDependencies inserts dependencies between services and components.
func (s *ApplicationServiceBase) insertServiceDependencies(
	ctx context.Context,
	serviceID uuid.UUID,
	componentRefs []string,
	componentIDMap map[string]uuid.UUID,
) error {
	for _, compHash := range componentRefs {
		componentID, exists := componentIDMap[compHash]
		if !exists {
			return fmt.Errorf("component hash %s not found in component map", compHash)
		}

		dependency := &models.ServiceDependency{
			ServiceID:      serviceID,
			DependencyID:   componentID,
			DependencyType: models.DependencyTypeComponent,
		}

		if err := s.ServiceDependencyRepo.AddDependency(ctx, dependency); err != nil {
			return fmt.Errorf("failed to add service dependency: %w", err)
		}
	}

	return nil
}

// ListApplications retrieves a paginated list of applications with filters.
func (s *ApplicationServiceBase) ListApplications(ctx context.Context, req ListApplicationsRequest) (*types.ApplicationListResponse, error) {
	if req.Page < 1 {
		return nil, fmt.Errorf("page must be greater than 0")
	}
	if req.PageSize < 1 {
		return nil, fmt.Errorf("pageSize must be greater than 0")
	}

	filters := &dbrepo.ApplicationFilters{
		DeploymentType: req.DeploymentType,
		CatalogID:      req.CatalogID,
		Limit:          req.PageSize,
		Offset:         (req.Page - 1) * req.PageSize,
	}

	totalCount, err := s.AppRepo.GetCount(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to get application count: %w", err)
	}

	applications, err := s.AppRepo.GetAll(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve applications: %w", err)
	}

	apps := make([]types.Application, 0, len(applications))
	for _, app := range applications {
		appData, err := s.buildApplication(ctx, app)
		if err != nil {
			return nil, err
		}

		apps = append(apps, appData)
	}

	totalPages := 0
	if totalCount > 0 {
		totalPages = (totalCount + req.PageSize - 1) / req.PageSize
	}

	return &types.ApplicationListResponse{
		Data: apps,
		Pagination: types.PaginationMetadata{
			Page:       req.Page,
			PageSize:   req.PageSize,
			TotalItems: totalCount,
			TotalPages: totalPages,
			HasNext:    req.Page < totalPages,
			HasPrev:    req.Page > 1,
		},
	}, nil
}

// CreateApplication validates, plans, persists, and asynchronously deploys a new application
// for the given runtime type.
func (s *ApplicationServiceBase) CreateApplication(ctx context.Context, req apimodels.CreateApplicationRequest) (*apimodels.CreateApplicationResponse, error) {
	effectiveRuntimeType, err := s.resolveRuntimeForCreateApplication(ctx, &req)
	if err != nil {
		return nil, err
	}

	if err := s.validateCreateApplicationRequest(ctx, req, effectiveRuntimeType); err != nil {
		return nil, err
	}

	runtimeScopedPlanner, err := s.DeploymentPlanner.WithRuntime(effectiveRuntimeType)
	if err != nil {
		return nil, fmt.Errorf("failed to scope deployment planner for runtime %q: %w", effectiveRuntimeType, err)
	}

	plan, err := s.createDeploymentPlan(ctx, runtimeScopedPlanner, req)
	if err != nil {
		return nil, err
	}

	if err := s.InsertDeploymentRecords(ctx, plan, req.CreatedBy); err != nil {
		return nil, fmt.Errorf("failed to insert deployment records: %w", err)
	}

	deployCtx := s.prepareCreateDeploymentContext(ctx, plan.ApplicationID)
	go s.executeDeploymentAsync(deployCtx, plan, req)

	return &apimodels.CreateApplicationResponse{ID: plan.ApplicationID.String()}, nil
}

func (s *ApplicationServiceBase) resolveRuntimeForCreateApplication(ctx context.Context, req *apimodels.CreateApplicationRequest) (string, error) {
	if req.WorkerName == "" {
		req.WorkerName = workerconstants.LocalWorkerName
	}

	return s.DeploymentPlanner.ResolveRuntimeType(ctx, req.WorkerName)
}

func (s *ApplicationServiceBase) validateCreateApplicationRequest(
	ctx context.Context,
	req apimodels.CreateApplicationRequest,
	runtimeType string,
) error {
	existingApp, err := s.AppRepo.GetByName(ctx, req.Name)
	if err != nil {
		return fmt.Errorf("failed to check for existing application: %w", err)
	}
	if existingApp != nil {
		return &ValidationError{
			Code:    http.StatusConflict,
			Message: fmt.Sprintf(ErrMsgApplicationNameExists, req.Name),
		}
	}

	scopedProvider, err := s.Provider.WithRuntime(runtimeType)
	if err != nil {
		return fmt.Errorf("failed to scope catalog provider for runtime %q: %w", runtimeType, err)
	}

	requestValidator := validators.NewApplicationValidator(scopedProvider)
	if err := requestValidator.ValidateDeploymentRequest(ctx, req); err != nil {
		return err
	}

	return nil
}

func (s *ApplicationServiceBase) createDeploymentPlan(
	ctx context.Context,
	planner *deployment.DeploymentPlanner,
	req apimodels.CreateApplicationRequest,
) (*deployment.DeploymentPlan, error) {
	plan, err := planner.PlanDeployment(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployment plan: %w", err)
	}

	return plan, nil
}

func (s *ApplicationServiceBase) prepareCreateDeploymentContext(ctx context.Context, applicationID uuid.UUID) context.Context {
	// Build the deployment context before launching the goroutine so Register is
	// called synchronously and cancellation cannot race with registration.
	deployCtx := context.Background()
	if id, ok := ctx.Value(logger.RequestIDKey).(string); ok && id != "" {
		deployCtx = context.WithValue(deployCtx, logger.RequestIDKey, id)
	}

	if s.DeploymentRegistry != nil {
		deployCtx = s.DeploymentRegistry.Register(deployCtx, applicationID)
	}

	return deployCtx
}

// executeDeploymentAsync runs the deployment in a background goroutine.
// deployCtx is already derived and registered with the DeploymentRegistry by the caller.
func (s *ApplicationServiceBase) executeDeploymentAsync(deployCtx context.Context, plan *deployment.DeploymentPlan, req apimodels.CreateApplicationRequest) {
	ctx := deployCtx

	// Deregister on any exit path — success, error, or panic.
	if s.DeploymentRegistry != nil {
		defer s.DeploymentRegistry.Deregister(plan.ApplicationID)
	}

	defer func() {
		if r := recover(); r != nil {
			logger.ErrorfCtx(ctx, "Panic recovered in deployment goroutine for application %s: %v", plan.ApplicationName, r)

			errMsg := fmt.Sprintf("Deployment panic: %v", r)
			if updateErr := catalogutils.UpdateApplicationStatus(ctx, s.AppRepo, plan.ApplicationID.String(), models.ApplicationStatusError, errMsg); updateErr != nil {
				logger.ErrorfCtx(ctx, "Failed to update application status after panic: %v", updateErr)
			}
		}
	}()

	// ── Pre-deploy: handle components that are already running (PreDeployed=true) ──
	//
	// insertComponentRecords marked managed-model components as PreDeployed when a
	// running managed instance was found.  For those components:
	//   • Endpoints from the DB are copied into the ComponentPlan.
	//   • The LiteLLM route is registered if missing (first-time for this component).
	//   • A fresh per-application virtual key is generated and injected into every
	//     dependent service's Values so the pod Secret template renders the key.
	//
	// For components that are NOT PreDeployed (new deploy), a PostComponentHook is
	// registered on the plan.  The deployer calls it after all component pods are
	// running but before services start.  The hook does the same LiteLLM + key
	// work using the endpoint that the deployer populated in ComponentPlan.Endpoints.
	if err := s.resolveModelsAndPrepareKeys(ctx, plan); err != nil {
		logger.ErrorfCtx(ctx, "Model resolve / LiteLLM prep failed for application %s: %v", plan.ApplicationName, err)
		if updateErr := catalogutils.UpdateApplicationStatus(ctx, s.AppRepo, plan.ApplicationID.String(), models.ApplicationStatusError, err.Error()); updateErr != nil {
			logger.ErrorfCtx(ctx, "Failed to update application status to Error: %v", updateErr)
		}
		return
	}

	// Register the post-component hook for newly deployed managed-model components.
	// The hook is a no-op when KeyRepo is nil or when all model components are PreDeployed.
	plan.PostComponentHook = s.buildPostComponentHook(plan)

	err := s.DeploymentExecutor.ExecuteWithPlan(ctx, plan, req)
	if err != nil {
		// Context cancelled — deletion is in charge of status, exit silently.
		if ctx.Err() != nil {
			logger.InfofCtx(ctx, "Deployment cancelled for application %s (deletion in progress)", plan.ApplicationName)

			return
		}

		logger.ErrorfCtx(ctx, "Deployment failed for application %s: %v", plan.ApplicationName, err)

		if updateErr := catalogutils.UpdateApplicationStatus(ctx, s.AppRepo, plan.ApplicationID.String(), models.ApplicationStatusError, err.Error()); updateErr != nil {
			logger.ErrorfCtx(ctx, "Failed to update application status to Error: %v", updateErr)
		}

		return
	}

	logger.InfolnCtx(ctx, fmt.Sprintf("Deployment completed successfully for application %s", plan.ApplicationName))

	// Post-deploy connector attachment: once the application is Running, connect any
	// datasource connectors that were specified in the create request.
	// Failures here do NOT revert the Running status — the deployment succeeded; only
	// the connector attachment is partial. Errors are logged for operator visibility.
	s.attachConnectorsPostDeploy(ctx, plan.ApplicationID, req.Services)
	logger.InfolnCtx(ctx, fmt.Sprintf("Post-deploy connector attachment completed for application %s", plan.ApplicationName))
}

// resolveModelsAndPrepareKeys iterates over all managed-model components in the plan
// (type = llm / embedding / reranker), checks whether a running managed component
// already exists for that type+provider, and:
//
//   - If a running component exists → marks it PreDeployed, copies its endpoint(s),
//     and registers the LiteLLM route only when no key row exists yet (first-time path).
//   - If no running component exists → leaves the component for normal pod deployment;
//     does NOT register the route (model_service does that after the pod is healthy).
//
// In both cases a fresh per-application virtual key is generated via LiteLLM and
// stored in every service Values map under "litellm" so catalog templates can render
// a pod Secret with the key.
//
// When KeyRepo is nil (e.g. test environments) the function skips the DB lookup and
// the LiteLLM calls gracefully, logging a warning.
func (s *ApplicationServiceBase) resolveModelsAndPrepareKeys(ctx context.Context, plan *deployment.DeploymentPlan) error {
	if s.KeyRepo == nil {
		logger.WarningfCtx(ctx, "[app-deploy] KeyRepo not configured; skipping model reuse and LiteLLM key generation")
		return nil
	}

	for _, comp := range plan.Components {
		if !managedModelTypes[comp.ComponentType] {
			continue
		}

		litellmInfo, err := s.resolveModelComponent(ctx, plan, comp)
		if err != nil {
			return fmt.Errorf("failed to resolve model component %s/%s: %w", comp.ComponentType, comp.ProviderID, err)
		}

		if litellmInfo == nil {
			// No running managed component found — normal deploy path, no LiteLLM prep needed here.
			logger.InfofCtx(ctx, "[app-deploy] no running managed component for %s/%s; will deploy normally", comp.ComponentType, comp.ProviderID)
			continue
		}

		// Inject model name + endpoint into service Values so templates have access.
		s.injectLiteLLMIntoServices(ctx, plan, comp, litellmInfo)

		logger.InfofCtx(ctx, "[app-deploy] component %s/%s: LiteLLM route=%s firstReg=%v",
			comp.ComponentType, comp.ProviderID, litellmInfo.RouteID, litellmInfo.FirstRegistration)
	}

	return nil
}

// resolveModelComponent handles the LiteLLM prep for a managed-model component that was
// already marked PreDeployed=true by insertComponentRecords (comp.DatabaseID is the
// existing running component's UUID).  It:
//  1. Fetches the existing component to get its endpoints and model metadata.
//  2. Copies endpoints into ComponentPlan so mergeComponentEndpoints works normally.
//  3. Checks whether a LiteLLM key already exists; if not, registers the route.
//  4. Generates and returns a fresh per-application virtual key.
//
// Returns nil when comp.PreDeployed is false (normal new-deploy path).
func (s *ApplicationServiceBase) resolveModelComponent(ctx context.Context, plan *deployment.DeploymentPlan, comp *deploymenttypes.ComponentPlan) (*deploymenttypes.LiteLLMInfo, error) {
	if !comp.PreDeployed {
		return nil, nil
	}

	existing, err := s.ComponentRepo.GetByID(ctx, comp.DatabaseID)
	if err != nil {
		return nil, fmt.Errorf("DB lookup failed: %w", err)
	}
	if existing == nil {
		return nil, nil
	}

	// Copy endpoints from DB record into ComponentPlan so mergeComponentEndpoints works normally.
	for _, ep := range existing.Endpoints {
		epType, _ := ep["type"].(string)
		epURL, _ := ep["url"].(string)
		if epType == "" || epURL == "" {
			continue
		}
		// Translate the stored "service" endpoint to the host/port map that
		// mergeEndpointIntoService expects: {"host": "<dns>", "port": "<port>"}.
		if comp.Endpoints == nil {
			comp.Endpoints = make(map[string]any)
		}
		comp.Endpoints[comp.ComponentType] = map[string]any{
			"host": epURL,
			"port": "8080",
		}
		break // first endpoint is sufficient
	}

	// Extract model name from component metadata.
	modelName, _ := existing.Metadata["model"].(string)
	comp.ModelName = modelName

	routeID := buildAppRouteID(modelName, comp.ProviderID)

	// Check whether this component already has a LiteLLM key → was already registered.
	existingKey, err := s.KeyRepo.GetByDependency(ctx, models.DependencyTypeComponent, existing.ID)
	if err != nil {
		return nil, fmt.Errorf("key lookup failed for component %s: %w", existing.ID, err)
	}

	firstRegistration := existingKey == nil
	if firstRegistration {
		// No existing key → the route has not been registered with LiteLLM yet.
		// Extract the pod host from the stored service endpoint URL, then let
		// resolveAPIBase decide local vs remote Caddy path — same as a fresh deploy.
		podHost := ""
		for _, ep := range existing.Endpoints {
			epType, _ := ep["type"].(string)
			if epType == "service" {
				if epURL, _ := ep["url"].(string); epURL != "" {
					// ep["url"] is the pod DNS name (e.g. "llm-<slug>") or a full URL.
					// Strip any scheme/port so resolveAPIBase gets a bare hostname.
					if u, err := url.Parse(epURL); err == nil && u.Hostname() != "" {
						podHost = u.Hostname()
					} else {
						podHost = epURL
					}
				}
				break
			}
		}
		if podHost == "" {
			logger.WarningfCtx(ctx, "[app-deploy] component %s has no service endpoint; skipping LiteLLM route registration", existing.ID)
		} else {
			caddyRouteID := routeID + "--" + existing.ID.String()[:8]
			apiBase, resolveErr := s.resolveAPIBase(ctx, plan, podHost, existing.ID, caddyRouteID)
			if resolveErr != nil {
				logger.WarningfCtx(ctx, "[app-deploy] resolveAPIBase failed for pre-deployed component %s: %v", existing.ID, resolveErr)
			} else {
				logger.InfofCtx(ctx, "[app-deploy] registering LiteLLM route %q for pre-deployed component %s api_base=%s", routeID, existing.ID, apiBase)
				if regErr := litellmPostApp(ctx, "/model/new", map[string]any{
					"model_name": routeID,
					"litellm_params": map[string]any{
						"model":    "hosted_vllm/" + modelName,
						"api_base": apiBase,
					},
				}); regErr != nil {
					// Non-fatal: log but continue — the key can still be generated.
					logger.WarningfCtx(ctx, "[app-deploy] LiteLLM route registration failed (will retry on next deploy): %v", regErr)
				}
			}
		}
	}

	// Backfill model_id + external endpoint on the pre-deployed component row if absent.
	// This covers components created before these fields were introduced.
	s.backfillComponentEndpointMeta(ctx, existing, routeID)

	// Generate a fresh per-application virtual key scoped to this LiteLLM route.
	// The key name encodes the application ID so it is identifiable in LiteLLM's UI.
	appKeyName := routeID + "--app-" + comp.DatabaseID.String()[:8]
	virtualKey, err := generateAppVirtualKey(ctx, appKeyName, routeID)
	if err != nil {
		return nil, fmt.Errorf("virtual key generation failed for route %q: %w", routeID, err)
	}

	return &deploymenttypes.LiteLLMInfo{
		RouteID:           routeID,
		ModelName:         modelName,
		VirtualKey:        virtualKey,
		FirstRegistration: firstRegistration,
	}, nil
}

// injectLiteLLMIntoServices merges LiteLLM connection details into the Values of every
// service that depends on the given component.
//
// It sets two things in the service Values:
//
//  1. Values["litellm"] — the virtual key and route metadata so the
//     litellm-secret.yaml.tmpl can render the pod Secret.
//
//  2. Values[componentType] overrides for host/port/model/apiKey — so the
//     existing env vars in service templates (LLM_ENDPOINT, LLM_MODEL, etc.)
//     automatically point at LiteLLM instead of the direct vLLM pod:
//     • host  → LiteLLM host reachable from the pod's network
//     • port  → corresponding port
//     • model → LiteLLM route ID (e.g. "granite-3-3-8b--vllm-cpu")
//     • apiKey → non-empty sentinel so templates render the secret-mount branch
//
// Endpoint resolution depends on where the application pods run:
//   - Local worker: pods share the control-plane Podman network, so they reach
//     LiteLLM directly via the pod DNS name from LITELLM_URL
//     (e.g. "http://ai-services--litellm:4000").
//   - Remote worker: pods are on a different host; they must go through the
//     worker-side Caddy egress (LiteLLMEgressURL =
//     "http://ai-services--caddy:8080/litellm") which tunnels over mTLS back
//     to the control-plane Caddy and on to litellm:4000.
func (s *ApplicationServiceBase) injectLiteLLMIntoServices(
	ctx context.Context,
	plan *deployment.DeploymentPlan,
	comp *deploymenttypes.ComponentPlan,
	info *deploymenttypes.LiteLLMInfo,
) {
	// Choose the endpoint reachable by pods running on the target worker.
	var litellmEndpoint string
	if plan.WorkerName == workerconstants.LocalWorkerName {
		// Local worker: same Podman network as the LiteLLM pod — use direct URL.
		litellmEndpoint = litellmURLApp()
	} else {
		// Remote worker: pods must tunnel through the worker Caddy egress.
		litellmEndpoint = join.LiteLLMEgressURL
	}

	// Parse host, port and path prefix from the LiteLLM URL.
	litellmHost, litellmPort, litellmPath := parseLiteLLMURL(litellmEndpoint)

	for _, serviceID := range comp.UsedByServices {
		svc, ok := plan.Services[serviceID]
		if !ok {
			continue
		}
		if svc.Values == nil {
			svc.Values = make(map[string]any)
		}

		// 1. Set Values["litellm"] so the secret template renders.
		svc.Values["litellm"] = map[string]any{
			"key":      info.VirtualKey,
			"modelName": info.ModelName,
			"routeID":   info.RouteID,
			"endpoint":  litellmEndpoint,
		}

		// 2. Override the component-type values so existing env var references
		// (LLM_ENDPOINT, LLM_MODEL, EMB_ENDPOINT, EMB_MODEL, etc.) point at
		// LiteLLM. Merge into any existing map so other fields (maxModelLen, etc.)
		// are preserved.
		compValues, _ := svc.Values[comp.ComponentType].(map[string]any)
		if compValues == nil {
			compValues = make(map[string]any)
		}
		compValues["host"] = litellmHost
		compValues["port"] = litellmPort
		// prefixPath is "/litellm" for remote-worker deployments (empty for local).
		// Templates append it between the host:port and any API path so the worker
		// Caddy egress route matches correctly:
		//   local:  LLM_ENDPOINT = http://ai-services--litellm:4000
		//   remote: LLM_ENDPOINT = http://ai-services--caddy:8080/litellm
		compValues["prefixPath"] = litellmPath
		compValues["model"] = info.RouteID
		svc.Values[comp.ComponentType] = compValues

		logger.InfofCtx(ctx, "[app-deploy] injected LiteLLM info into service %s (route=%s endpoint=%s:%s%s)",
			serviceID, info.RouteID, litellmHost, litellmPort, litellmPath)
	}
}

// parseLiteLLMURL splits a LiteLLM base URL into host, port, and path prefix.
//
//	"http://ai-services--litellm:4000"       → ("ai-services--litellm", "4000", "")
//	"http://ai-services--caddy:8080/litellm" → ("ai-services--caddy",   "8080", "/litellm")
func parseLiteLLMURL(rawURL string) (host, port, path string) {
	s := rawURL
	// Strip scheme.
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	// Split off path prefix.
	if idx := strings.Index(s, "/"); idx >= 0 {
		path = s[idx:] // e.g. "/litellm"
		s = s[:idx]
	}
	// Split host:port.
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		return s[:idx], s[idx+1:], path
	}
	return s, "4000", path
}

// buildPostComponentHook returns a closure that the deployer calls after all component pods
// are running but before service pods are started.  It handles newly deployed managed-model
// components (PreDeployed=false) by:
//  1. Extracting the pod endpoint from comp.Endpoints (populated by the deployer).
//  2. Registering the LiteLLM route (POST /model/new).
//  3. Generating a virtual key and persisting it in the DB keys table.
//  4. Generating a separate per-application virtual key and injecting it into
//     every dependent service's Values under "litellm" so the Secret template
//     renders it correctly.
//
// The hook is a no-op when KeyRepo is nil.
func (s *ApplicationServiceBase) buildPostComponentHook(plan *deployment.DeploymentPlan) func(context.Context) error {
	return func(ctx context.Context) error {
		if s.KeyRepo == nil {
			return nil
		}

		for _, comp := range plan.Components {
			if !managedModelTypes[comp.ComponentType] || comp.PreDeployed {
				// Either not a managed-model type, or already handled in the pre-deploy path.
				continue
			}

			if err := s.handleNewlyDeployedModel(ctx, plan, comp); err != nil {
				// Log but do not abort — the pod is already running; LiteLLM reg is best-effort.
				logger.ErrorfCtx(ctx, "[app-deploy] post-component hook failed for %s/%s: %v", comp.ComponentType, comp.ProviderID, err)
			}
		}

		return nil
	}
}

// resolveAPIBase determines the correct LiteLLM api_base for a newly deployed model
// component, mirroring the logic in model_service.deployAsync.
//
// For a local worker, LiteLLM reaches the vLLM pod directly via pod DNS on plain HTTP.
// For a remote worker, two Caddy routes are registered (mTLS ingress on the worker,
// mTLS egress on the CP) and the api_base points at the CP Caddy egress :8080.
//
// podHost is the pod DNS name / IP returned by the deployer (e.g. "llm-<slug>").
// componentID is used to build a unique Caddy route ID per deployment.
func (s *ApplicationServiceBase) resolveAPIBase(ctx context.Context, plan *deployment.DeploymentPlan, podHost string, componentID uuid.UUID, caddyRouteID string) (string, error) {
	if plan.WorkerName == workerconstants.LocalWorkerName {
		// Local: LiteLLM → vLLM pod directly.
		return fmt.Sprintf("http://%s:8000/v1", podHost), nil
	}

	// Remote worker — needs mTLS Caddy routes exactly as model_service does.
	if s.WorkerRegistry == nil {
		return "", fmt.Errorf("WorkerRegistry not configured; cannot build remote api_base for worker %q", plan.WorkerName)
	}

	workerMeta, _ := s.WorkerRegistry.WorkerMetadata(plan.WorkerName)
	workerDomainSuffix := workerMeta[workerconstants.MetaKeyDomainSuffix]
	if workerDomainSuffix == "" {
		return "", fmt.Errorf("worker %q did not send %s at registration — cannot build mTLS routes",
			plan.WorkerName, workerconstants.MetaKeyDomainSuffix)
	}

	// Build a runtime so we can get the RemoteRuntime sender for the worker Caddy Admin API.
	rt, err := runtime.NewRuntimeFactory(runtimeTypes.RuntimeType(plan.RuntimeType)).
		CreateRemote(plan.WorkerName, s.WorkerRegistry, "")
	if err != nil {
		return "", fmt.Errorf("failed to build remote runtime for worker %q: %w", plan.WorkerName, err)
	}
	remoteRT, ok := rt.(*remoteruntime.RemoteRuntime)
	if !ok {
		return "", fmt.Errorf("expected RemoteRuntime for worker %q, got %T", plan.WorkerName, rt)
	}

	ingressPathPrefix := fmt.Sprintf("/worker/%s/models/%s", plan.WorkerName, caddyRouteID)

	// 1. Worker Caddy :8443 — mTLS ingress (path-based, strips prefix → vLLM :8000).
	remotePM := proxy.NewRemoteProxyManager(remoteRT.Sender)
	mtlsRoute := proxy.Route{
		ID:         caddyRouteID + "--mtls",
		PathPrefix: ingressPathPrefix,
		Upstream:   podHost + ":8000",
		Terminal:   true,
	}
	logger.InfofCtx(ctx, "[app-deploy] registering mTLS ingress route %q on worker Caddy (prefix %s)", mtlsRoute.ID, ingressPathPrefix)
	if err := remotePM.RegisterMTLSPathRoute(ctx, mtlsRoute); err != nil {
		return "", fmt.Errorf("worker mTLS ingress route registration failed: %w", err)
	}

	// 2. CP Caddy :8080 — mTLS egress → worker Caddy :8443.
	localPM, err := proxy.GetCaddyProxyManager()
	if err != nil {
		return "", fmt.Errorf("CP Caddy proxy manager unavailable: %w", err)
	}
	workerDialAddr := fmt.Sprintf("%s.%s:%s", plan.WorkerName, workerDomainSuffix, proxy.DefaultMTLSPort)
	egressRoute := proxy.EgressRoute{
		ID:                caddyRouteID + "--egress",
		PathPrefix:        ingressPathPrefix,
		DialUpstream:      workerDialAddr,
		ClientCertPath:    workerconstants.GatewayPKIDir + "/server.crt",
		ClientKeyPath:     workerconstants.GatewayPKIDir + "/" + gatewaypkg.ServerKeyPlaintextFile,
		TrustedCACertPath: workerconstants.GatewayPKIDir + "/ca.crt",
	}
	logger.InfofCtx(ctx, "[app-deploy] registering mTLS egress route %q on CP Caddy → %s", egressRoute.ID, workerDialAddr)
	if err := localPM.RegisterEgressRoute(ctx, egressRoute); err != nil {
		return "", fmt.Errorf("CP Caddy egress route registration failed: %w", err)
	}

	// LiteLLM api_base: plain HTTP to local CP Caddy egress :8080.
	caddyAdminURL := utils.GetEnv(proxy.CaddyAdminURLEnvVar, "")
	caddyHost := "ai-services--caddy"
	if caddyAdminURL != "" {
		if u, err := url.Parse(caddyAdminURL); err == nil && u.Hostname() != "" {
			caddyHost = u.Hostname()
		}
	}
	return fmt.Sprintf("http://%s:%s%s/v1", caddyHost, proxy.DefaultEgressPort, ingressPathPrefix), nil
}

// handleNewlyDeployedModel performs LiteLLM registration, DB key persistence, and per-app
// virtual key injection for a freshly deployed managed-model component.
func (s *ApplicationServiceBase) handleNewlyDeployedModel(ctx context.Context, plan *deployment.DeploymentPlan, comp *deploymenttypes.ComponentPlan) error {
	// Extract model name from the component params (set by the planner from the request).
	modelName, _ := comp.Params["model"].(string)
	if modelName == "" {
		// Fall back to Values (loaded from catalog values.yaml + overrides).
		modelName, _ = comp.Values["model"].(string)
	}

	routeID := buildAppRouteID(modelName, comp.ProviderID)
	caddyRouteID := routeID + "--" + comp.DatabaseID.String()[:8]

	// Derive pod host from the endpoint the deployer populated.
	podHost := ""
	if ep, ok := comp.Endpoints[comp.ComponentType]; ok {
		if epMap, ok := ep.(map[string]any); ok {
			podHost, _ = epMap["host"].(string)
		}
	}
	if podHost == "" {
		return fmt.Errorf("no endpoint found for component %s after deployment; skipping LiteLLM registration", comp.ComponentType)
	}

	// Resolve the correct api_base (local pod DNS or CP Caddy egress for remote workers).
	apiBase, err := s.resolveAPIBase(ctx, plan, podHost, comp.DatabaseID, caddyRouteID)
	if err != nil {
		return fmt.Errorf("failed to resolve api_base for component %s: %w", comp.DatabaseID, err)
	}

	// 1. Register the LiteLLM route.
	logger.InfofCtx(ctx, "[app-deploy] registering LiteLLM route %q for new component %s api_base=%s", routeID, comp.DatabaseID, apiBase)
	if err := litellmPostApp(ctx, "/model/new", map[string]any{
		"model_name": routeID,
		"litellm_params": map[string]any{
			"model":    "hosted_vllm/" + modelName,
			"api_base": apiBase,
		},
	}); err != nil {
		return fmt.Errorf("LiteLLM route registration failed: %w", err)
	}

	// 2. Generate and persist the per-model virtual key in the DB (same as model_service).
	// Guard against duplicate inserts: if a key row already exists for this component
	// (e.g. a retry after a partial failure) reuse the stored key rather than minting
	// another one in LiteLLM and leaving the old one orphaned.
	existingKey, err := s.KeyRepo.GetByDependency(ctx, models.DependencyTypeComponent, comp.DatabaseID)
	if err != nil {
		return fmt.Errorf("key lookup for component %s failed: %w", comp.DatabaseID, err)
	}
	if existingKey == nil {
		modelVirtualKey, err := generateAppVirtualKey(ctx, routeID, routeID)
		if err != nil {
			return fmt.Errorf("model virtual key generation failed: %w", err)
		}
		if err := s.KeyRepo.Insert(ctx, &models.Key{
			DependencyID:   comp.DatabaseID,
			DependencyType: models.DependencyTypeComponent,
			VirtualKey:     modelVirtualKey,
			RouteID:        routeID,
		}); err != nil {
			return fmt.Errorf("failed to persist model virtual key: %w", err)
		}
		logger.InfofCtx(ctx, "[app-deploy] model key persisted for component %s route=%s", comp.DatabaseID, routeID)
	} else {
		logger.InfofCtx(ctx, "[app-deploy] model key already exists for component %s route=%s; skipping insert", comp.DatabaseID, routeID)
	}

	// Persist model_id + external endpoint on the freshly deployed component row.
	freshComp, fetchErr := s.ComponentRepo.GetByID(ctx, comp.DatabaseID)
	if fetchErr == nil && freshComp != nil {
		s.backfillComponentEndpointMeta(ctx, freshComp, routeID)
	}

	// 3. Generate a fresh per-application virtual key and inject into service Values.
	appKeyName := routeID + "--app-" + plan.ApplicationID.String()[:8]
	appVirtualKey, err := generateAppVirtualKey(ctx, appKeyName, routeID)
	if err != nil {
		return fmt.Errorf("app virtual key generation failed: %w", err)
	}

	litellmInfo := &deploymenttypes.LiteLLMInfo{
		RouteID:           routeID,
		ModelName:         modelName,
		VirtualKey:        appVirtualKey,
		FirstRegistration: true,
	}
	s.injectLiteLLMIntoServices(ctx, plan, comp, litellmInfo)

	logger.InfofCtx(ctx, "[app-deploy] new component %s/%s: LiteLLM route=%s key persisted, app key injected",
		comp.ComponentType, comp.ProviderID, routeID)

	return nil
}

// attachConnectorsPostDeploy calls ConnectDatasourcesToApplication with all unique connector
// UUIDs found in the create request's service connector refs. It is a best-effort operation:
// failures are logged but do not affect the application's Running status. No-op when
// DatasourceService is nil or when no service in the request carries connector refs.
func (s *ApplicationServiceBase) attachConnectorsPostDeploy(ctx context.Context, applicationID uuid.UUID, services []apimodels.Service) {
	if s.DatasourceService == nil {
		logger.WarningfCtx(ctx, "DatasourceService is nil; skipping post-deploy connector attachment for application %s", applicationID)

		return
	}

	datasourceIDs := collectDatasourceIDs(ctx, services)
	if len(datasourceIDs) == 0 {
		logger.WarningfCtx(ctx, "No datasource IDs collected; skipping post-deploy connector attachment for application %s", applicationID)

		return
	}

	resp, err := s.DatasourceService.ConnectDatasourcesToApplication(ctx, applicationID, datasourceIDs)
	if err != nil {
		logger.ErrorfCtx(ctx, "post-deploy connector attachment failed for application %s: %v", applicationID, err)

		return
	}

	logConnectErrors(ctx, applicationID, resp)
}

// collectDatasourceIDs returns a deduplicated slice of connector UUIDs from all service
// connector refs in the create request. The same connector ID may appear across multiple
// services; ConnectDatasourcesToApplication resolves eligible services internally, so each
// unique ID needs to be passed only once.
func collectDatasourceIDs(ctx context.Context, services []apimodels.Service) []uuid.UUID {
	seen := make(map[uuid.UUID]bool)
	var ids []uuid.UUID

	for _, svc := range services {
		for _, ref := range svc.Connectors {
			id, err := uuid.Parse(ref.ID)
			if err != nil {
				// Should not happen — ValidateConnectorRefs already validated UUIDs.
				logger.WarningfCtx(ctx, "skipping unparseable connector ref ID %q: %v", ref.ID, err)

				continue
			}

			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}

	return ids
}

// logConnectErrors logs any per-datasource failures returned by ConnectDatasourcesToApplication.
func logConnectErrors(ctx context.Context, applicationID uuid.UUID, resp *apimodels.ConnectDatasourcesResponse) {
	if resp == nil || len(resp.Errors) == 0 {
		return
	}

	for _, connErr := range resp.Errors {
		logger.ErrorfCtx(ctx, "post-deploy connector %s failed for application %s: %s",
			connErr.DatasourceID, applicationID, connErr.Error)
	}
}

// GetApplicationResources retrieves CPU, memory, and Spyre-card usage for an application.
func (s *ApplicationServiceBase) GetApplicationResources(ctx context.Context, id uuid.UUID) (*types.ApplicationResourcesResponse, error) {
	app, err := s.AppRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get application: %w", err)
	}
	if app == nil {
		return nil, &ValidationError{
			Code:    http.StatusNotFound,
			Message: ErrMsgApplicationNotFound,
		}
	}

	runtimeClient, err := s.createRuntime(app)
	if err != nil {
		return nil, fmt.Errorf("failed to create runtime client: %w", err)
	}

	resourceTotals, err := s.collectResources(ctx, app, runtimeClient, s.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to collect application resources: %w", err)
	}

	return buildResourcesResponse(resourceTotals), nil
}

func (s *ApplicationServiceBase) collectResources(
	ctx context.Context,
	app *models.Application,
	runtimeClient runtime.Runtime,
	catalogProvider *catalog.CatalogProvider,
) (*resourceTotals, error) {
	totals := &resourceTotals{spyreCards: make(map[string]bool)}
	countedComponents := make(map[uuid.UUID]bool)

	for _, service := range app.Services {
		if err := s.processServiceResources(ctx, service, runtimeClient, catalogProvider, totals, countedComponents); err != nil {
			return nil, fmt.Errorf("failed to process service %s resources: %w", service.ID, err)
		}
	}

	return totals, nil
}

func (s *ApplicationServiceBase) processServiceResources(
	ctx context.Context,
	service models.Service,
	runtimeClient runtime.Runtime,
	catalogProvider *catalog.CatalogProvider,
	totals *resourceTotals,
	countedComponents map[uuid.UUID]bool,
) error {
	if err := s.addServiceResources(ctx, service, catalogProvider, runtimeClient, totals); err != nil {
		return fmt.Errorf("failed to get service allocated resources: %w", err)
	}

	if err := s.addComponentResources(ctx, service.ID, catalogProvider, runtimeClient, totals, countedComponents); err != nil {
		return fmt.Errorf("failed to get component allocated resources: %w", err)
	}

	return nil
}

func addAllocatedResources(runtimeMetadata *clitemplates.AppMetadata, totals *resourceTotals) {
	if runtimeMetadata.Resources != nil {
		totals.allocatedCPU += runtimeMetadata.Resources.CPU
		totals.allocatedMemory += runtimeMetadata.Resources.Memory
	}
}

func (s *ApplicationServiceBase) addServiceResources(
	ctx context.Context,
	service models.Service,
	catalogProvider *catalog.CatalogProvider,
	runtimeClient runtime.Runtime,
	totals *resourceTotals,
) error {
	scopedProvider, err := catalogProvider.WithRuntime(runtimeClient.Type().String())
	if err != nil {
		return fmt.Errorf("failed to scope catalog provider for runtime %q: %w", runtimeClient.Type().String(), err)
	}

	runtimeMetadata, err := scopedProvider.LoadServiceRuntimeMetadata(service.CatalogID)
	if err != nil {
		return fmt.Errorf("failed to load service runtime metadata for catalog ID %s: %w", service.CatalogID, err)
	}

	addAllocatedResources(runtimeMetadata, totals)

	if err := addUsedResourcesByTemplateID(ctx, service.ID.String(), runtimeClient, totals); err != nil {
		return fmt.Errorf("failed to get service used resources: %w", err)
	}

	return nil
}

func (s *ApplicationServiceBase) addComponentResources(
	ctx context.Context,
	serviceID uuid.UUID,
	catalogProvider *catalog.CatalogProvider,
	runtimeClient runtime.Runtime,
	totals *resourceTotals,
	countedComponents map[uuid.UUID]bool,
) error {
	dependencies, err := s.ServiceDependencyRepo.GetDependenciesByServiceID(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("failed to get dependencies for service %s: %w", serviceID, err)
	}

	for _, dep := range dependencies {
		if dep.DependencyType != models.DependencyTypeComponent || countedComponents[dep.DependencyID] {
			continue
		}

		if err := s.processComponentResources(ctx, dep.DependencyID, catalogProvider, runtimeClient, totals); err != nil {
			return err
		}

		countedComponents[dep.DependencyID] = true
	}

	return nil
}

func (s *ApplicationServiceBase) processComponentResources(
	ctx context.Context,
	componentID uuid.UUID,
	catalogProvider *catalog.CatalogProvider,
	runtimeClient runtime.Runtime,
	totals *resourceTotals,
) error {
	component, err := s.ComponentRepo.GetByID(ctx, componentID)
	if err != nil {
		return fmt.Errorf("failed to get component %s: %w", componentID, err)
	}

	scopedProvider, err := catalogProvider.WithRuntime(runtimeClient.Type().String())
	if err != nil {
		return fmt.Errorf("failed to scope catalog provider for runtime %q: %w", runtimeClient.Type().String(), err)
	}

	runtimeMetadata, err := scopedProvider.LoadComponentRuntimeMetadata(component.Type, component.Provider)
	if err != nil {
		return fmt.Errorf("failed to load runtime metadata for component %s/%s: %w", component.Type, component.Provider, err)
	}

	addAllocatedResources(runtimeMetadata, totals)

	if err := addUsedResourcesByTemplateID(ctx, component.ID.String(), runtimeClient, totals); err != nil {
		return fmt.Errorf("failed to get component used resources for %s: %w", component.ID, err)
	}

	return nil
}

func addUsedResourcesByTemplateID(ctx context.Context, templateID string, runtimeClient runtime.Runtime, totals *resourceTotals) error {
	filters := map[string][]string{
		"label": {fmt.Sprintf("%s=%s", consts.ApplicationTemplateKey, templateID)},
	}

	pods, err := runtimeClient.ListPods(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to list pods for template %s: %w", templateID, err)
	}

	for _, pod := range pods {
		if err := collectPodResources(ctx, pod.Name, runtimeClient, totals); err != nil {
			return fmt.Errorf("failed to get used resources for pod %s: %w", pod.Name, err)
		}
	}

	return nil
}

func collectPodResources(ctx context.Context, podName string, runtimeClient runtime.Runtime, totals *resourceTotals) error {
	resources, err := runtimeClient.GetPodResources(ctx, podName)
	if err != nil {
		return fmt.Errorf("failed to get resources for pod %s: %w", podName, err)
	}

	for _, card := range resources.SpyreCards {
		totals.spyreCards[card] = true
	}

	totals.usedCPU += resources.CPU
	totals.usedMemory += resources.MemUsage

	return nil
}

func buildResourcesResponse(totals *resourceTotals) *types.ApplicationResourcesResponse {
	totalSpyreCards := make([]string, 0, len(totals.spyreCards))
	for card := range totals.spyreCards {
		totalSpyreCards = append(totalSpyreCards, card)
	}

	accelerators := make(map[string][]string)
	if len(totalSpyreCards) > 0 {
		accelerators[consts.SpyreResourceName] = totalSpyreCards
	}

	return &types.ApplicationResourcesResponse{
		CPU: types.ApplicationCPUInfo{
			Total: float64(totals.allocatedCPU),
			Used:  math.Round(totals.usedCPU*consts.PercentageDivisor) / consts.PercentageDivisor,
		},
		Memory: types.ApplicationMemInfo{
			TotalBytes: int64(totals.allocatedMemory),
			UsedBytes:  int64(totals.usedMemory),
		},
		Accelerators: accelerators,
	}
}

// ApplicationsPs returns runtime pod/container status for an application by querying the configured runtime.
func (s *ApplicationServiceBase) ApplicationsPs(ctx context.Context, appID uuid.UUID) (*types.ApplicationPSResponse, error) {
	app, err := s.AppRepo.GetByID(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("failed to get application: %w", err)
	}
	if app == nil {
		return nil, &ValidationError{
			Code:    http.StatusNotFound,
			Message: ErrMsgApplicationNotFound,
		}
	}

	logger.InfofCtx(ctx, "Starting application ps for application ID: %s, name: %s", appID.String(), app.Name)

	rt, err := s.createRuntime(app)
	if err != nil {
		return nil, fmt.Errorf("failed to init runtime client: %w", err)
	}

	servicePods := s.collectServicePods(ctx, rt, app.Services)

	componentPods, err := s.collectComponentPods(ctx, rt, app.Services)
	if err != nil {
		return nil, fmt.Errorf("failed to collect component pods: %w", err)
	}

	if app.WorkerID == nil {
		return nil, fmt.Errorf("application %s has no worker_id", app.ID)
	}

	workerInfo, err := s.buildWorkerInfo(ctx, *app.WorkerID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve worker for application %s: %w", app.ID, err)
	}

	psResp := &types.ApplicationPSResponse{
		ID:          app.ID.String(),
		Name:        app.Name,
		RuntimeType: workerInfo.RuntimeType,
		WorkerName:  workerInfo.Name,
		Services:    servicePods,
		Components:  componentPods,
	}

	// Namespace is only meaningful for OpenShift workers
	if runtimeTypes.RuntimeType(workerInfo.RuntimeType) == runtimeTypes.RuntimeTypeOpenShift {
		psResp.Namespace = catalogutils.AppNamespace(app.ID)
	}

	return psResp, nil
}

func (s *ApplicationServiceBase) collectServicePods(
	ctx context.Context,
	rt runtime.Runtime,
	services []models.Service,
) []types.Pod {
	servicePods := make([]types.Pod, 0, len(services))

	for _, service := range services {
		pod, err := loadApplicationPods(ctx, rt, service.ID.String())
		if err != nil {
			logger.ErrorfCtx(ctx, "failed to load service pod for service %s: %v", service.ID, err)

			continue
		}
		servicePods = append(servicePods, pod...)
	}

	logger.InfofCtx(ctx, "Successfully collected %d service pods", len(servicePods))

	return servicePods
}

func (s *ApplicationServiceBase) collectComponentPods(
	ctx context.Context,
	rt runtime.Runtime,
	services []models.Service,
) ([]types.Pod, error) {
	componentMap := make(map[string][]types.Pod)

	for _, service := range services {
		serviceDependencies, err := s.ServiceDependencyRepo.GetDependenciesByServiceID(ctx, service.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get dependencies for service %s: %w", service.ID, err)
		}

		for _, dependency := range serviceDependencies {
			if dependency.DependencyType != models.DependencyTypeComponent {
				continue
			}

			componentID := dependency.DependencyID.String()

			if _, exists := componentMap[componentID]; exists {
				continue
			}

			componentPod, err := loadApplicationPods(ctx, rt, componentID)
			if err != nil {
				logger.ErrorfCtx(ctx, "failed to load component pod %s: %v", componentID, err)

				continue
			}

			componentMap[componentID] = componentPod
		}
	}

	componentPods := make([]types.Pod, 0, len(componentMap))
	for _, podDetails := range componentMap {
		componentPods = append(componentPods, podDetails...)
	}

	logger.InfofCtx(ctx, "Successfully collected %d unique component pods", len(componentPods))

	return componentPods, nil
}

func loadApplicationPods(ctx context.Context, rt runtime.Runtime, appID string) ([]types.Pod, error) {
	filteredPod, err := common.FetchFilteredPods(ctx, rt, appID)
	if err != nil {
		return nil, err
	}
	if len(filteredPod) == 0 {
		return nil, fmt.Errorf("no pod found with given id")
	}

	appPodList := make([]types.Pod, 0, len(filteredPod))

	for _, pod := range filteredPod {
		processedPod, err := common.ProcessPod(ctx, rt, pod)
		if err != nil {
			return nil, fmt.Errorf("failed to process pod: %w", err)
		}
		// ProcessPod returns (nil, nil) when InspectPod fails, so we should skip that pod
		if processedPod == nil {
			continue
		}

		containers := make([]types.PodContainer, 0, len(pod.Containers))
		for _, container := range processedPod.Containers {
			containers = append(containers, types.PodContainer{
				Name:    container.Name,
				Status:  types.Status(strings.ToLower(processedPod.Status)),
				Healthy: strings.ToLower(container.Health) == string(consts.Ready),
			})
		}

		appPod := types.Pod{
			PodID:      processedPod.ID,
			PodName:    processedPod.Name,
			Status:     types.Status(strings.ToLower(processedPod.Status)),
			Healthy:    processedPod.Health == string(consts.Ready),
			Created:    pod.Created.Format(constants.RFC3339WithTimezone),
			Labels:     pod.Labels,
			Containers: containers,
		}

		appPodList = append(appPodList, appPod)
	}

	return appPodList, nil
}

// validateForDeletion fetches the application and validates that the requesting
// user owns it and it is not already being deleted. Returns the app model on success.
func (s *ApplicationServiceBase) validateForDeletion(ctx context.Context, id uuid.UUID, user string) (*models.Application, error) {
	app, err := s.AppRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get application: %w", err)
	}
	if app == nil {
		return nil, &ValidationError{Code: http.StatusNotFound, Message: ErrMsgApplicationNotFound}
	}
	if app.CreatedBy != user {
		return nil, &ValidationError{Code: http.StatusForbidden, Message: ErrMsgUserNotOwner}
	}
	if app.Status == models.ApplicationStatusDeleting {
		return nil, &ValidationError{Code: http.StatusConflict, Message: ErrMsgApplicationAlreadyDeleting}
	}

	return app, nil
}

func (s *ApplicationServiceBase) DeleteApplication(ctx context.Context, id uuid.UUID, user string, keepData bool) (*DeleteApplicationResponse, error) {
	app, err := s.validateForDeletion(ctx, id, user)
	if err != nil {
		return nil, err
	}

	// Resolve the runtime before cancelling or launching the goroutine so that
	// worker connectivity errors surface synchronously to the HTTP caller.
	rt, err := s.createRuntime(app)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve runtime for deletion: %w", err)
	}

	// Cancel any in-flight deployment before transitioning to Deleting.
	// No-op when DeploymentRegistry is nil (e.g. OpenShift stub).
	if s.DeploymentRegistry != nil {
		s.DeploymentRegistry.Cancel(id)
	}

	if err := catalogutils.UpdateApplicationStatus(ctx, s.AppRepo, id, models.ApplicationStatusDeleting, "Deleting deployment..."); err != nil {
		return nil, err
	}

	orphanedComponentIDs, err := s.identifyOrphanedComponents(ctx, id, app.Services)
	if err != nil {
		return nil, fmt.Errorf("failed to get application components: %w", err)
	}

	var requestID string
	if reqID, ok := ctx.Value(logger.RequestIDKey).(string); ok {
		requestID = reqID
	}

	deletionCtx := context.Background()
	if requestID != "" {
		deletionCtx = context.WithValue(deletionCtx, logger.RequestIDKey, requestID)
	}

	go s.executeDeletionAsync(deletionCtx, id, app.Services, orphanedComponentIDs, keepData, rt)

	return &DeleteApplicationResponse{
		ID:      id.String(),
		Status:  string(models.ApplicationStatusDeleting),
		Message: "Deletion initiated successfully",
	}, nil
}

func (s *ApplicationServiceBase) executeDeletionAsync(
	parentCtx context.Context,
	appID uuid.UUID,
	services []models.Service,
	orphanedComponentIDs []uuid.UUID,
	keepData bool,
	rt runtime.Runtime,
) {
	var requestID string
	if id, ok := parentCtx.Value(logger.RequestIDKey).(string); ok {
		requestID = id
	}

	ctx := context.Background()
	if requestID != "" {
		ctx = context.WithValue(ctx, logger.RequestIDKey, requestID)
	}
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorfCtx(ctx, "Panic recovered in deletion goroutine for application %s: %v", appID, r)

			errMsg := fmt.Sprintf("Deletion panic: %v", r)
			if updateErr := catalogutils.UpdateApplicationStatus(ctx, s.AppRepo, appID.String(), models.ApplicationStatusError, errMsg); updateErr != nil {
				logger.ErrorfCtx(ctx, "Failed to update application status after panic: %v", updateErr)
			}
		}
	}()

	err := s.DeletionExecutor.Execute(ctx, appID, services, orphanedComponentIDs, keepData, rt)
	if err != nil {
		logger.ErrorfCtx(ctx, "Deletion failed for application %s: %v", appID.String(), err)

		if updateErr := catalogutils.UpdateApplicationStatus(ctx, s.AppRepo, appID.String(), models.ApplicationStatusError, err.Error()); updateErr != nil {
			logger.ErrorfCtx(ctx, "Failed to update application status to Error: %v", updateErr)
		}

		return
	}

	logger.InfolnCtx(ctx, fmt.Sprintf("Deletion completed successfully for application id '%s'", appID.String()))
}

// identifyOrphanedComponents identifies components that will become orphaned after service deletion.
func (s *ApplicationServiceBase) identifyOrphanedComponents(ctx context.Context, appID uuid.UUID, services []models.Service) ([]uuid.UUID, error) {
	serviceIDs := s.buildServiceIDMap(services)

	componentCandidates, err := s.collectComponentCandidates(ctx, appID, services)
	if err != nil {
		return nil, err
	}

	return s.filterOrphanedComponents(ctx, componentCandidates, serviceIDs), nil
}

// buildServiceIDMap creates a map of service IDs for quick lookup.
func (s *ApplicationServiceBase) buildServiceIDMap(services []models.Service) map[uuid.UUID]bool {
	serviceIDs := make(map[uuid.UUID]bool, len(services))
	for _, svc := range services {
		serviceIDs[svc.ID] = true
	}

	return serviceIDs
}

func (s *ApplicationServiceBase) collectComponentCandidates(ctx context.Context, appID uuid.UUID, services []models.Service) (map[uuid.UUID]bool, error) {
	componentCandidates := make(map[uuid.UUID]bool)

	for _, svc := range services {
		deps, err := s.ServiceDependencyRepo.GetDependenciesByServiceID(ctx, svc.ID)
		if err != nil {
			logger.ErrorfCtx(ctx, "failed to get dependencies for service %s: %s", svc.ID, err)
			_ = catalogutils.UpdateApplicationStatus(ctx, s.AppRepo, appID, models.ApplicationStatusError, "failed to get service dependencies")

			return nil, err
		}

		for _, dep := range deps {
			if dep.DependencyType == models.DependencyTypeComponent {
				componentCandidates[dep.DependencyID] = true
			}
		}
	}

	return componentCandidates, nil
}

// filterOrphanedComponents checks which components are truly orphaned.
func (s *ApplicationServiceBase) filterOrphanedComponents(ctx context.Context, componentCandidates map[uuid.UUID]bool, serviceIDs map[uuid.UUID]bool) []uuid.UUID {
	var orphanedComponents []uuid.UUID

	for componentID := range componentCandidates {
		if s.isComponentOrphaned(ctx, componentID, serviceIDs) {
			orphanedComponents = append(orphanedComponents, componentID)
		}
	}

	return orphanedComponents
}

// isComponentOrphaned checks if a component has no remaining dependent services.
func (s *ApplicationServiceBase) isComponentOrphaned(ctx context.Context, componentID uuid.UUID, serviceIDs map[uuid.UUID]bool) bool {
	dependentServices, err := s.ServiceDependencyRepo.GetServicesByDependency(ctx, componentID, models.DependencyTypeComponent)
	if err != nil {
		logger.ErrorfCtx(ctx, "failed to check component %s orphan status: %s", componentID, err)

		return false
	}

	for _, svcID := range dependentServices {
		if !serviceIDs[svcID] {
			return false
		}
	}

	return true
}


// backfillComponentEndpointMeta writes model_id into the component's metadata and appends
// an "external" LiteLLM endpoint to its endpoints list — both only when absent, so the
// function is safe to call on new and pre-existing rows alike.
func (s *ApplicationServiceBase) backfillComponentEndpointMeta(ctx context.Context, c *models.Component, routeID string) {
	updated := false

	// 1. Write model_id into metadata if not already set.
	if _, ok := c.Metadata["model_id"]; !ok {
		if c.Metadata == nil {
			c.Metadata = make(map[string]any)
		}
		c.Metadata["model_id"] = routeID
		if err := s.ComponentRepo.Update(ctx, c); err != nil {
			logger.WarningfCtx(ctx, "[app-deploy] component %s: failed to write model_id to metadata: %v", c.ID, err)
		} else {
			updated = true
		}
	}

	// 2. Append "external" endpoint if not already present.
	hasExternal := false
	for _, ep := range c.Endpoints {
		if t, _ := ep["type"].(string); t == "external" {
			hasExternal = true
			break
		}
	}
	if !hasExternal {
		endpoints := append(c.Endpoints, map[string]any{
			"type": "external",
			"url":  litellmURLApp(),
		})
		if err := s.ComponentRepo.UpdateEndpoints(ctx, c.ID, endpoints); err != nil {
			logger.WarningfCtx(ctx, "[app-deploy] component %s: failed to append external endpoint: %v", c.ID, err)
		} else {
			updated = true
		}
	}

	if updated {
		logger.InfofCtx(ctx, "[app-deploy] component %s: backfilled model_id=%q and external endpoint", c.ID, routeID)
	}
}


// Made with Bob
