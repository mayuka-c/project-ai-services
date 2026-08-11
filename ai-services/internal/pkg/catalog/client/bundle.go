package client

import (
	"fmt"

	bundlesvc "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/bundle"
	"github.com/project-ai-services/ai-services/internal/pkg/utils"
)

// API route constants for catalog bundle endpoints.
const (
	catalogBundlesRoute    = "/api/v1/catalog/bundles"
	catalogBundleByIDRoute = "/api/v1/catalog/bundles/%s"
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

