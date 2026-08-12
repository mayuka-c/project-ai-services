package client

import (
	"context"
	"fmt"
	"os"
	"time"

	bundlesvc "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/bundle"
	"github.com/project-ai-services/ai-services/internal/pkg/utils"
)

// API route constants for catalog bundle endpoints.
const (
	catalogBundlesRoute          = "/api/v1/catalog/bundles"
	catalogBundleByIDRoute       = "/api/v1/catalog/bundles/%s"
	catalogBundleValidateRoute   = "/api/v1/catalog/bundles/validate"
)

// ListBundles retrieves all catalog bundles registered on the server.
func (c *ApplicationClient) ListBundles() (*bundlesvc.BundleListResponse, error) {
	var result bundlesvc.BundleListResponse
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(catalogBundlesRoute)
	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return &result, nil
}

// GetBundle retrieves a single catalog bundle by its ID.
func (c *ApplicationClient) GetBundle(id string) (*bundlesvc.BundleResponse, error) {
	var result bundlesvc.BundleResponse
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(catalogBundleByIDRoute, id))
	if err != nil {
		return nil, fmt.Errorf("get bundle: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return &result, nil
}

// UploadBundle POSTs a .tar.gz archive as multipart/form-data to create a new bundle.
// Returns the 201 BundleResponse (status is always "active" on success — the POST endpoint
// is fully synchronous). catalog_id, catalog_type, and version are read from metadata.yaml
// inside the archive; no additional flags are required.
func (c *ApplicationClient) UploadBundle(filePath string) (*bundlesvc.BundleResponse, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open bundle file: %w", err)
	}
	defer f.Close()

	var result bundlesvc.BundleResponse
	resp, err := c.client.HTTPClient().R().
		SetFileReader("file", filePath, f).
		SetResult(&result).
		Post(catalogBundlesRoute)
	if err != nil {
		return nil, fmt.Errorf("upload bundle: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return &result, nil
}

// UpdateBundle PUTs a replacement .tar.gz archive for the bundle identified by bundleID.
// The server validates that catalog_id and catalog_type are unchanged (immutable).
// Returns the 202 BundleResponse immediately — the server processes the replacement
// asynchronously. Use PollBundleActive to wait for status == "active".
func (c *ApplicationClient) UpdateBundle(bundleID, filePath string) (*bundlesvc.BundleResponse, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open bundle file: %w", err)
	}
	defer f.Close()

	var result bundlesvc.BundleResponse
	resp, err := c.client.HTTPClient().R().
		SetFileReader("file", filePath, f).
		SetResult(&result).
		Put(fmt.Sprintf(catalogBundleByIDRoute, bundleID))
	if err != nil {
		return nil, fmt.Errorf("update bundle: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return &result, nil
}

// DeleteBundle sends DELETE /api/v1/catalog/bundles/:bundleID.
// Removes the bundle's on-disk directory and DB record then reloads CatalogProvider.
func (c *ApplicationClient) DeleteBundle(bundleID string) error {
	resp, err := c.client.HTTPClient().R().
		Delete(fmt.Sprintf(catalogBundleByIDRoute, bundleID))
	if err != nil {
		return fmt.Errorf("delete bundle: %w", err)
	}

	if resp.IsError() {
		return &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return nil
}

// ValidateBundle POSTs a .tar.gz archive to the validate endpoint.
// No DB record is written and CatalogProvider is not reloaded.
// Returns the raw BundleResponse-shaped JSON decoded into a generic map so the caller
// can inspect catalog_type and cast to the appropriate concrete type if needed.
// On a 422 response the error is wrapped as *HTTPError with the server's message.
func (c *ApplicationClient) ValidateBundle(filePath string) (bundlesvc.ValidationResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open bundle file: %w", err)
	}
	defer f.Close()

	// The validate endpoint returns either a ServiceValidationResult or
	// ComponentValidationResult JSON body. We decode into ComponentValidationResult
	// (a superset: it includes the component_type field which is empty for services)
	// and then downcast based on catalog_type.
	var raw bundlesvc.ComponentValidationResult
	resp, err := c.client.HTTPClient().R().
		SetFileReader("file", filePath, f).
		SetResult(&raw).
		Post(catalogBundleValidateRoute)
	if err != nil {
		return nil, fmt.Errorf("validate bundle: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	// Return the typed concrete result based on catalog_type.
	if raw.CatalogType == "component" {
		return &raw, nil
	}

	return &bundlesvc.ServiceValidationResult{
		Valid:       raw.Valid,
		CatalogType: raw.CatalogType,
		CatalogID:   raw.CatalogID,
		Version:     raw.Version,
		Name:        raw.Name,
		DirName:     raw.DirName,
	}, nil
}

// PollBundleActive polls GET /api/v1/catalog/bundles/:bundleID at the given interval
// until status equals "active" or the context is cancelled.
// Used after a 202 Accepted PUT response to wait for the async replacement to complete.
func (c *ApplicationClient) PollBundleActive(ctx context.Context, bundleID string, interval time.Duration) (*bundlesvc.BundleResponse, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("poll bundle active: %w", ctx.Err())
		case <-time.After(interval):
			bundle, err := c.GetBundle(bundleID)
			if err != nil {
				return nil, fmt.Errorf("poll bundle active: %w", err)
			}

			if bundle.Status == "active" {
				return bundle, nil
			}

			if bundle.Status == "failed" {
				errMsg := ""
				if bundle.Error != nil {
					errMsg = *bundle.Error
				}

				return nil, fmt.Errorf("bundle %s failed: %s", bundleID, errMsg)
			}
		}
	}
}
