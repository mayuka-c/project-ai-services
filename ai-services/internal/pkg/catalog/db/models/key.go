package models

import (
	"time"

	"github.com/google/uuid"
)

// Key stores the LiteLLM virtual key for a managed model: either a locally deployed
// component (DependencyType = "component") or a remote model connector
// (DependencyType = "connector"). The virtual_key value (sk-...) is treated as a secret
// and is never included in list responses or server logs.
type Key struct {
	ID             uuid.UUID      `json:"id"`
	DependencyID   uuid.UUID      `json:"dependency_id"`
	DependencyType DependencyType `json:"dependency_type"`
	VirtualKey     string         `json:"-"` // never serialised in list responses
	RouteID        string         `json:"route_id"`
	CreatedAt      time.Time      `json:"created_at"`
}

// Made with Bob
