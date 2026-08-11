// Package bundle implements the service layer for catalog bundle upload and management.
package bundle

import (
	"context"
	"io"
	"time"
)

// BundleServiceInterface defines the contract for bundle business logic.
type BundleServiceInterface interface {
	// ValidateBundle performs a validate-only pass on the archive: reads metadata.yaml,
	// extracts to a temp directory, validates structure, then cleans up.
	// No DB row is written and no reload is triggered.
	ValidateBundle(ctx context.Context, file io.Reader) (*ValidationResult, error)

	// ProcessBundle handles a POST upload.
	// id, type, and version are all read from metadata.yaml inside the archive.
	// Returns a *BundleResponse on success.
	ProcessBundle(ctx context.Context, file io.Reader, userID string) (*BundleResponse, error)

	// ReplaceBundle handles a real (non-dry-run) PUT update.
	// existing is the current active bundle record; CatalogID and CatalogType are read from it
	// and validated as immutable against the archive metadata. Version is read from the archive.
	// Returns a *BundleResponse with status "processing" immediately.
	ReplaceBundle(ctx context.Context, existing *BundleRecord, file io.Reader, userID string) (*BundleResponse, error)

	// GetByBundleID returns the bundle record for the given internal bundle ID, or nil.
	GetByBundleID(ctx context.Context, bundleID string) (*BundleRecord, error)

	// GetBundleByID returns the BundleResponse for the given internal bundle ID, or nil.
	// Used by the GET /api/v1/catalog/bundles/:id polling endpoint.
	GetBundleByID(ctx context.Context, bundleID string) (*BundleResponse, error)

	// DeleteBundle removes the bundle's on-disk directory, deletes the DB record, and
	// triggers a reload — all synchronously.
	DeleteBundle(ctx context.Context, existing *BundleRecord) error

	// ListBundles returns all bundles ordered by uploaded_at descending.
	ListBundles(ctx context.Context) (*BundleListResponse, error)
}

// BundleRecord is the service-layer view of a bundle DB row.
type BundleRecord struct {
	ID          string
	Name        string
	Status      string
	SizeBytes   *int64
	CatalogType string
	CatalogID   string
	Version     string
	UploadedBy  string
	UploadedAt  time.Time
}

// BundleMetadata holds the fields read from metadata.yaml inside an archive.
type BundleMetadata struct {
	CatalogID   string
	CatalogType string
	Version     string
}

// BundleResponse is the HTTP response body for a single bundle (202 / GET).
type BundleResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	UploadedAt  string  `json:"uploaded_at"`
	SizeBytes   *int64  `json:"size_bytes"`
	CatalogType string  `json:"catalog_type"`
	CatalogID   string  `json:"catalog_id"`
	Version     string  `json:"version"`
	UploadedBy  string  `json:"uploaded_by"`
	Error       *string `json:"error,omitempty"`
}

// BundleListResponse is the HTTP response body for GET /api/v1/catalog/bundles.
type BundleListResponse struct {
	Bundles []BundleResponse `json:"bundles"`
}

// ValidationResult is the response body for POST /catalog/bundles/validate.
type ValidationResult struct {
	Valid       bool   `json:"valid"`
	CatalogID   string `json:"catalog_id"`
	CatalogType string `json:"catalog_type"`
	Version     string `json:"version"`
}

// ValidationError represents a validation or request error with an HTTP status code.
type ValidationError struct {
	Code    int
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}
