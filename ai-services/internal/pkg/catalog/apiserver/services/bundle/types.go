// Package bundle implements the service layer for catalog bundle creation and management.
package bundle

import (
	"context"
	"io"
	"time"
)

// BundleServiceInterface defines the contract for bundle business logic.
type BundleServiceInterface interface {
	// ValidateBundle performs a validate-only pass on the archive.
	// No DB row is written and no reload is triggered.
	// Returns a ServiceValidationResult or ComponentValidationResult (both implement ValidationResult).
	ValidateBundle(ctx context.Context, file io.Reader) (ValidationResult, error)

	// ProcessBundle handles a synchronous POST Create Bundle operation.
	// Peeks metadata.yaml, conflict-checks, extracts, inserts the DB row as processing,
	// reloads the catalog, activates the row, and returns status=active (201).
	ProcessBundle(ctx context.Context, file io.Reader, userID string) (*BundleResponse, error)

	// ReplaceBundle handles a synchronous PUT update.
	ReplaceBundle(ctx context.Context, existing *BundleRecord, file io.Reader, userID string) (*BundleResponse, error)

	// GetByBundleID returns the bundle record for the given internal bundle ID, or nil.
	GetByBundleID(ctx context.Context, bundleID string) (*BundleRecord, error)

	// GetBundleByID returns the BundleResponse for the given internal bundle ID, or nil.
	GetBundleByID(ctx context.Context, bundleID string) (*BundleResponse, error)

	// DeleteBundle marks the bundle deleting, removes the on-disk directory, reloads,
	// and deletes the DB record — all synchronously.
	DeleteBundle(ctx context.Context, existing *BundleRecord) error

	// ListBundles returns all bundles ordered by created_at descending.
	ListBundles(ctx context.Context) (*BundleListResponse, error)
}

// BundleRecord is the service-layer view of a bundle DB row.
type BundleRecord struct {
	ID          string
	Name        string // human-readable display label (may be empty)
	Status      string
	SizeBytes   *int64
	CatalogType string
	CatalogID   string
	Version     string
	CreatedBy   string
	CreatedAt   time.Time
}

// ---------------------------------------------------------------------------
// BundleMetadata interface and concrete types
// ---------------------------------------------------------------------------

// BundleMetadata is the interface returned by peekMetadata. Each concrete type
// carries the scalar fields from its metadata.yaml and encodes all
// type-specific derivations as methods — no type assertions needed in the pipeline.
//
// Adding a new catalog type (e.g. "architecture") means:
//  1. Define a new concrete struct implementing this interface.
//  2. Add a case in parseMetadataYAML to construct it.
//  3. The rest of the pipeline (ProcessBundle, ReplaceBundle, ValidateBundle)
//     is unchanged — it calls only these methods.
type BundleMetadata interface {
	// CatalogID returns the globally unique value stored in the DB catalog_id column.
	// Services:   bare id                              e.g. "my-service"
	// Components: composite <component_type>--<id>     e.g. "llm--my-provider"
	CatalogID() string

	// CatalogType returns "service" or "component".
	CatalogType() string

	// Version returns the semantic version string.
	Version() string

	// DisplayName returns the human-readable label from the metadata.yaml `name:` field.
	// e.g. "My Custom Service", "My Custom LLM Provider"
	// Not globally unique — the same label may appear for different catalog_ids.
	DisplayName() string
}

// ServiceMetadata is the BundleMetadata implementation for catalog_type="service".
type ServiceMetadata struct {
	id          string
	version     string
	displayName string
}

func (m *ServiceMetadata) CatalogID() string   { return m.id }
func (m *ServiceMetadata) CatalogType() string { return "service" }
func (m *ServiceMetadata) Version() string     { return m.version }
func (m *ServiceMetadata) DisplayName() string { return m.displayName }

// ComponentMetadata is the BundleMetadata implementation for catalog_type="component".
// ComponentType is required and must be one of the recognised values
// (llm, embedding, reranker, vector_store).
//
// CatalogID is the composite "<component_type>--<id>" value stored in the DB,
// e.g. "llm--my-provider". The double-dash separator avoids filesystem conflicts
// (colons are not valid in directory names on most OSes).
// The same bare id may exist under different component types — they produce different
// CatalogID() values and are stored as entirely independent DB rows and on-disk directories.
type ComponentMetadata struct {
	id            string
	componentType string
	version       string
	displayName   string
}

// CatalogID returns "<component_type>--<id>", e.g. "llm--my-provider".
func (m *ComponentMetadata) CatalogID() string   { return m.componentType + "--" + m.id }
func (m *ComponentMetadata) CatalogType() string { return "component" }
func (m *ComponentMetadata) Version() string     { return m.version }
func (m *ComponentMetadata) DisplayName() string { return m.displayName }

// ComponentType returns the component_type for this metadata.
// Defined on the concrete type — callers that need it may type-assert to *ComponentMetadata.
func (m *ComponentMetadata) ComponentType() string { return m.componentType }

// ---------------------------------------------------------------------------
// ValidationResult interface and concrete types
// ---------------------------------------------------------------------------

// ValidationResult is the interface returned by ValidateBundle and serialised as the
// 200 OK body for POST /catalog/bundles/validate.
// Concrete types: ServiceValidationResult, ComponentValidationResult.
type ValidationResult interface {
	// IsValid reports whether the archive passed validation.
	IsValid() bool

	// GetCatalogType returns "service" or "component".
	GetCatalogType() string

	// GetCatalogID returns the same value as BundleMetadata.CatalogID().
	GetCatalogID() string

	// GetVersion returns the semantic version string from metadata.yaml.
	GetVersion() string

	// GetDisplayName returns the human-readable label from metadata.yaml `name:`.
	GetDisplayName() string
}

// ServiceValidationResult is the ValidationResult implementation for catalog_type="service".
// JSON shape:
//
//	{
//	  "valid":        true,
//	  "catalog_type": "service",
//	  "catalog_id":   "my-service",
//	  "version":      "1.0.0",
//	  "name":         "My Custom Service"
//	}
type ServiceValidationResult struct {
	Valid       bool   `json:"valid"`
	CatalogType string `json:"catalog_type"`
	CatalogID   string `json:"catalog_id"`
	Version     string `json:"version"`
	Name        string `json:"name,omitempty"` // display label; omitted if blank
}

func (r *ServiceValidationResult) IsValid() bool          { return r.Valid }
func (r *ServiceValidationResult) GetCatalogType() string { return r.CatalogType }
func (r *ServiceValidationResult) GetCatalogID() string   { return r.CatalogID }
func (r *ServiceValidationResult) GetVersion() string     { return r.Version }
func (r *ServiceValidationResult) GetDisplayName() string { return r.Name }

// ComponentValidationResult is the ValidationResult implementation for catalog_type="component".
// JSON shape:
//
//	{
//	  "valid":          true,
//	  "catalog_type":   "component",
//	  "component_type": "llm",
//	  "catalog_id":     "llm--my-provider",
//	  "version":        "1.0.0",
//	  "name":           "My Custom LLM Provider"
//	}
type ComponentValidationResult struct {
	Valid         bool   `json:"valid"`
	CatalogType   string `json:"catalog_type"`
	ComponentType string `json:"component_type"`
	CatalogID     string `json:"catalog_id"`
	Version       string `json:"version"`
	Name          string `json:"name,omitempty"` // display label; omitted if blank
}

func (r *ComponentValidationResult) IsValid() bool          { return r.Valid }
func (r *ComponentValidationResult) GetCatalogType() string { return r.CatalogType }
func (r *ComponentValidationResult) GetCatalogID() string   { return r.CatalogID }
func (r *ComponentValidationResult) GetVersion() string     { return r.Version }
func (r *ComponentValidationResult) GetDisplayName() string { return r.Name }

// GetComponentType returns the component_type for this result.
// Defined on the concrete type — callers that need it may type-assert to *ComponentValidationResult.
func (r *ComponentValidationResult) GetComponentType() string { return r.ComponentType }

// ---------------------------------------------------------------------------
// HTTP response types
// ---------------------------------------------------------------------------

// BundleResponse is the HTTP response body for a single bundle (201 / GET).
type BundleResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name,omitempty"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	SizeBytes   *int64  `json:"size_bytes"`
	CatalogType string  `json:"catalog_type"`
	CatalogID   string  `json:"catalog_id"`
	Version     string  `json:"version"`
	CreatedBy   string  `json:"created_by"`
	Error       *string `json:"error,omitempty"`
}

// BundleListResponse is the HTTP response body for GET /api/v1/catalog/bundles.
type BundleListResponse struct {
	Bundles []BundleResponse `json:"bundles"`
}

// ---------------------------------------------------------------------------
// Error type
// ---------------------------------------------------------------------------

// ValidationError represents a validation or request error with an HTTP status code.
type ValidationError struct {
	Code    int
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}
