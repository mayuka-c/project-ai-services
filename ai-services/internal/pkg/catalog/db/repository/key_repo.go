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
	// Insert creates a new key row for a managed model (component or connector).
	Insert(ctx context.Context, key *models.Key) error
	// GetByDependency returns the key for the given component/connector, or (nil, nil) if not found.
	GetByDependency(ctx context.Context, depType models.DependencyType, depID uuid.UUID) (*models.Key, error)
	// GetByRouteID returns the key with the given route_id, or (nil, nil) if not found.
	// Used to look up per-application virtual keys whose route_id encodes both the
	// model route and the application UUID.
	GetByRouteID(ctx context.Context, routeID string) (*models.Key, error)
	// DeleteByDependency removes the key row for the given component/connector.
	// It is a no-op (returns nil) when no row matches.
	DeleteByDependency(ctx context.Context, depType models.DependencyType, depID uuid.UUID) error
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
		INSERT INTO keys (dependency_id, dependency_type, virtual_key, route_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`

	err := r.pool.QueryRow(ctx, query, key.DependencyID, key.DependencyType, key.VirtualKey, key.RouteID).
		Scan(&key.ID, &key.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert key: %w", err)
	}

	return nil
}

// GetByDependency returns the key for the given component/connector, or (nil, nil) if not found.
func (r *keyRepo) GetByDependency(ctx context.Context, depType models.DependencyType, depID uuid.UUID) (*models.Key, error) {
	query := `
		SELECT id, dependency_id, dependency_type, virtual_key, route_id, created_at
		FROM keys
		WHERE dependency_type = $1 AND dependency_id = $2
	`

	var k models.Key
	err := r.pool.QueryRow(ctx, query, depType, depID).Scan(
		&k.ID, &k.DependencyID, &k.DependencyType, &k.VirtualKey, &k.RouteID, &k.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get key for %s %s: %w", depType, depID, err)
	}

	return &k, nil
}

// GetByRouteID returns the key with the given route_id, or (nil, nil) if not found.
func (r *keyRepo) GetByRouteID(ctx context.Context, routeID string) (*models.Key, error) {
	query := `
		SELECT id, dependency_id, dependency_type, virtual_key, route_id, created_at
		FROM keys
		WHERE route_id = $1
	`

	var k models.Key
	err := r.pool.QueryRow(ctx, query, routeID).Scan(
		&k.ID, &k.DependencyID, &k.DependencyType, &k.VirtualKey, &k.RouteID, &k.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get key for route_id %s: %w", routeID, err)
	}

	return &k, nil
}

// DeleteByDependency removes the key row for the given component/connector.
func (r *keyRepo) DeleteByDependency(ctx context.Context, depType models.DependencyType, depID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM keys WHERE dependency_type = $1 AND dependency_id = $2`, depType, depID)
	if err != nil {
		return fmt.Errorf("failed to delete key for %s %s: %w", depType, depID, err)
	}
	return nil
}

// Made with Bob
