package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
)

// DeployModelRequest is the request body for POST /api/v1/models.
// It deploys a new local model by creating a pod on the control plane or a remote worker.
type DeployModelRequest struct {
	// Type is the component role: "llm", "embedding", or "reranker".
	Type string `json:"type" binding:"required"`
	// Name is a human-readable slug for this deployment (3–100 chars, slug-safe).
	Name string `json:"name" binding:"required,min=3,max=100"`
	// ProviderID identifies the local backend, e.g. "vllm-cpu" or "vllm-spyre".
	ProviderID string `json:"provider_id" binding:"required"`
	// WorkerSelector is an optional Worker LPAR ID (e.g. "lpar-1").
	// Omit to deploy on the control-plane Podman socket.
	WorkerSelector string `json:"worker_selector,omitempty"`
	// Params holds provider-specific configuration (e.g. model).
	Params map[string]any `json:"params" binding:"required"`
	// CreatedBy is set from the auth context; never from the request body.
	CreatedBy string `json:"-"`
}

// DeployModelResponse is the body returned with 202 Accepted.
type DeployModelResponse struct {
	// ID is the UUID of the newly created component row.
	ID uuid.UUID `json:"id"`
}

// CreateModelConnectorRequest is the request body for POST /api/v1/connectors/models.
// It mirrors CreateDatasourceRequest with an additional Type field (llm, embedding, reranker).
type CreateModelConnectorRequest struct {
	// Name is the unique human-readable label for this connector (3–100 chars, case-insensitive unique).
	Name string `json:"name" binding:"required,min=3,max=100"`
	// Type is the connector type: "llm", "embedding", or "reranker".
	Type string `json:"type" binding:"required"`
	// ProviderID identifies the remote provider (e.g. "watsonx").
	ProviderID string `json:"provider_id" binding:"required"`
	// Params holds the flat provider-specific configuration validated against the provider's schema.json.
	// Sensitive fields (format: "password") are passed to LiteLLM and never stored.
	Params map[string]any `json:"params" binding:"required"`
	// CreatedBy is set from the auth context, never from the request body.
	CreatedBy string `json:"-"`
}

// CreateModelConnectorResponse is the response body returned with 201 Created.
type CreateModelConnectorResponse struct {
	ID string `json:"id"`
}

// ModelProviderInfo is the provider sub-object embedded in model API responses.
type ModelProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ModelWorkerInfo is the worker sub-object embedded in model API responses.
// Nil when the model was deployed on the control-plane Podman socket.
type ModelWorkerInfo struct {
	ID          string `json:"id"`
	RuntimeType string `json:"runtime_type"`
	Status      string `json:"status"`
}

// ModelListItem is a single entry in the list-models response.
type ModelListItem struct {
	ID        uuid.UUID         `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Provider  ModelProviderInfo `json:"provider"`
	Worker    *ModelWorkerInfo  `json:"worker"`
	Metadata  map[string]any    `json:"metadata"`
	Status    string            `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ListModelsRequest carries validated pagination and filter params for GET /api/v1/models.
type ListModelsRequest struct {
	Type     string
	Page     int
	PageSize int
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

// GetModelResponse is the response body for GET /api/v1/models/:id.
type GetModelResponse struct {
	ID           uuid.UUID             `json:"id"`
	Name         string                `json:"name"`
	Type         string                `json:"type"`
	Provider     ModelProviderInfo     `json:"provider"`
	Worker       *ModelWorkerInfo      `json:"worker"`
	Metadata     map[string]any        `json:"metadata"`
	Status       string                `json:"status"`
	Message      string                `json:"message,omitempty"`
	Endpoints    []ModelEndpoint       `json:"endpoints"`
	Applications []ModelApplicationRef `json:"applications"`
	CreatedBy    string                `json:"created_by"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

// UndeployModelResponse is the response body for DELETE /api/v1/models/:id.
type UndeployModelResponse struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// GetModelKeyResponse is the response body for GET /api/v1/keys/:component_id.
type GetModelKeyResponse struct {
	ComponentID string `json:"component_id"`
	VirtualKey  string `json:"virtual_key"`
	RouteID     string `json:"route_id"`
}

// Made with Bob
