package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"

	"github.com/google/uuid"
	apimodels "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/models"
	dbmodels "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/models"
	dbrepo "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/repository"
	catalogutils "github.com/project-ai-services/ai-services/internal/pkg/catalog/utils"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/validators"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	pkgutils "github.com/project-ai-services/ai-services/internal/pkg/utils"
)

// connectorNameRe matches the datasource connector name rule: letters, digits, hyphens, underscores.
var connectorNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// createRemote registers a remote model endpoint as a connector. Flow:
//
//  1. Validate type, name, provider existence and params against the provider's schema.json.
//  2. Duplicate-name guard (case-insensitive — handled by LOWER() in the DB query).
//  3. Register the LiteLLM route (sensitive params passed straight to LiteLLM).
//  4. Probe the route via GET /health?model=<route_id>; on failure delete the route and return 422.
//  5. Generate a per-model LiteLLM virtual key for the route.
//  6. Persist the connector with sensitive fields stripped from metadata.
//  7. Persist the virtual key in keys (dependency_type = 'connector').
func (s *ModelService) createRemote(ctx context.Context, req apimodels.CreateModelRequest) (*apimodels.CreateModelResponse, error) {
	// Phase 1: validate request.
	if !validModelTypes[req.Type] {
		return nil, &ValidationError{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("unknown type %q: must be one of llm, embedding, reranker", req.Type),
		}
	}

	if !connectorNameRe.MatchString(req.Name) {
		return nil, &ValidationError{
			Code:    http.StatusBadRequest,
			Message: "Model name may only contain letters, digits, hyphens (-), and underscores (_)",
		}
	}

	if !s.catalogProvider.ConnectorExists(req.Type, req.ProviderID) {
		return nil, &ValidationError{
			Code:    http.StatusNotFound,
			Message: fmt.Sprintf("Connector provider %q not found in catalog for type %q", req.ProviderID, req.Type),
		}
	}

	rawSchema, err := s.catalogProvider.GetConnectorProviderParams(ctx, req.Type, req.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("failed to load param schema for provider %q: %w", req.ProviderID, err)
	}

	schema, err := pkgutils.ConvertRawJsontoMap(rawSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to decode param schema for provider %q: %w", req.ProviderID, err)
	}

	if len(req.Params) == 0 {
		return nil, &ValidationError{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("Params is required for connector provider %q", req.ProviderID),
		}
	}

	if err := validators.ValidateParams(req.Params, schema, fmt.Sprintf("connector provider %q", req.ProviderID)); err != nil {
		return nil, err
	}

	// Phase 2: duplicate-name guard.
	existing, err := s.connectorRepo.GetByName(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing connector: %w", err)
	}
	if existing != nil {
		return nil, &ValidationError{
			Code:    http.StatusConflict,
			Message: fmt.Sprintf("Model with name %q already exists", req.Name),
		}
	}

	// Phase 3: register the LiteLLM route.
	modelName, _ := req.Params["model_name"].(string)
	routeID := buildRouteID(path.Base(modelName), req.ProviderID)

	if err := s.registerConnectorRoute(ctx, routeID, req.ProviderID, req.Params); err != nil {
		return nil, fmt.Errorf("LiteLLM route registration failed: %w", err)
	}

	// Phase 4: probe the route; roll back on failure.
	if probeErr := s.probeLiteLLMRoute(ctx, routeID); probeErr != nil {
		if delErr := s.deleteLiteLLMRoute(ctx, routeID); delErr != nil {
			logger.WarningfCtx(ctx, "failed to delete LiteLLM route %q after failed probe: %v", routeID, delErr)
		}

		return nil, &ValidationError{
			Code:    http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("Connection test failed: %v", probeErr),
		}
	}

	// Phase 5: generate the per-model virtual key scoped to this route.
	virtualKey, err := s.generateVirtualKey(ctx, routeID)
	if err != nil {
		s.rollbackConnectorRoute(ctx, routeID, "")

		return nil, fmt.Errorf("virtual key generation failed: %w", err)
	}

	// Phase 6: persist the connector with sensitive fields stripped.
	connector := &dbmodels.Connector{
		Name:      req.Name,
		Type:      req.Type,
		Provider:  req.ProviderID,
		Status:    dbmodels.ConnectorStatusConnected,
		Metadata:  catalogutils.StripSensitiveFields(req.Params, catalogutils.SensitiveFieldsFromSchema(schema)),
		CreatedBy: req.CreatedBy,
	}

	if err := s.connectorRepo.Insert(ctx, connector); err != nil {
		s.rollbackConnectorRoute(ctx, routeID, virtualKey)

		return nil, fmt.Errorf("failed to persist connector: %w", err)
	}

	// Phase 7: persist the virtual key against the connector.
	if err := s.keyRepo.Insert(ctx, &dbmodels.Key{
		DependencyID:   connector.ID,
		DependencyType: dbmodels.DependencyTypeConnector,
		VirtualKey:     virtualKey,
		RouteID:        routeID,
	}); err != nil {
		if _, delErr := s.connectorRepo.DeleteIfUnlinked(ctx, connector.ID, dbmodels.DependencyTypeConnector); delErr != nil {
			logger.WarningfCtx(ctx, "failed to delete connector %s after key insert failure: %v", connector.ID, delErr)
		}
		s.rollbackConnectorRoute(ctx, routeID, virtualKey)

		return nil, fmt.Errorf("failed to persist virtual key: %w", err)
	}

	return &apimodels.CreateModelResponse{ID: connector.ID, DeploymentType: "remote"}, nil
}

// UpdateRemoteModel re-registers the LiteLLM route with new credentials, probes it,
// then updates the connector row. Returns 405 if the UUID belongs to a local component.
// userID is accepted for audit/ownership checks by callers but is not currently used here.
func (s *ModelService) UpdateRemoteModel(ctx context.Context, id uuid.UUID, _ string, req apimodels.UpdateRemoteModelRequest) (*apimodels.UpdateRemoteModelResponse, error) {
	// Reject if this UUID belongs to a local component (managed pod).
	comp, err := s.componentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to check component: %w", err)
	}
	if comp != nil && comp.CreatedBy != nil {
		return nil, &ValidationError{
			Code:    http.StatusMethodNotAllowed,
			Message: "local models cannot be updated via this endpoint",
		}
	}

	// Fetch the connector.
	connector, err := s.connectorRepo.GetByID(ctx, id, true)
	if err != nil {
		if errors.Is(err, dbrepo.ErrConnectorNotFound) {
			return nil, &ValidationError{Code: http.StatusNotFound, Message: "model not found"}
		}
		return nil, fmt.Errorf("failed to fetch connector: %w", err)
	}

	// Load and validate the provider schema.
	rawSchema, err := s.catalogProvider.GetConnectorProviderParams(ctx, connector.Type, connector.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to load param schema for provider %q: %w", connector.Provider, err)
	}

	schema, err := pkgutils.ConvertRawJsontoMap(rawSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to decode param schema for provider %q: %w", connector.Provider, err)
	}

	if err := validators.ValidateParams(req.Params, schema, fmt.Sprintf("connector provider %q", connector.Provider)); err != nil {
		return nil, err
	}

	// Rebuild the route ID from existing connector metadata.
	existingModelName, _ := connector.Metadata["model_name"].(string)
	routeID := buildRouteID(path.Base(existingModelName), connector.Provider)

	// Delete the old LiteLLM route (best-effort — proceed even if it fails).
	if err := s.deleteLiteLLMRoute(ctx, routeID); err != nil {
		logger.WarningfCtx(ctx, "UpdateRemoteModel: failed to delete old LiteLLM route %q (continuing): %v", routeID, err)
	}

	// Re-register with new params.
	if err := s.registerConnectorRoute(ctx, routeID, connector.Provider, req.Params); err != nil {
		return nil, fmt.Errorf("LiteLLM route re-registration failed: %w", err)
	}

	// Probe the new route; roll back on failure.
	if probeErr := s.probeLiteLLMRoute(ctx, routeID); probeErr != nil {
		if delErr := s.deleteLiteLLMRoute(ctx, routeID); delErr != nil {
			logger.WarningfCtx(ctx, "UpdateRemoteModel: failed to delete LiteLLM route %q after failed probe: %v", routeID, delErr)
		}
		return nil, &ValidationError{
			Code:    http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("Connection test failed: %v", probeErr),
		}
	}

	// Persist updated metadata (sensitive fields stripped).
	updatedConnector, err := s.connectorRepo.Update(ctx, id, dbrepo.ConnectorUpdateFields{
		Metadata: catalogutils.StripSensitiveFields(req.Params, catalogutils.SensitiveFieldsFromSchema(schema)),
		Status:   dbmodels.ConnectorStatusConnected,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update connector: %w", err)
	}

	// Resolve provider display name.
	providerName := updatedConnector.Provider
	if catalogConn, loadErr := s.catalogProvider.LoadConnector(updatedConnector.Type, updatedConnector.Provider); loadErr == nil {
		providerName = catalogConn.Name
	}

	return &apimodels.UpdateRemoteModelResponse{
		ID:             updatedConnector.ID,
		DeploymentType: "remote",
		Name:           updatedConnector.Name,
		Type:           updatedConnector.Type,
		Provider: apimodels.ModelProviderInfo{
			ID:   updatedConnector.Provider,
			Name: providerName,
		},
		Status:    string(updatedConnector.Status),
		Message:   updatedConnector.Message,
		CreatedBy: updatedConnector.CreatedBy,
		CreatedAt: updatedConnector.CreatedAt,
		UpdatedAt: updatedConnector.UpdatedAt,
	}, nil
}

// deleteRemote tears down a remote connector synchronously:
//  1. Revoke and delete the virtual key.
//  2. Delete the LiteLLM route.
//  3. Delete the connector DB row (only if unlinked).
func (s *ModelService) deleteRemote(ctx context.Context, connector *dbmodels.Connector) error {
	modelName, _ := connector.Metadata["model_name"].(string)
	routeID := buildRouteID(path.Base(modelName), connector.Provider)

	// Step 1: revoke and delete the virtual key.
	key, err := s.keyRepo.GetByDependency(ctx, dbmodels.DependencyTypeConnector, connector.ID)
	if err != nil {
		logger.WarningfCtx(ctx, "[modelmanager] connector %s: failed to fetch virtual key (continuing): %v", connector.ID, err)
	}
	if key != nil {
		if err := s.revokeVirtualKey(ctx, key.VirtualKey); err != nil {
			logger.WarningfCtx(ctx, "[modelmanager] connector %s: failed to revoke virtual key (continuing): %v", connector.ID, err)
		}
		if err := s.keyRepo.DeleteByDependency(ctx, dbmodels.DependencyTypeConnector, connector.ID); err != nil {
			logger.WarningfCtx(ctx, "[modelmanager] connector %s: failed to delete keys row (continuing): %v", connector.ID, err)
		}
	}

	// Step 2: deregister LiteLLM route.
	if err := s.deleteLiteLLMRoute(ctx, routeID); err != nil {
		logger.WarningfCtx(ctx, "[modelmanager] connector %s: failed to deregister LiteLLM route %q (continuing): %v", connector.ID, routeID, err)
	}

	// Step 3: delete the connector row.
	if _, err := s.connectorRepo.DeleteIfUnlinked(ctx, connector.ID, dbmodels.DependencyTypeConnector); err != nil {
		if errors.Is(err, dbrepo.ErrConnectorInUse) {
			return &ValidationError{
				Code:    http.StatusConflict,
				Message: "model is in use by one or more active applications",
			}
		}
		return fmt.Errorf("failed to delete connector: %w", err)
	}

	return nil
}

// rollbackConnectorRoute best-effort revokes the virtual key (if any) and deletes the LiteLLM route.
func (s *ModelService) rollbackConnectorRoute(ctx context.Context, routeID, virtualKey string) {
	if virtualKey != "" {
		if err := s.revokeVirtualKey(ctx, virtualKey); err != nil {
			logger.WarningfCtx(ctx, "failed to revoke virtual key for route %q during rollback: %v", routeID, err)
		}
	}
	if err := s.deleteLiteLLMRoute(ctx, routeID); err != nil {
		logger.WarningfCtx(ctx, "failed to delete LiteLLM route %q during rollback: %v", routeID, err)
	}
}

// registerConnectorRoute calls POST /model/new for a remote connector.
// litellm_params.model is "{provider_id}/{params.model_name}"; every other param
// (api_base, api_key, project_id, ...) is passed through unchanged.
// model_info.id is set to the route ID so the route can be removed via /model/delete.
func (s *ModelService) registerConnectorRoute(ctx context.Context, routeID, providerID string, params map[string]any) error {
	litellmParams := make(map[string]any, len(params))
	for k, v := range params {
		if k != "model_name" {
			litellmParams[k] = v
		}
	}
	litellmParams["model"] = fmt.Sprintf("%s/%v", providerID, params["model_name"])

	payload := map[string]any{
		"model_name":     routeID,
		"litellm_params": litellmParams,
		"model_info":     map[string]any{"id": routeID},
	}

	return s.litellmPost(ctx, "/model/new", payload)
}

// probeLiteLLMRoute calls GET /health?model=<routeID> and returns an error unless
// LiteLLM reports at least one healthy endpoint and no unhealthy ones.
func (s *ModelService) probeLiteLLMRoute(ctx context.Context, routeID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, litellmURL()+"/health?model="+url.QueryEscape(routeID), nil)
	if err != nil {
		return fmt.Errorf("failed to create health request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+litellmMasterKey())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("health request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		HealthyCount       int `json:"healthy_count"`
		UnhealthyCount     int `json:"unhealthy_count"`
		UnhealthyEndpoints []struct {
			Error string `json:"error"`
		} `json:"unhealthy_endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode health response: %w", err)
	}

	if result.UnhealthyCount > 0 {
		if len(result.UnhealthyEndpoints) > 0 && result.UnhealthyEndpoints[0].Error != "" {
			return fmt.Errorf("%s", result.UnhealthyEndpoints[0].Error)
		}

		return fmt.Errorf("endpoint unhealthy")
	}
	if result.HealthyCount == 0 {
		return fmt.Errorf("no endpoints returned")
	}

	return nil
}
