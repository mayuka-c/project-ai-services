package models

import (
	"time"

	"github.com/google/uuid"
)

// Component represents a reusable component in the catalog.
// Components are infrastructure pieces that can be shared across multiple services,
// such as LLM servers, embedding models, vector databases, etc.
type Component struct {
	ID        uuid.UUID        `json:"id"`
	Type      string           `json:"type"`              // e.g., "llm", "embedding", "vector_db", "reranker"
	Provider  string           `json:"provider"`          // e.g., "vllm-cpu", "vllm-spyre"
	Status    ComponentStatus  `json:"status"`
	Message   string           `json:"message,omitempty"`
	Endpoints []map[string]any `json:"endpoints,omitempty"` // JSONB field for endpoint configurations
	Version   string           `json:"version"`             // Component version
	Metadata  map[string]any   `json:"metadata,omitempty"`  // JSONB field for additional metadata
	Name      *string          `json:"name,omitempty"`      // Human-readable label; NULL for pipeline-created components
	CreatedBy *string          `json:"created_by,omitempty"` // User who triggered deploy; NULL for pipeline-created components
	WorkerID  *uuid.UUID       `json:"worker_id,omitempty"`  // UUID FK to workers.id; NULL = control-plane Podman deploy
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// Made with Bob
