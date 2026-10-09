package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/db/models"
)

// ComponentRepository defines the interface for component data operations.
type ComponentRepository interface {
	// Insert creates a new component in the database.
	Insert(ctx context.Context, component *models.Component) error
	// GetByID retrieves a component by ID.
	GetByID(ctx context.Context, id uuid.UUID) (*models.Component, error)
	// GetAll retrieves all components from the database.
	GetAll(ctx context.Context) ([]models.Component, error)
	// GetByType retrieves all components of a specific type.
	GetByType(ctx context.Context, componentType string) ([]models.Component, error)
	// Update updates a component in the database.
	Update(ctx context.Context, component *models.Component) error
	// UpdateStatus updates only the status and message of a component.
	UpdateStatus(ctx context.Context, id uuid.UUID, status models.ComponentStatus, message string) error
	// UpdateEndpoints updates only the endpoints of a component.
	UpdateEndpoints(ctx context.Context, id uuid.UUID, endpoints []map[string]any) error
	// Delete removes a component from the database.
	Delete(ctx context.Context, id uuid.UUID) error
	// ExistsByTypeAndProvider reports whether any row in the components table has the given type and provider.
	ExistsByTypeAndProvider(ctx context.Context, componentType, provider string) (bool, error)

	// ListManaged returns all managed model components (created_by IS NOT NULL, type IN llm/embedding/reranker).
	// When componentType is non-empty, results are further filtered to that single type.
	// Results are paginated: offset = (page-1)*pageSize.
	ListManaged(ctx context.Context, componentType string, offset, limit int) ([]models.Component, int, error)
	// ExistsByTypeAndActiveStatus reports whether a managed component with the given type
	// is already in Running or Deploying status.
	ExistsByTypeAndActiveStatus(ctx context.Context, componentType string) (bool, error)
	// GetApplicationsByComponentID returns the list of applications linked to a component
	// via service_dependencies (dependency_type = 'component').
	GetApplicationsByComponentID(ctx context.Context, componentID uuid.UUID) ([]ApplicationRef, error)
	// GetRunningByTypeAndProvider returns the first managed component (created_by IS NOT NULL)
	// that has the given type and provider and is in Running status, or (nil, nil) if none exists.
	GetRunningByTypeAndProvider(ctx context.Context, componentType, provider string) (*models.Component, error)
}

// ApplicationRef is a lightweight struct for applications linked to a component.
type ApplicationRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// componentRepo implements ComponentRepository using pgx.
type componentRepo struct {
	pool *pgxpool.Pool
}

// NewComponentRepository creates a new ComponentRepository instance.
func NewComponentRepository(pool *pgxpool.Pool) ComponentRepository {
	return &componentRepo{pool: pool}
}

// Insert creates a new component in the database.
func (r *componentRepo) Insert(ctx context.Context, component *models.Component) error {
	query := `
		INSERT INTO components (id, type, provider, status, message, endpoints, version, metadata, name, created_by, worker_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at, updated_at
	`

	// Generate UUID if not provided
	if component.ID == uuid.Nil {
		component.ID = uuid.New()
	}

	// Marshal endpoints to JSONB
	var endpointsJSON []byte
	var err error
	if component.Endpoints != nil {
		endpointsJSON, err = json.Marshal(component.Endpoints)
		if err != nil {
			return fmt.Errorf("failed to marshal endpoints: %w", err)
		}
	}

	// Marshal metadata to JSONB
	var metadataJSON []byte
	if component.Metadata != nil {
		metadataJSON, err = json.Marshal(component.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	err = r.pool.QueryRow(
		ctx,
		query,
		component.ID,
		component.Type,
		component.Provider,
		component.Status,
		sql.NullString{String: component.Message, Valid: component.Message != ""},
		endpointsJSON,
		sql.NullString{String: component.Version, Valid: component.Version != ""},
		metadataJSON,
		component.Name,
		component.CreatedBy,
		component.WorkerID,
	).Scan(&component.CreatedAt, &component.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to insert component: %w", err)
	}

	return nil
}

// GetByID retrieves a component by ID.
func (r *componentRepo) GetByID(ctx context.Context, id uuid.UUID) (*models.Component, error) {
	query := `
		SELECT id, type, provider, status, message, endpoints, version, metadata,
		       name, created_by, worker_id, created_at, updated_at
		FROM components
		WHERE id = $1
	`

	var (
		component     models.Component
		endpointsJSON []byte
		metadataJSON  []byte
		version       sql.NullString
		message       sql.NullString
		name          sql.NullString
		createdBy     sql.NullString
		workerID      uuid.NullUUID
	)

	err := r.pool.QueryRow(ctx, query, id).Scan(
		&component.ID,
		&component.Type,
		&component.Provider,
		&component.Status,
		&message,
		&endpointsJSON,
		&version,
		&metadataJSON,
		&name,
		&createdBy,
		&workerID,
		&component.CreatedAt,
		&component.UpdatedAt,
	)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to get component: %w", err)
	}

	applyNullableFields(&component, version, message, name, createdBy, workerID)

	if err := unmarshalJSONFields(&component, endpointsJSON, metadataJSON); err != nil {
		return nil, err
	}

	return &component, nil
}

// scanComponent scans a component row and unmarshals JSON fields.
func scanComponent(rows pgx.Rows) (*models.Component, error) {
	var (
		component     models.Component
		endpointsJSON []byte
		metadataJSON  []byte
		version       sql.NullString
		message       sql.NullString
		name          sql.NullString
		createdBy     sql.NullString
		workerID      uuid.NullUUID
	)

	err := rows.Scan(
		&component.ID, &component.Type, &component.Provider, &component.Status, &message,
		&endpointsJSON, &version, &metadataJSON,
		&name, &createdBy, &workerID,
		&component.CreatedAt, &component.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan component: %w", err)
	}

	applyNullableFields(&component, version, message, name, createdBy, workerID)

	if err := unmarshalJSONFields(&component, endpointsJSON, metadataJSON); err != nil {
		return nil, err
	}

	return &component, nil
}

// applyNullableFields copies nullable SQL values onto a component struct.
func applyNullableFields(c *models.Component, version, message, name, createdBy sql.NullString, workerID uuid.NullUUID) {
	if version.Valid {
		c.Version = version.String
	}
	if message.Valid {
		c.Message = message.String
	}
	if name.Valid {
		c.Name = &name.String
	}
	if createdBy.Valid {
		c.CreatedBy = &createdBy.String
	}
	if workerID.Valid {
		c.WorkerID = &workerID.UUID
	}
}

// unmarshalJSONFields unmarshals the endpoints and metadata JSONB columns.
func unmarshalJSONFields(c *models.Component, endpointsJSON, metadataJSON []byte) error {
	if len(endpointsJSON) > 0 {
		if err := json.Unmarshal(endpointsJSON, &c.Endpoints); err != nil {
			return fmt.Errorf("failed to unmarshal endpoints: %w", err)
		}
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &c.Metadata); err != nil {
			return fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}
	return nil
}

const selectComponentColumns = `
	SELECT id, type, provider, status, message, endpoints, version, metadata,
	       name, created_by, worker_id, created_at, updated_at
	FROM components
`

// GetAll retrieves all components from the database.
func (r *componentRepo) GetAll(ctx context.Context) ([]models.Component, error) {
	query := selectComponentColumns + `ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query components: %w", err)
	}
	defer rows.Close()

	return collectComponents(rows)
}

// GetByType retrieves all components of a specific type.
func (r *componentRepo) GetByType(ctx context.Context, componentType string) ([]models.Component, error) {
	query := selectComponentColumns + `WHERE type = $1 ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, query, componentType)
	if err != nil {
		return nil, fmt.Errorf("failed to query components by type: %w", err)
	}
	defer rows.Close()

	return collectComponents(rows)
}

// managedModelTypes is the fixed set of component types exposed by the model management API.
// Shared infrastructure types (e.g. vector_db) live in the same table but must not appear
// in model list/get responses.
var managedModelTypes = []string{"llm", "embedding", "reranker"}

// ListManaged returns all managed model components (created_by IS NOT NULL, type IN llm/embedding/reranker),
// optionally filtered to a single type. Returns the total row count for pagination alongside the page slice.
func (r *componentRepo) ListManaged(ctx context.Context, componentType string, offset, limit int) ([]models.Component, int, error) {
	var (
		rows    pgx.Rows
		err     error
		total   int
		cntArgs []any
		selArgs []any
	)

	// Always scope to model types — prevents vector_db and other infrastructure
	// components from leaking into model management responses.
	cntQuery := `SELECT COUNT(*) FROM components WHERE created_by IS NOT NULL AND type = ANY($1)`
	selQuery := selectComponentColumns + `WHERE created_by IS NOT NULL AND type = ANY($1)`
	cntArgs = append(cntArgs, managedModelTypes)
	selArgs = append(selArgs, managedModelTypes)

	if componentType != "" {
		cntQuery += ` AND type = $2`
		selQuery += ` AND type = $2`
		cntArgs = append(cntArgs, componentType)
		selArgs = append(selArgs, componentType)
	}

	selQuery += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, len(selArgs)+1, len(selArgs)+2)
	selArgs = append(selArgs, limit, offset)

	if err = r.pool.QueryRow(ctx, cntQuery, cntArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count managed components: %w", err)
	}

	rows, err = r.pool.Query(ctx, selQuery, selArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query managed components: %w", err)
	}
	defer rows.Close()

	components, err := collectComponents(rows)
	if err != nil {
		return nil, 0, err
	}

	return components, total, nil
}

// ExistsByTypeAndActiveStatus reports whether a managed component with the given type is
// already in Running or Deploying status.
func (r *componentRepo) ExistsByTypeAndActiveStatus(ctx context.Context, componentType string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM components
			WHERE type        = $1
			  AND created_by IS NOT NULL
			  AND status     IN ('Running', 'Deploying')
		)`,
		componentType,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check active component existence: %w", err)
	}
	return exists, nil
}

// GetApplicationsByComponentID returns the list of applications linked to a component
// via service_dependencies (dependency_type = 'component').
func (r *componentRepo) GetApplicationsByComponentID(ctx context.Context, componentID uuid.UUID) ([]ApplicationRef, error) {
	query := `
		SELECT DISTINCT a.id, a.name
		FROM service_dependencies sd
		JOIN services s ON s.id = sd.service_id
		JOIN applications a ON a.id = s.app_id
		WHERE sd.dependency_id   = $1
		  AND sd.dependency_type = 'component'
	`

	rows, err := r.pool.Query(ctx, query, componentID)
	if err != nil {
		return nil, fmt.Errorf("failed to query applications for component: %w", err)
	}
	defer rows.Close()

	var refs []ApplicationRef
	for rows.Next() {
		var ref ApplicationRef
		if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
			return nil, fmt.Errorf("failed to scan application ref: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating application refs: %w", err)
	}
	return refs, nil
}

// Update updates a component in the database.
func (r *componentRepo) Update(ctx context.Context, component *models.Component) error {
	query := `
		UPDATE components
		SET type = $1, provider = $2, endpoints = $3, version = $4, metadata = $5, updated_at = NOW()
		WHERE id = $6
		RETURNING updated_at
	`

	// Marshal endpoints to JSONB
	var endpointsJSON []byte
	var err error
	if component.Endpoints != nil {
		endpointsJSON, err = json.Marshal(component.Endpoints)
		if err != nil {
			return fmt.Errorf("failed to marshal endpoints: %w", err)
		}
	}

	// Marshal metadata to JSONB
	var metadataJSON []byte
	if component.Metadata != nil {
		metadataJSON, err = json.Marshal(component.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	err = r.pool.QueryRow(
		ctx,
		query,
		component.Type,
		component.Provider,
		endpointsJSON,
		sql.NullString{String: component.Version, Valid: component.Version != ""},
		metadataJSON,
		component.ID,
	).Scan(&component.UpdatedAt)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}

		return fmt.Errorf("failed to update component: %w", err)
	}

	return nil
}

// UpdateStatus updates only the status and message of a component.
func (r *componentRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status models.ComponentStatus, message string) error {
	query := `
		UPDATE components
		SET status = $1, message = $2, updated_at = NOW()
		WHERE id = $3
	`

	_, err := r.pool.Exec(ctx, query, status, sql.NullString{String: message, Valid: message != ""}, id)
	if err != nil {
		return fmt.Errorf("failed to update component status: %w", err)
	}

	return nil
}

// UpdateEndpoints updates only the endpoints of a component.
func (r *componentRepo) UpdateEndpoints(ctx context.Context, id uuid.UUID, endpoints []map[string]any) error {
	query := `
		UPDATE components
		SET endpoints = $1, updated_at = NOW()
		WHERE id = $2
	`

	// Marshal endpoints to JSONB
	var endpointsJSON []byte
	var err error
	if endpoints != nil {
		endpointsJSON, err = json.Marshal(endpoints)
		if err != nil {
			return fmt.Errorf("failed to marshal endpoints: %w", err)
		}
	}

	_, err = r.pool.Exec(ctx, query, endpointsJSON, id)
	if err != nil {
		return fmt.Errorf("failed to update component endpoints: %w", err)
	}

	return nil
}

// Delete removes a component from the database along with its keys row (keys.dependency_id
// is polymorphic, so there is no FK cascade).
func (r *componentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	query := `
		WITH deleted_keys AS (
			DELETE FROM keys WHERE dependency_type = 'component' AND dependency_id = $1
		)
		DELETE FROM components WHERE id = $1`

	_, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete component: %w", err)
	}

	return nil
}

// ExistsByTypeAndProvider reports whether any row in the components table has the given type and provider.
func (r *componentRepo) ExistsByTypeAndProvider(ctx context.Context, componentType, provider string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM components WHERE type = $1 AND provider = $2)`, componentType, provider).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check component existence by type and provider: %w", err)
	}

	return exists, nil
}

// collectComponents iterates rows and returns a slice of components.
func collectComponents(rows pgx.Rows) ([]models.Component, error) {
	var components []models.Component
	for rows.Next() {
		component, err := scanComponent(rows)
		if err != nil {
			return nil, err
		}
		components = append(components, *component)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating components: %w", err)
	}
	return components, nil
}

// GetRunningByTypeAndProvider returns the first managed component with the given type and
// provider that is currently in Running status, or (nil, nil) when none exists.
// "Managed" means created_by IS NOT NULL — i.e. deployed via the model-manager API.
func (r *componentRepo) GetRunningByTypeAndProvider(ctx context.Context, componentType, provider string) (*models.Component, error) {
	query := selectComponentColumns + `
		WHERE type       = $1
		  AND provider   = $2
		  AND status     = 'Running'
		  AND created_by IS NOT NULL
		ORDER BY created_at DESC
		LIMIT 1`

	rows, err := r.pool.Query(ctx, query, componentType, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to query running component for %s/%s: %w", componentType, provider, err)
	}
	defer rows.Close()

	components, err := collectComponents(rows)
	if err != nil {
		return nil, err
	}
	if len(components) == 0 {
		return nil, nil
	}
	return &components[0], nil
}

// Made with Bob
