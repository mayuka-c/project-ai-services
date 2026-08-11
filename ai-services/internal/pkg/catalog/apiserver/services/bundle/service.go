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

	// idPrefix is the prefix used for all bundle IDs.
	idPrefix = "bnd_"
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

// ListBundles returns all bundle records ordered by uploaded_at descending.
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
func (s *bundleService) ValidateBundle(_ context.Context, file io.Reader) (*ValidationResult, error) {
	archiveBytes, err := io.ReadAll(file)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusBadRequest, Message: "failed to read archive"}
	}

	meta, err := peekMetadata(archiveBytes)
	if err != nil {
		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	tmpDir, err := os.MkdirTemp("", "bundle-validate-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	defer os.RemoveAll(tmpDir)

	if _, err := extractAndMeasure(bytes.NewReader(archiveBytes), tmpDir, meta.CatalogID); err != nil {
		var valErr *ValidationError
		if errors.As(err, &valErr) {
			return nil, valErr
		}

		return nil, &ValidationError{Code: http.StatusBadRequest, Message: fmt.Sprintf("failed to extract archive: %v", err)}
	}

	if err := validateBundleStructure(tmpDir, meta.CatalogID, meta.CatalogType); err != nil {
		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	return &ValidationResult{
		Valid:       true,
		CatalogID:   meta.CatalogID,
		CatalogType: meta.CatalogType,
		Version:     meta.Version,
	}, nil
}

// ProcessBundle handles a POST upload — fully synchronous.
//
// Steps:
//  1. Read archive bytes.
//  2. Peek metadata.yaml → id, type, version.
//  3. Conflict check: reject 409 if an active bundle with catalog_id already exists.
//  4. Extract to the permanent bundle directory and measure uncompressed size.
//  5. Validate bundle structure.
//  6. Insert DB row with status = active and the measured size.
//  7. Trigger catalog reload.
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

	// Step 3: conflict check.
	exists, err := s.repo.ActiveCatalogIDExists(ctx, meta.CatalogID)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing bundles: %w", err)
	}

	if exists {
		return nil, &ValidationError{
			Code:    http.StatusConflict,
			Message: fmt.Sprintf("a bundle with catalog_id %q already exists; use PUT /api/v1/catalog/bundles/:bundle_id to update it", meta.CatalogID),
		}
	}

	// Step 4: extract to the permanent directory and measure size.
	// Compute name first — it is both the DB Name field and the on-disk directory name.
	name := fmt.Sprintf("%s-%s", meta.CatalogID, meta.Version)
	destDir := bundleDirPath(meta.CatalogType, name)

	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create bundle directory: %w", err)
	}

	sizeBytes, err := extractAndMeasure(bytes.NewReader(archiveBytes), destDir, meta.CatalogID)
	if err != nil {
		os.RemoveAll(destDir)

		var valErr *ValidationError
		if errors.As(err, &valErr) {
			return nil, valErr
		}

		return nil, fmt.Errorf("extraction failed: %w", err)
	}

	// Step 5: validate bundle structure.
	if err := validateBundleStructure(destDir, meta.CatalogID, meta.CatalogType); err != nil {
		os.RemoveAll(destDir)

		return nil, &ValidationError{Code: http.StatusUnprocessableEntity, Message: err.Error()}
	}

	// Step 6: insert DB row as active immediately.
	bundleID := generateBundleID()

	bundle := &models.Bundle{
		ID:          bundleID,
		Name:        name,
		CatalogType: meta.CatalogType,
		CatalogID:   meta.CatalogID,
		Version:     meta.Version,
		UploadedBy:  userID,
	}

	if err := s.repo.Insert(ctx, bundle); err != nil {
		os.RemoveAll(destDir)

		return nil, fmt.Errorf("failed to record bundle: %w", err)
	}

	if err := s.repo.MarkActive(ctx, bundleID, sizeBytes); err != nil {
		// Best-effort cleanup: remove the directory and the DB row.
		os.RemoveAll(destDir)
		_ = s.repo.Delete(ctx, bundleID)

		return nil, fmt.Errorf("failed to activate bundle: %w", err)
	}

	// Step 7: reload catalog so the new service is immediately available.
	_ = s.reloader.Reload(ctx)

	// Re-fetch from DB so the response reflects the final persisted state
	// (status=active, size_bytes, uploaded_at) rather than the stale local struct.
	persisted, err := s.repo.GetByID(ctx, bundleID)
	if err != nil || persisted == nil {
		return toResponse(bundle), nil
	}

	return toResponse(persisted), nil
}

// ReplaceBundle handles a PUT update — runs asynchronously so the existing bundle
// continues serving while the replacement is extracted and validated.
//
// Steps:
//  1. Read archive bytes and peek metadata.yaml.
//  2. Validate that catalog_id and catalog_type are unchanged (immutable fields).
//  3. Insert a new DB row with status = processing (so clients can poll).
//  4. Spawn a goroutine: extract new bundle, validate, mark active, delete old dir, reload.
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
	if meta.CatalogID != existing.CatalogID {
		return nil, &ValidationError{
			Code:    http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("archive catalog_id %q does not match existing record %q; this field is immutable", meta.CatalogID, existing.CatalogID),
		}
	}

	if meta.CatalogType != existing.CatalogType {
		return nil, &ValidationError{
			Code:    http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("archive catalog_type %q does not match existing record %q; this field is immutable", meta.CatalogType, existing.CatalogType),
		}
	}

	// Insert a processing row so the client can poll while the goroutine runs.
	// Name and version are blank until extraction completes.
	bundleID := generateBundleID()

	bundle := &models.Bundle{
		ID:          bundleID,
		Name:        "",
		CatalogType: existing.CatalogType,
		CatalogID:   existing.CatalogID,
		Version:     "",
		UploadedBy:  userID,
	}

	if err := s.repo.Insert(ctx, bundle); err != nil {
		return nil, fmt.Errorf("failed to record replacement bundle: %w", err)
	}

	go s.runReplaceAsync(bundleID, existing, meta, archiveBytes)

	return toResponse(bundle), nil
}

// runReplaceAsync extracts and activates a replacement bundle in a goroutine.
func (s *bundleService) runReplaceAsync(bundleID string, existing *BundleRecord, meta *BundleMetadata, archiveBytes []byte) {
	ctx := context.Background()

	newName := fmt.Sprintf("%s-%s", meta.CatalogID, meta.Version)
	destDir := bundleDirPath(existing.CatalogType, newName)

	if err := os.MkdirAll(destDir, 0o750); err != nil {
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to create bundle directory: %v", err))

		return
	}

	sizeBytes, err := extractAndMeasure(bytes.NewReader(archiveBytes), destDir, meta.CatalogID)
	if err != nil {
		os.RemoveAll(destDir)

		var valErr *ValidationError
		if errors.As(err, &valErr) {
			_ = s.repo.MarkFailed(ctx, bundleID, valErr.Message)
		} else {
			_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("extraction failed: %v", err))
		}

		return
	}

	if err := validateBundleStructure(destDir, meta.CatalogID, meta.CatalogType); err != nil {
		os.RemoveAll(destDir)
		_ = s.repo.MarkFailed(ctx, bundleID, err.Error())

		return
	}

	if err := s.repo.UpdateVersionAndName(ctx, bundleID, meta.Version, newName); err != nil {
		os.RemoveAll(destDir)
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to update bundle record: %v", err))

		return
	}

	if err := s.repo.MarkActive(ctx, bundleID, sizeBytes); err != nil {
		os.RemoveAll(destDir)
		_ = s.repo.MarkFailed(ctx, bundleID, fmt.Sprintf("failed to activate bundle: %v", err))

		return
	}

	// Remove the old on-disk directory only after the new bundle is active.
	if existing.Name != "" {
		os.RemoveAll(bundleDirPath(existing.CatalogType, existing.Name))
	}

	_ = s.reloader.Reload(context.Background())
}

// DeleteBundle removes the on-disk directory, deletes the DB record, and triggers a reload.
func (s *bundleService) DeleteBundle(ctx context.Context, existing *BundleRecord) error {
	bundleDir := bundleDirPath(existing.CatalogType, existing.Name)

	if err := os.RemoveAll(bundleDir); err != nil {
		return fmt.Errorf("failed to remove bundle directory: %w", err)
	}

	if err := s.repo.Delete(ctx, existing.ID); err != nil {
		return fmt.Errorf("failed to delete bundle record: %w", err)
	}

	if err := s.reloader.Reload(ctx); err != nil {
		return fmt.Errorf("failed to reload catalog: %w", err)
	}

	return nil
}

// ---- Archive helpers --------------------------------------------------------

// bundleDirPath returns the on-disk directory for a bundle.
// Layout: <bundleStorageRoot>/<catalogType>/<name>
// e.g.   /data/catalog-bundles/service/mayuka-service-1.0.0
//
// catalogType is the singular DB value ("service", "component") and is used
// verbatim as the subdirectory name.
// name is always the DB Name field (<catalogID>-<version>).
func bundleDirPath(catalogType, name string) string {
	return filepath.Join(bundleStorageRoot, catalogType, name)
}

// generateBundleID creates a time-sortable unique ID for a bundle.
func generateBundleID() string {
	return fmt.Sprintf("%s%x", idPrefix, time.Now().UnixNano())
}

// peekMetadata reads id (catalog_id), type (catalog_type), and version from
// <topDir>/metadata.yaml inside the archive without extracting to disk.
// topDir is inferred from the first entry so archives without explicit directory
// entries (some tar implementations) are handled correctly.
func peekMetadata(archiveBytes []byte) (*BundleMetadata, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(archiveBytes))
	if err != nil {
		return nil, fmt.Errorf("invalid gzip archive: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	topDir := ""

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

		if hdr.Typeflag == tar.TypeReg && name == topDir+"/metadata.yaml" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("failed to read metadata.yaml: %w", err)
			}

			return parseMetadataYAML(data, topDir)
		}
	}

	return nil, fmt.Errorf("metadata.yaml not found in archive (expected at <catalog_id>/metadata.yaml)")
}

// parseMetadataYAML extracts id, type, and version from a metadata.yaml byte slice.
// Uses a lightweight line scan — the three fields are always simple scalar values.
func parseMetadataYAML(data []byte, topDir string) (*BundleMetadata, error) {
	meta := &BundleMetadata{}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)

		if val, ok := scalarField(line, "id"); ok {
			meta.CatalogID = val
		} else if val, ok := scalarField(line, "type"); ok {
			meta.CatalogType = val
		} else if val, ok := scalarField(line, "version"); ok {
			meta.Version = val
		}
	}

	// catalog_id: fall back to the top-level directory name when absent.
	if meta.CatalogID == "" {
		meta.CatalogID = topDir
	}

	if meta.CatalogID != topDir {
		return nil, fmt.Errorf("metadata.yaml catalog_id %q does not match archive top-level directory %q", meta.CatalogID, topDir)
	}

	if meta.CatalogType == "" {
		return nil, fmt.Errorf("type is missing from metadata.yaml")
	}

	if !IsValidCatalogType(meta.CatalogType) {
		return nil, fmt.Errorf("unsupported catalog_type %q in metadata.yaml; accepted: service, component", meta.CatalogType)
	}

	if meta.Version == "" {
		return nil, fmt.Errorf("version is missing from metadata.yaml")
	}

	return meta, nil
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
// Enforces path-traversal guards and verifies the top-level directory matches catalogID.
func extractAndMeasure(r io.Reader, destDir, catalogID string) (int64, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return 0, &ValidationError{Code: http.StatusBadRequest, Message: "invalid gzip archive"}
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	topLevelVerified := false
	var totalBytes int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return 0, &ValidationError{Code: http.StatusBadRequest, Message: fmt.Sprintf("corrupt archive: %v", err)}
		}

		if filepath.IsAbs(hdr.Name) || strings.Contains(hdr.Name, "..") {
			return 0, &ValidationError{Code: http.StatusBadRequest, Message: fmt.Sprintf("unsafe path in archive: %q", hdr.Name)}
		}

		if !topLevelVerified && hdr.Typeflag == tar.TypeDir {
			topDir := strings.TrimSuffix(strings.SplitN(hdr.Name, "/", 2)[0], "/")
			if topDir != catalogID {
				return 0, &ValidationError{
					Code:    http.StatusBadRequest,
					Message: fmt.Sprintf("archive top-level directory %q does not match catalog_id %q", topDir, catalogID),
				}
			}

			topLevelVerified = true
		}

		// Strip the top-level directory from the entry path so files land
		// directly in destDir rather than destDir/<catalogID>/...
		// e.g. "mayuka-service/podman/values.yaml" → "podman/values.yaml"
		strippedName := strings.SplitN(filepath.ToSlash(hdr.Name), "/", 2)[1]
		if strippedName == "" {
			// The entry is the top-level directory itself — nothing to write.
			continue
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

// validateBundleStructure checks that the extracted bundle directory contains the
// minimum required file: metadata.yaml directly at <destDir>/metadata.yaml.
func validateBundleStructure(destDir, _, _ string) error {
	metaPath := filepath.Join(destDir, "metadata.yaml")
	if _, err := os.Stat(metaPath); err != nil {
		return fmt.Errorf("missing required file metadata.yaml in bundle root")
	}

	return nil
}

// ---- DB model converters ----------------------------------------------------

// toRecord converts a DB model to a service-layer BundleRecord.
func toRecord(b *models.Bundle) *BundleRecord {
	rec := &BundleRecord{
		ID:          b.ID,
		Name:        b.Name,
		Status:      string(b.Status),
		CatalogType: b.CatalogType,
		CatalogID:   b.CatalogID,
		Version:     b.Version,
		UploadedBy:  b.UploadedBy,
		UploadedAt:  b.UploadedAt,
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
		Name:        b.Name,
		Status:      string(b.Status),
		UploadedAt:  b.UploadedAt.UTC().Format(time.RFC3339),
		CatalogType: b.CatalogType,
		CatalogID:   b.CatalogID,
		Version:     b.Version,
		UploadedBy:  b.UploadedBy,
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
