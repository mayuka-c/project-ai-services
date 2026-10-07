package models

import (
	"time"

	"github.com/google/uuid"
)

// Key stores the LiteLLM virtual key for a locally deployed model component.
// The virtual_key value (sk-...) is treated as a secret and is never included in
// list responses or server logs. It is served only via the authenticated
// GET /api/v1/keys/:component_id endpoint.
type Key struct {
	ID          uuid.UUID `json:"id"`
	ComponentID uuid.UUID `json:"component_id"`
	VirtualKey  string    `json:"-"` // never serialised in list responses
	RouteID     string    `json:"route_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// Made with Bob
