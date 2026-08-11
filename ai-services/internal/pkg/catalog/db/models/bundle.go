package models

import (
	"database/sql"
	"time"
)

// BundleStatus represents the lifecycle status of an uploaded catalog bundle.
type BundleStatus string

const (
	BundleStatusProcessing BundleStatus = "processing"
	BundleStatusActive     BundleStatus = "active"
	BundleStatusFailed     BundleStatus = "failed"
)

// Bundle represents a customer-uploaded catalog bundle record in the database.
type Bundle struct {
	// ID is the internal record ID, e.g. "bnd_01JW4X9K2M8VQRP3T5YZ".
	ID string `json:"id"`
	// Name is the human-readable versioned name: <catalog_id>-<version>,
	// e.g. "my-service-1.0.0". Used as the directory name on the bundle volume.
	Name string `json:"name"`
	// Status tracks the lifecycle: processing → active on success, failed on error.
	Status BundleStatus `json:"status"`
	// SizeBytes is the uncompressed on-disk size in bytes; nil until extraction completes.
	SizeBytes sql.NullInt64 `json:"size_bytes"`
	// CatalogType is the catalog item type declared by the uploader: "service" or "component".
	CatalogType string `json:"catalog_type"`
	// CatalogID is the id of the catalog item, e.g. "my-service".
	CatalogID string `json:"catalog_id"`
	// Version is the semantic version of this bundle, e.g. "1.0.0".
	Version string `json:"version"`
	// Error holds the validation error message when status is "failed".
	Error sql.NullString `json:"error,omitempty"`
	// UploadedBy is the user ID who uploaded this bundle.
	UploadedBy string `json:"uploaded_by"`
	// UploadedAt is the timestamp when the bundle was first recorded.
	UploadedAt time.Time `json:"uploaded_at"`
}
