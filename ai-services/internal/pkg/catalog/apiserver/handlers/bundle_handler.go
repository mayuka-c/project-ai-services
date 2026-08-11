package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/middleware"
	bundlesvc "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/bundle"
)

// maxBundleSizeBytes is the maximum allowed compressed upload size (50 MB).
const maxBundleSizeBytes = 50 * 1024 * 1024

// BundleHandler handles catalog bundle upload, replacement, deletion, and listing.
// It follows the same pattern as ApplicationHandler.
type BundleHandler struct {
	bundleService bundlesvc.BundleServiceInterface
}

// NewBundleHandler creates a new BundleHandler.
func NewBundleHandler(svc bundlesvc.BundleServiceInterface) *BundleHandler {
	return &BundleHandler{bundleService: svc}
}

// UploadBundle godoc
//
//	@Summary     Upload a new custom catalog bundle
//	@Description Accepts a .tar.gz archive for a single catalog item. catalog_id, catalog_type,
//	             and version are all read from metadata.yaml inside the archive — no extra form
//	             fields are required. Returns 409 if a bundle with the same catalog_id is already
//	             registered (use PUT to update). The archive is validated and, if valid,
//	             hot-reloaded into CatalogProvider.
//	@Tags        Catalog
//	@Accept      multipart/form-data
//	@Produce     json
//	@Security    BearerAuth
//	@Param       file     formData  file    true   ".tar.gz bundle archive (max 50 MB)"
//	@Param       dry_run  formData  string  false  "Validate only, do not apply (default: false)"
//	@Success     201  {object}  bundlesvc.BundleResponse      "Bundle created and active"
//	@Success     200  {object}  bundlesvc.ValidationResult    "dry_run=true: validation passed"
//	@Failure     400  {object}  ErrorResponse                 "Missing or unreadable file field, wrong content-type, archive too large"
//	@Failure     401  {object}  ErrorResponse                 "Unauthorized"
//	@Failure     409  {object}  ErrorResponse                 "catalog_id already registered; use PUT"
//	@Failure     422  {object}  ErrorResponse                 "Validation failed (bad metadata, missing files, etc.)"
//	@Router      /catalog/bundles [post]
func (h *BundleHandler) UploadBundle(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBundleSizeBytes)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "missing or unreadable file field"})

		return
	}
	defer file.Close()

	if !strings.HasSuffix(header.Filename, ".tar.gz") {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "file must be a .tar.gz archive"})

		return
	}

	userID := c.GetString(middleware.CtxUserIDKey)

	// Dry-run: validate-only — reads metadata + validates structure, no DB write.
	if c.PostForm("dry_run") == "true" {
		result, err := h.bundleService.ValidateBundle(c.Request.Context(), file)
		if err != nil {
			if valErr, ok := err.(*bundlesvc.ValidationError); ok {
				c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

				return
			}

			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "dry-run failed unexpectedly"})

			return
		}

		c.JSON(http.StatusOK, result)

		return
	}

	// Real upload: ProcessBundle reads metadata, checks conflict, extracts, validates,
	// inserts the DB row as active, and reloads the catalog — all in one synchronous call.
	resp, err := h.bundleService.ProcessBundle(c.Request.Context(), file, userID)
	if err != nil {
		if valErr, ok := err.(*bundlesvc.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

			return
		}

		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "bundle upload failed"})

		return
	}

	c.Header("Location", fmt.Sprintf("/api/v1/catalog/bundles/%s", resp.ID))
	c.JSON(http.StatusCreated, resp)
}

// UpdateBundle godoc
//
//	@Summary     Replace an existing catalog bundle
//	@Description Replaces the bundle identified by bundle_id. catalog_id and catalog_type are
//	             resolved from the DB record and validated as immutable against the archive's
//	             metadata.yaml — no form fields are needed beyond the file. Version is read
//	             from the archive. Returns 404 if no bundle with that bundle_id exists. The
//	             existing bundle remains active until the replacement is validated and activated.
//	@Tags        Catalog
//	@Accept      multipart/form-data
//	@Produce     json
//	@Security    BearerAuth
//	@Param       bundle_id  path      string  true   "Internal record ID of the bundle to replace"
//	@Param       file       formData  file    true   ".tar.gz replacement archive (max 50 MB)"
//	@Param       dry_run    formData  string  false  "Validate only, do not apply (default: false)"
//	@Success     202  {object}  bundlesvc.BundleResponse   "Replacement bundle accepted and processing"
//	@Success     200  {object}  bundlesvc.ValidationResult "dry_run=true: validation passed"
//	@Failure     400  {object}  ErrorResponse              "Missing file field or archive too large"
//	@Failure     401  {object}  ErrorResponse              "Unauthorized"
//	@Failure     404  {object}  ErrorResponse              "No bundle with this bundle_id"
//	@Failure     422  {object}  ErrorResponse              "Validation failed or immutable fields differ"
//	@Router      /catalog/bundles/{bundle_id} [put]
func (h *BundleHandler) UpdateBundle(c *gin.Context) {
	// 1. Enforce compressed size limit.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBundleSizeBytes)

	bundleID := c.Param("bundle_id")

	// 2. Resolve existing record — 404 if not found.
	existing, err := h.bundleService.GetByBundleID(c.Request.Context(), bundleID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to look up bundle"})

		return
	}

	if existing == nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("no bundle with id %q; use POST /api/v1/catalog/bundles to create a new one", bundleID),
		})

		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "missing or unreadable file field"})

		return
	}
	defer file.Close()

	if !strings.HasSuffix(header.Filename, ".tar.gz") {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "file must be a .tar.gz archive"})

		return
	}

	dryRun := c.PostForm("dry_run") == "true"
	userID := c.GetString(middleware.CtxUserIDKey)

	// 3. Dry-run: synchronous validate-only — existing bundle is completely untouched.
	if dryRun {
		result, err := h.bundleService.ValidateBundle(c.Request.Context(), file)
		if err != nil {
			if valErr, ok := err.(*bundlesvc.ValidationError); ok {
				c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

				return
			}

			c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "dry-run validation failed unexpectedly"})

			return
		}

		c.JSON(http.StatusOK, result)

		return
	}

	resp, err := h.bundleService.ReplaceBundle(c.Request.Context(), existing, file, userID)
	if err != nil {
		if valErr, ok := err.(*bundlesvc.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

			return
		}

		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "bundle replacement failed"})

		return
	}

	c.Header("Location", fmt.Sprintf("/api/v1/catalog/bundles/%s", resp.ID))
	c.JSON(http.StatusAccepted, resp)
}

// DeleteBundle godoc
//
//	@Summary     Delete a catalog bundle
//	@Description Removes the bundle identified by bundle_id: deletes the on-disk directory
//	             and the DB record, then reloads CatalogProvider synchronously. Existing
//	             deployed applications are not affected.
//	@Tags        Catalog
//	@Produce     json
//	@Security    BearerAuth
//	@Param       bundle_id  path  string  true  "Internal record ID of the bundle to delete"
//	@Success     204  "Bundle deleted"
//	@Failure     401  {object}  ErrorResponse  "Unauthorized"
//	@Failure     404  {object}  ErrorResponse  "No bundle with this bundle_id"
//	@Router      /catalog/bundles/{bundle_id} [delete]
func (h *BundleHandler) DeleteBundle(c *gin.Context) {
	bundleID := c.Param("bundle_id")

	existing, err := h.bundleService.GetByBundleID(c.Request.Context(), bundleID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to look up bundle"})

		return
	}

	if existing == nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("no bundle with id %q", bundleID),
		})

		return
	}

	if err := h.bundleService.DeleteBundle(c.Request.Context(), existing); err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to delete bundle"})

		return
	}

	c.Status(http.StatusNoContent)
}

// GetBundle godoc
//
//	@Summary     Get a catalog bundle by ID
//	@Description Returns the current status and metadata for a specific bundle.
//	             Poll this endpoint after POST/PUT returns 202 until status is "active" or "failed".
//	@Tags        Catalog
//	@Produce     json
//	@Security    BearerAuth
//	@Param       bundle_id  path  string  true  "Internal record ID of the bundle"
//	@Success     200  {object}  bundlesvc.BundleResponse  "Bundle record"
//	@Failure     401  {object}  ErrorResponse             "Unauthorized"
//	@Failure     404  {object}  ErrorResponse             "No bundle with this bundle_id"
//	@Router      /catalog/bundles/{bundle_id} [get]
func (h *BundleHandler) GetBundle(c *gin.Context) {
	bundleID := c.Param("bundle_id")

	resp, err := h.bundleService.GetBundleByID(c.Request.Context(), bundleID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve bundle"})

		return
	}

	if resp == nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("no bundle with id %q", bundleID),
		})

		return
	}

	c.JSON(http.StatusOK, resp)
}

// ListBundles godoc
//
//	@Summary     List all catalog bundles
//	@Description Returns all uploaded bundles ordered by upload time (newest first).
//	@Tags        Catalog
//	@Produce     json
//	@Security    BearerAuth
//	@Success     200  {object}  bundlesvc.BundleListResponse  "List of bundles"
//	@Failure     401  {object}  ErrorResponse                 "Unauthorized"
//	@Router      /catalog/bundles [get]
func (h *BundleHandler) ListBundles(c *gin.Context) {
	resp, err := h.bundleService.ListBundles(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to list bundles"})

		return
	}

	c.JSON(http.StatusOK, resp)
}

