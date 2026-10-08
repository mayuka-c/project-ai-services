package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/db/models"
)

// KeyRepository defines the interface for LiteLLM virtual key data operations.
type KeyRepository interface {
	// Insert creates a new key row for a deployed model component.
	Insert(ctx context.Context, key *models.Key) error
	// GetByComponentID returns the key for a given component, or (nil, nil) if not found.
	GetByComponentID(ctx context.Context, componentID uuid.UUID) (*models.Key, error)
	// GetByRouteID returns the key with the given route_id, or (nil, nil) if not found.
	// Used to look up per-application virtual keys whose route_id encodes both the
	// model route and the application UUID.
	GetByRouteID(ctx context.Context, routeID string) (*models.Key, error)
	// DeleteByComponentID removes the key row for the given component.
	// It is a no-op (returns nil) when no row matches.
	DeleteByComponentID(ctx context.Context, componentID uuid.UUID) error
}

// keyRepo implements KeyRepository using pgx.
type keyRepo struct {
	pool *pgxpool.Pool
}

// NewKeyRepository creates a new KeyRepository instance.
func NewKeyRepository(pool *pgxpool.Pool) KeyRepository {
	return &keyRepo{pool: pool}
}

// Insert creates a new key row and populates the generated ID and created_at from RETURNING.
func (r *keyRepo) Insert(ctx context.Context, key *models.Key) error {
	query := `
		INSERT INTO keys (component_id, virtual_key, route_id)
		VALUES ($1, $2, $3)
		RETURNING id, created_at
	`

	err := r.pool.QueryRow(ctx, query, key.ComponentID, key.VirtualKey, key.RouteID).
		Scan(&key.ID, &key.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert key: %w", err)
	}

	return nil
}

// GetByComponentID returns the key for a given component, or (nil, nil) if not found.
func (r *keyRepo) GetByComponentID(ctx context.Context, componentID uuid.UUID) (*models.Key, error) {
	query := `
		SELECT id, component_id, virtual_key, route_id, created_at
		FROM keys
		WHERE component_id = $1
	`

	var k models.Key
	err := r.pool.QueryRow(ctx, query, componentID).Scan(
		&k.ID, &k.ComponentID, &k.VirtualKey, &k.RouteID, &k.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get key for component %s: %w", componentID, err)
	}

	return &k, nil
}

// GetByRouteID returns the key with the given route_id, or (nil, nil) if not found.
func (r *keyRepo) GetByRouteID(ctx context.Context, routeID string) (*models.Key, error) {
	query := `
		SELECT id, component_id, virtual_key, route_id, created_at
		FROM keys
		WHERE route_id = $1
	`

	var k models.Key
	err := r.pool.QueryRow(ctx, query, routeID).Scan(
		&k.ID, &k.ComponentID, &k.VirtualKey, &k.RouteID, &k.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get key for route_id %s: %w", routeID, err)
	}

	return &k, nil
}

// DeleteByComponentID removes the key row for the given component.
func (r *keyRepo) DeleteByComponentID(ctx context.Context, componentID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM keys WHERE component_id = $1`, componentID)
	if err != nil {
		return fmt.Errorf("failed to delete key for component %s: %w", componentID, err)
	}
	return nil
}

// Made with Bob
