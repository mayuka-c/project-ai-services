package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-ai-services/ai-services/internal/pkg/catalog/db/models"
	dbrepo "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/repository"
)

const (
	// bundleStorageRoot is the root directory on the bundle volume where all bundles are stored.
	bundleStorageRoot = "/data/catalog-bundles"

	// maxUncompressedBytes is the maximum allowed uncompressed size (200 MB).
	maxUncompressedBytes = 200 * 1024 * 1024
)

// validCatalogTypes lists the accepted catalog_type values.
var validCatalogTypes = map[string]bool{
	"service":   true,
	"component": true,
}

// CatalogReloader is the interface that CatalogProvider satisfies for bundle-triggered reloads.
type CatalogReloader interface {
	Reload(ctx context.Context) error
}

// bundleService is the concrete implementation of BundleServiceInterface.
type bundleService struct {
	repo     dbrepo.BundleRepository
	reloader CatalogReloader
}

// NewBundleService creates a BundleService backed by the given repository and catalog reloader.
// Pass the *catalog.CatalogProvider as the reloader so that uploads immediately refresh the catalog.
func NewBundleService(repo dbrepo.BundleRepository, reloader CatalogReloader) BundleServiceInterface {
	return &bundleService{repo: repo, reloader: reloader}
}

// IsValidCatalogType reports whether the given catalog_type is accepted.
func IsValidCatalogType(t string) bool {
	return validCatalogTypes[t]
}

// ---- BundleServiceInterface implementation ----------------------------------

// GetByBundleID returns the BundleRecord for the given bundle ID, or nil if not found.
func (s *bundleService) GetByBundleID(ctx context.Context, bundleID string) (*BundleRecord, error) {
	b, err := s.repo.GetByID(ctx, bundleID)
	if err != nil {
		return nil, err
	}

	if b == nil {
		return nil, nil
	}

	return toRecord(b), nil
}

// GetBundleByID returns the BundleResponse for the given bundle ID, or nil if not found.
func (s *bundleService) GetBundleByID(ctx context.Context, bundleID string) (*BundleResponse, error) {
	b, err := s.repo.GetByID(ctx, bundleID)
	if err != nil {
		return nil, err
	}

	if b == nil {
		return nil, nil
	}

	return toResponse(b), nil
}

// ListBundles returns all bundle records ordered by created_at descending.
func (s *bundleService) ListBundles(ctx context.Context) (*BundleListResponse, error) {
	bundles, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}

	resp := &BundleListResponse{Bundles: make([]BundleResponse, 0, len(bundles))}
	for i := range bundles {
		resp.Bundles = append(resp.Bundles, *toResponse(&bundles[i]))
	}

	return resp, nil
}

// ValidateBundle extracts the archive to a temp directory, validates structure, then
// cleans up. No DB row is written and no reload is triggered.
// Returns a ServiceValidationResult or ComponentValidationResult (both implement ValidationResult).
func (s *bundleService) ValidateBundle(_ context.Context, file io.Reader) (ValidationResult, error) {
	archiveBytes, err := io.ReadAll(file)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusBadRequest, Message: "failed to read archive"}
	}

	meta, err := peekMetadata(archiveBytes)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	// Validate bundle structure from archive (no extraction to disk).
	if err := validateBundleStructureFromArchive(archiveBytes); err != nil {
		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	// Build the typed validation result matching the archive's catalog type.
	switch meta.CatalogType() {
	case "component":
		cm, _ := meta.(*ComponentMetadata)
		result := &ComponentValidationResult{
			Valid:       true,
			CatalogType: meta.CatalogType(),
			CatalogID:   meta.CatalogID(),
			Version:     meta.Version(),
			Name:        meta.DisplayName(),
		}
		if cm != nil {
			result.ComponentType = cm.ComponentType()
		}

		return result, nil
	default: // "service"
		return &ServiceValidationResult{
			Valid:       true,
			CatalogType: meta.CatalogType(),
			CatalogID:   meta.CatalogID(),
			Version:     meta.Version(),
			Name:        meta.DisplayName(),
		}, nil
	}
}

// ProcessBundle handles bundle creation — fully synchronous.
//
// Steps:
//  1. Read archive bytes.
//  2. Peek metadata.yaml → id, type, version.
//  3. Conflict check: reject 409 if an active bundle with catalog_id already exists.
//  4. Validate bundle structure from archive (no extraction).
//  5. Extract to the permanent bundle directory and measure uncompressed size.
//  6. Insert DB row with status = processing.
//  7. Reload catalog.
//  8. Activate the bundle row.
func (s *bundleService) ProcessBundle(ctx context.Context, file io.Reader, userID string) (*BundleResponse, error) {
	archiveBytes, err := io.ReadAll(file)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusBadRequest, Message: "failed to read archive"}
	}

	// Step 2: read all metadata fields from the archive's metadata.yaml.
	meta, err := peekMetadata(archiveBytes)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	// Step 3: conflict check using the identity fields from metadata.yaml.
	exists, err := s.repo.ActiveCatalogIDExists(ctx, meta.CatalogType(), meta.CatalogID())
	if err != nil {
		return nil, fmt.Errorf("failed to check existing bundles: %w", err)
	}

	if exists {
		return nil, &ValidationError{
			Code:    http.StatusConflict,
			Message: fmt.Sprintf("a bundle with catalog_id %q already exists; use PUT /api/v1/catalog/bundles/:bundle_id to update it", meta.CatalogID()),
		}
	}

	// Step 4: validate bundle structure from archive (before any disk writes).
	if err := validateBundleStructureFromArchive(archiveBytes); err != nil {
		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	// Step 5: extract to the permanent directory and measure size.
	destDir := bundleDirPath(meta.CatalogType(), meta.CatalogID(), meta.Version())

	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create bundle directory: %w", err)
	}

	sizeBytes, err := extractAndMeasure(bytes.NewReader(archiveBytes), destDir)
	if err != nil {
		os.RemoveAll(destDir)

		var valErr *ValidationError
		if errors.As(err, &valErr) {
			return nil, valErr
		}

		return nil, fmt.Errorf("extraction failed: %w", err)
	}

	// Step 6: insert DB row — id is generated by the DB and written back via RETURNING.
	bundle := &models.Bundle{
		CatalogType: meta.CatalogType(),
		CatalogID:   meta.CatalogID(),
		Version:     meta.Version(),
		CreatedBy:   userID,
	}

	if err := s.repo.Insert(ctx, bundle); err != nil {
		os.RemoveAll(destDir)

		return nil, fmt.Errorf("failed to record bundle: %w", err)
	}

	// Step 7: reload catalog so the new bundle is immediately available.
	if err := s.reloader.Reload(ctx); err != nil {
		os.RemoveAll(destDir)
		_ = s.repo.MarkFailed(ctx, bundle.ID, fmt.Sprintf("failed to reload catalog: %v", err))

		return nil, fmt.Errorf("failed to reload catalog: %w", err)
	}

	// Step 8: activate the bundle row after reload succeeds.
	if err := s.repo.Activate(ctx, bundle.ID, meta.Version(), meta.DisplayName(), sizeBytes); err != nil {
		os.RemoveAll(destDir)
		_ = s.repo.MarkFailed(ctx, bundle.ID, fmt.Sprintf("failed to activate bundle: %v", err))

		return nil, fmt.Errorf("failed to activate bundle: %w", err)
	}

	// Re-fetch from DB so the response reflects the final persisted state
	// (status=active, size_bytes, created_at) rather than the stale local struct.
	persisted, err := s.repo.GetByID(ctx, bundle.ID)
	if err != nil || persisted == nil {
		return toResponse(bundle), nil
	}

	return toResponse(persisted), nil
}

// ReplaceBundle handles a synchronous PUT update.
func (s *bundleService) ReplaceBundle(ctx context.Context, existing *BundleRecord, file io.Reader, userID string) (*BundleResponse, error) {
	archiveBytes, err := io.ReadAll(file)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusBadRequest, Message: "failed to read archive"}
	}

	meta, err := peekMetadata(archiveBytes)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	// Validate immutable fields.
	if meta.CatalogID() != existing.CatalogID {
		return nil, &ValidationError{
			Code:    http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("archive catalog_id %q does not match existing record %q; this field is immutable", meta.CatalogID(), existing.CatalogID),
		}
	}

	if meta.CatalogType() != existing.CatalogType {
		return nil, &ValidationError{
			Code:    http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("archive catalog_type %q does not match existing record %q; this field is immutable", meta.CatalogType(), existing.CatalogType),
		}
	}

	// Perform the replacement synchronously.
	if err := s.runReplace(ctx, existing.ID, existing.Version, meta.Version(), meta.DisplayName(), existing.CatalogID, existing.CatalogType, archiveBytes); err != nil {
		return nil, err
	}

	// Fetch and return the updated bundle record.
	return s.GetBundleByID(ctx, existing.ID)
}

// runReplace extracts and activates a bundle replacement synchronously.
// It updates the existing DB row in place — no new row is inserted.
func (s *bundleService) runReplace(ctx context.Context, bundleID, oldVersion, newVersion, displayName, catalogID, catalogType string, archiveBytes []byte) error {
	// Validate bundle structure from archive before any extraction.
	if err := validateBundleStructureFromArchive(archiveBytes); err != nil {
		return &ValidationError{Code: http.StatusUnprocessableEntity, Message: fmt.Sprintf("bundle structure validation failed: %v", err)}
	}

	if err := s.repo.MarkProcessing(ctx, bundleID); err != nil {
		return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to mark bundle processing: %v", err)}
	}

	newDir := bundleDirPath(catalogType, catalogID, newVersion)
	oldDir := bundleDirPath(catalogType, catalogID, oldVersion)
	stagingDir := newDir + "-new"

	if err := os.RemoveAll(stagingDir); err != nil {
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to clean staging bundle directory: %v", err))
		return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to clean staging bundle directory: %v", err)}
	}

	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to create staging bundle directory: %v", err))
		return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to create staging bundle directory: %v", err)}
	}

	sizeBytes, err := extractAndMeasure(bytes.NewReader(archiveBytes), stagingDir)
	if err != nil {
		os.RemoveAll(stagingDir)
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to extract archive: %v", err))
		return &ValidationError{Code: http.StatusUnprocessableEntity, Message: fmt.Sprintf("failed to extract archive: %v", err)}
	}

	if err := os.RemoveAll(newDir); err != nil {
		os.RemoveAll(stagingDir)
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to remove existing target bundle directory: %v", err))
		return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to remove existing target bundle directory: %v", err)}
	}

	if err := os.Rename(stagingDir, newDir); err != nil {
		os.RemoveAll(stagingDir)
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to promote staging bundle directory: %v", err))
		return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to promote staging bundle directory: %v", err)}
	}

	if err := s.repo.Activate(ctx, bundleID, newVersion, displayName, sizeBytes); err != nil {
		os.RemoveAll(newDir)
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to activate bundle: %v", err))
		return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to activate bundle: %v", err)}
	}

	if err := s.reloader.Reload(ctx); err != nil {
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to reload catalog: %v", err))
		return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to reload catalog: %v", err)}
	}

	if oldDir != newDir {
		if err := os.RemoveAll(oldDir); err != nil {
			_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to remove old bundle directory: %v", err))
			return &ValidationError{Code: http.StatusInternalServerError, Message: fmt.Sprintf("failed to remove old bundle directory: %v", err)}
		}
	}

	return nil
}

// DeleteBundle marks the bundle deleting, removes the on-disk directory, reloads the catalog,
// and then deletes the DB record.
func (s *bundleService) DeleteBundle(ctx context.Context, existing *BundleRecord) error {
	bundleDir := bundleDirPath(existing.CatalogType, existing.CatalogID, existing.Version)

	if err := s.repo.MarkDeleting(ctx, existing.ID); err != nil {
		return fmt.Errorf("failed to mark bundle deleting: %w", err)
	}

	if err := os.RemoveAll(bundleDir); err != nil {
		_ = s.repo.MarkFailed(ctx, existing.ID, fmt.Sprintf("failed to remove bundle directory: %v", err))
		return fmt.Errorf("failed to remove bundle directory: %w", err)
	}

	if err := s.reloader.Reload(ctx); err != nil {
		_ = s.repo.MarkFailed(ctx, existing.ID, fmt.Sprintf("failed to reload catalog: %v", err))
		return fmt.Errorf("failed to reload catalog: %w", err)
	}

	if err := s.repo.Delete(ctx, existing.ID); err != nil {
		_ = s.repo.MarkFailed(ctx, existing.ID, fmt.Sprintf("failed to delete bundle record: %v", err))
		return fmt.Errorf("failed to delete bundle record: %w", err)
	}

	return nil
}

// ---- Archive helpers --------------------------------------------------------

// bundleDirPath returns the on-disk directory for a bundle.
// Layout: <bundleStorageRoot>/<catalogType>/<catalogID>-<version>
// e.g.   /data/catalog-bundles/service/mayuka-service-1.0.0
//
//	/data/catalog-bundles/component/llm--my-provider-1.0.0
//
// catalogType is the singular DB value ("service", "component").
// catalogID is the DB catalog_id value (e.g. "my-service", "llm--my-provider").
// version is the semantic version string (e.g. "1.0.0").
func bundleDirPath(catalogType, catalogID, version string) string {
	return filepath.Join(bundleStorageRoot, catalogType, catalogID+"-"+version)
}

// peekMetadata reads id (catalog_id), type (catalog_type), and version from
// <topDir>/metadata.yaml inside the archive without extracting to disk.
// topDir is inferred from the first entry so archives without explicit directory
// entries (some tar implementations) are handled correctly.
// Returns a *ServiceMetadata or *ComponentMetadata (both implement BundleMetadata).
func peekMetadata(archiveBytes []byte) (BundleMetadata, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(archiveBytes))
	if err != nil {
		return nil, fmt.Errorf("invalid gzip archive: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	topDir := ""

	// flatMetadata buffers the content of a root-level "metadata.yaml" encountered
	// before any subdirectory is seen. Used when the archive is flat (no top-level dir).
	var flatMetadata []byte

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("corrupt archive: %w", err)
		}

		// Normalise: strip leading "./" so all paths are "dir/file" shaped.
		name := strings.TrimPrefix(hdr.Name, "./")

		// Path-traversal guard.
		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}

		// Infer topDir from the first entry that is actually inside a directory
		// (contains a "/"). Root-level loose files — OS artifacts or otherwise —
		// are ignored for this purpose.
		if topDir == "" && strings.Contains(name, "/") {
			topDir = strings.SplitN(name, "/", 2)[0]
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Flat archive: metadata.yaml sits at the root with no directory prefix.
		if topDir == "" && name == "metadata.yaml" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("failed to read metadata.yaml: %w", err)
			}
			flatMetadata = data
			continue
		}

		// Wrapped archive: metadata.yaml is inside the top-level directory.
		if topDir != "" && name == topDir+"/metadata.yaml" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("failed to read metadata.yaml: %w", err)
			}

			return parseMetadataYAML(data, topDir)
		}
	}

	// Flat archive with no top-level directory: use the buffered root metadata.yaml.
	if flatMetadata != nil {
		return parseMetadataYAML(flatMetadata, "")
	}

	return nil, fmt.Errorf("metadata.yaml not found in archive (expected at <catalog_id>/metadata.yaml or at the archive root)")
}

// validComponentTypes lists the accepted component_type values for component bundles.
var validComponentTypes = map[string]bool{
	"llm":       true,
	"embedding": true,
	"reranker":  true,
	"vector_db": true,
}

// parseMetadataYAML extracts id, type, name, version (and component_type for components)
// from a metadata.yaml byte slice. Uses a lightweight line scan — the fields are always
// simple scalar values.
// topDir is accepted for signature compatibility with peekMetadata but is never
// validated against — the archive top-level directory name is irrelevant.
// Returns a *ServiceMetadata or *ComponentMetadata (both implement BundleMetadata).
func parseMetadataYAML(data []byte, _ string) (BundleMetadata, error) {
	var (
		id            string
		catalogType   string
		componentType string
		version       string
		displayName   string
	)

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)

		if val, ok := scalarField(line, "id"); ok {
			id = val
		} else if val, ok := scalarField(line, "type"); ok {
			catalogType = val
		} else if val, ok := scalarField(line, "component_type"); ok {
			componentType = val
		} else if val, ok := scalarField(line, "version"); ok {
			version = val
		} else if val, ok := scalarField(line, "name"); ok {
			displayName = val
		}
	}

	if id == "" {
		return nil, fmt.Errorf("id is missing from metadata.yaml")
	}

	if catalogType == "" {
		return nil, fmt.Errorf("type is missing from metadata.yaml")
	}

	if !IsValidCatalogType(catalogType) {
		return nil, fmt.Errorf("unsupported catalog_type %q in metadata.yaml; accepted: service, component", catalogType)
	}

	if version == "" {
		return nil, fmt.Errorf("version is missing from metadata.yaml")
	}

	switch catalogType {
	case "component":
		if componentType == "" {
			return nil, fmt.Errorf("component_type is missing from metadata.yaml (required for catalog_type=component)")
		}

		if !validComponentTypes[componentType] {
			return nil, fmt.Errorf("unsupported component_type %q in metadata.yaml; accepted: llm, embedding, reranker, vector_db", componentType)
		}

		return &ComponentMetadata{
			id:            id,
			componentType: componentType,
			version:       version,
			displayName:   displayName,
		}, nil
	default: // "service"
		return &ServiceMetadata{
			id:          id,
			version:     version,
			displayName: displayName,
		}, nil
	}
}

// scalarField parses a "key: value" YAML line and returns (trimmed-value, true) on match.
func scalarField(line, key string) (string, bool) {
	prefix := key + ":"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}

	val := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	val = strings.Trim(val, `"'`)

	if val == "" {
		return "", false
	}

	return val, true
}

// extractAndMeasure extracts the archive to destDir and returns the total uncompressed size in bytes.
// Supports both wrapped archives (single top-level directory) and flat archives (files at root).
// For wrapped archives the top-level directory is stripped so files land directly in destDir.
// The archive top-level directory name is never validated — identity comes from metadata.yaml alone.
func extractAndMeasure(r io.Reader, destDir string) (int64, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return 0, &ValidationError{Code: http.StatusBadRequest, Message: "invalid gzip archive"}
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	// topDir is set on the first entry that contains "/". Empty means flat archive.
	topDir := ""
	var totalBytes int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return 0, &ValidationError{Code: http.StatusBadRequest, Message: fmt.Sprintf("corrupt archive: %v", err)}
		}

		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")

		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return 0, &ValidationError{Code: http.StatusBadRequest, Message: fmt.Sprintf("unsafe path in archive: %q", hdr.Name)}
		}

		// Infer topDir from the first entry that is inside a directory.
		// Root-level loose files (macOS ._*, .DS_Store) are ignored.
		if topDir == "" && strings.Contains(name, "/") {
			topDir = strings.SplitN(name, "/", 2)[0]
		}

		// Determine the path to write inside destDir.
		// Wrapped archive: strip the top-level directory prefix.
		// Flat archive (topDir == ""): use the entry name as-is.
		var strippedName string
		if topDir != "" {
			parts := strings.SplitN(name, "/", 2)
			if len(parts) < 2 || parts[1] == "" {
				// Top-level directory entry itself — nothing to write.
				continue
			}
			strippedName = parts[1]
		} else {
			if name == "" {
				continue
			}
			strippedName = name
		}

		target := filepath.Join(destDir, filepath.Clean(strippedName))

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return 0, fmt.Errorf("mkdir %q: %w", target, err)
			}
		case tar.TypeReg:
			totalBytes += hdr.Size

			if totalBytes > maxUncompressedBytes {
				return 0, &ValidationError{
					Code:    http.StatusBadRequest,
					Message: fmt.Sprintf("archive exceeds maximum uncompressed size of %d MB", maxUncompressedBytes/1024/1024),
				}
			}

			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return 0, fmt.Errorf("mkdir for file %q: %w", target, err)
			}

			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
			if err != nil {
				return 0, fmt.Errorf("create file %q: %w", target, err)
			}

			if _, err := io.Copy(f, tr); err != nil {
				f.Close()

				return 0, fmt.Errorf("write file %q: %w", target, err)
			}

			f.Close()
		}
	}

	return totalBytes, nil
}

// validateBundleStructureFromArchive validates the bundle structure directly from the archive
// without extracting it to disk. It checks that metadata.yaml exists in the archive root.
func validateBundleStructureFromArchive(archiveBytes []byte) error {
	gr, err := gzip.NewReader(bytes.NewReader(archiveBytes))
	if err != nil {
		return fmt.Errorf("invalid gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	metadataFound := false

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		// Look for metadata.yaml at the bundle root (after top-level directory is stripped).
		// The path will be like "topdir/metadata.yaml" in wrapped archives.
		parts := strings.Split(hdr.Name, "/")
		if len(parts) >= 2 && parts[1] == "metadata.yaml" && hdr.Typeflag == tar.TypeReg {
			metadataFound = true
			break
		}
		// Also check for flat archives where metadata.yaml is at root.
		if len(parts) == 1 && parts[0] == "metadata.yaml" && hdr.Typeflag == tar.TypeReg {
			metadataFound = true
			break
		}
	}

	if !metadataFound {
		return fmt.Errorf("missing required file metadata.yaml in bundle root")
	}

	return nil
}

// ---- DB model converters ----------------------------------------------------

// toRecord converts a DB model to a service-layer BundleRecord.
func toRecord(b *models.Bundle) *BundleRecord {
	rec := &BundleRecord{
		ID:          b.ID,
		Name:        b.Name.String,
		Status:      string(b.Status),
		CatalogType: b.CatalogType,
		CatalogID:   b.CatalogID,
		Version:     b.Version,
		CreatedBy:   b.CreatedBy,
		CreatedAt:   b.CreatedAt,
	}

	if b.SizeBytes.Valid {
		v := b.SizeBytes.Int64
		rec.SizeBytes = &v
	}

	return rec
}

// toResponse converts a DB model to a BundleResponse for the HTTP layer.
func toResponse(b *models.Bundle) *BundleResponse {
	resp := &BundleResponse{
		ID:          b.ID,
		Name:        b.Name.String,
		Status:      string(b.Status),
		CreatedAt:   b.CreatedAt.UTC().Format(time.RFC3339),
		CatalogType: b.CatalogType,
		CatalogID:   b.CatalogID,
		Version:     b.Version,
		CreatedBy:   b.CreatedBy,
	}

	if b.SizeBytes.Valid {
		v := b.SizeBytes.Int64
		resp.SizeBytes = &v
	}

	if b.Error.Valid && b.Error.String != "" {
		resp.Error = &b.Error.String
	}

	return resp
}
