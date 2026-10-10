package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
)

// CreateModelRequest is the request body for POST /api/v1/models.
// deployment_type=local deploys a pod; deployment_type=remote registers a remote connector.
type CreateModelRequest struct {
	// DeploymentType selects the backend: "local" (pod) or "remote" (connector, no pod).
	DeploymentType string `json:"deployment_type" binding:"required"`
	// Type is the model role: "llm", "embedding", or "reranker".
	Type string `json:"type" binding:"required"`
	// Name is a human-readable slug (3–100 chars, slug-safe, unique case-insensitively).
	Name string `json:"name" binding:"required,min=3,max=100"`
	// ProviderID identifies the backend.
	// Local: "vllm-cpu", "vllm-spyre". Remote: "watsonx", "hosted_vllm", "openai".
	ProviderID string `json:"provider_id" binding:"required"`
	// Params holds provider-specific configuration validated against the provider's schema.json.
	// For remote models, sensitive fields (format:"password") are forwarded to LiteLLM only.
	Params map[string]any `json:"params" binding:"required"`
	// WorkerSelector is an optional Worker LPAR name (e.g. "lpar-1"). Local only.
	// Omit to deploy on the control-plane Podman socket.
	WorkerSelector string `json:"worker_selector,omitempty"`
	// CreatedBy is set from the auth context; never from the request body.
	CreatedBy string `json:"-"`
}

// CreateModelResponse is returned on a successful POST /api/v1/models.
// Local → 202 Accepted. Remote → 201 Created.
type CreateModelResponse struct {
	ID             uuid.UUID `json:"id"`
	DeploymentType string    `json:"deployment_type"`
}

// UpdateRemoteModelRequest is the body for PUT /api/v1/models/:id.
// Only fields marked ui:section="Authentication" in the provider's schema.json may be updated.
type UpdateRemoteModelRequest struct {
	Params map[string]any `json:"params" binding:"required"`
}

// UpdateRemoteModelResponse is the body returned by PUT /api/v1/models/:id.
type UpdateRemoteModelResponse struct {
	ID             uuid.UUID         `json:"id"`
	DeploymentType string            `json:"deployment_type"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Provider       ModelProviderInfo `json:"provider"`
	Status         string            `json:"status"`
	Message        string            `json:"message,omitempty"`
	CreatedBy      string            `json:"created_by"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// ModelProviderInfo is the provider sub-object in model API responses.
type ModelProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ModelWorkerInfo is the worker sub-object in model API responses.
// Nil when the model runs on the control-plane Podman socket.
type ModelWorkerInfo struct {
	ID          string `json:"id"`
	RuntimeType string `json:"runtime_type"`
	Status      string `json:"status"`
}

// ModelListItem is a single entry in the GET /api/v1/models list response.
type ModelListItem struct {
	ID             uuid.UUID         `json:"id"`
	DeploymentType string            `json:"deployment_type"`
	// ModelID is the LiteLLM route alias — pass this as the `model` field in LiteLLM API calls.
	ModelID        string            `json:"model_id,omitempty"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Provider       ModelProviderInfo `json:"provider"`
	Worker         *ModelWorkerInfo  `json:"worker"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
	Status         string            `json:"status"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// ListModelsRequest carries validated pagination and filter params for GET /api/v1/models.
type ListModelsRequest struct {
	Type           string
	DeploymentType string
	Page           int
	PageSize       int
}

// ListModelsResponse is the paginated response for GET /api/v1/models.
type ListModelsResponse struct {
	Data       []ModelListItem          `json:"data"`
	Pagination types.PaginationMetadata `json:"pagination"`
}

// ModelApplicationRef is a lightweight application reference in the GetModel response.
type ModelApplicationRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ModelEndpoint is a single endpoint entry in the GetModel response.
type ModelEndpoint struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// GetModelResponse is the body for GET /api/v1/models/:id.
type GetModelResponse struct {
	ID             uuid.UUID             `json:"id"`
	DeploymentType string                `json:"deployment_type"`
	// ModelID is the LiteLLM route alias — pass this as the `model` field in LiteLLM API calls.
	ModelID        string                `json:"model_id,omitempty"`
	Name           string                `json:"name"`
	Type           string                `json:"type"`
	Provider       ModelProviderInfo     `json:"provider"`
	Worker         *ModelWorkerInfo      `json:"worker"`
	Metadata       map[string]any        `json:"metadata,omitempty"`
	Status         string                `json:"status"`
	Message        string                `json:"message,omitempty"`
	Endpoints      []ModelEndpoint       `json:"endpoints,omitempty"`
	Applications   []ModelApplicationRef `json:"applications"`
	CreatedBy      string                `json:"created_by"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

// UndeployModelResponse is the body for DELETE /api/v1/models/:id.
type UndeployModelResponse struct {
	ID             string `json:"id"`
	DeploymentType string `json:"deployment_type"`
	Message        string `json:"message"`
}

// GetModelKeyResponse is the body for GET /api/v1/models/keys?instance_id=<id>.
type GetModelKeyResponse struct {
	ComponentID string `json:"component_id"`
	VirtualKey  string `json:"virtual_key"`
	RouteID     string `json:"route_id"`
}

// Made with Bob
