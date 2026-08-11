package client

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/models"
	catalogTypes "github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
	"github.com/project-ai-services/ai-services/internal/pkg/utils"
)

// API route constants for application endpoints.
const (
	applicationsRoute       = "/api/v1/applications"
	getApplicationPSRoute   = "/api/v1/applications/%s/ps"
	getApplicationRoute     = "/api/v1/applications/%s"
	svcDeployOptionsRoute   = "/api/v1/services/%s/deploy-options"
	archDeployOptionsRoute  = "/api/v1/architectures/%s/deploy-options"
	compProviderParamsRoute = "/api/v1/components/%s/providers/%s/params"
	svcParamsRoute          = "/api/v1/services/%s/params"
	archParamsRoute         = "/api/v1/architectures/%s/params"
	svcImagesRoute          = "/api/v1/services/%s/images"
	archImagesRoute         = "/api/v1/architectures/%s/images"
	svcModelsRoute          = "/api/v1/services/%s/models"
	archModelsRoute         = "/api/v1/architectures/%s/models"
	svcMDRoute              = "/api/v1/services/%s/md"
)

// HTTPError represents an HTTP error with status code.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// ApplicationClient provides methods for interacting with the applications API.
type ApplicationClient struct {
	client *Client
}

// NewApplicationClient creates a new ApplicationClient with the given server URL and token.
func NewApplicationClient() (*ApplicationClient, error) {
	client, err := New()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize client: %w", err)
	}

	return &ApplicationClient{
		client: client,
	}, nil
}

// ListApplications retrieves a paginated list of all applications for the authenticated user.
// It supports optional filters via the params argument.
//
// Example:
//
//	client := NewApplicationClient()
//	resp, err := client.ListApplications(&ListApplicationsParams{
//	    Page: 1,
//	    PageSize: 20,
//	    DeploymentType: "services",
//	    CatalogID: "rag",
//	})
func (c *ApplicationClient) ListApplications(params *ListApplicationsParams) (*catalogTypes.ApplicationListResponse, error) {
	var result catalogTypes.ApplicationListResponse
	req := c.client.HTTPClient().R().
		SetResult(&result)

	if params != nil {
		if params.Page > 0 {
			req.SetQueryParam("page", strconv.Itoa(params.Page))
		}
		if params.PageSize > 0 {
			req.SetQueryParam("page_size", strconv.Itoa(params.PageSize))
		}
		if params.DeploymentType != "" {
			req.SetQueryParam("deployment_type", params.DeploymentType)
		}
		if params.CatalogID != "" {
			req.SetQueryParam("catalog_id", params.CatalogID)
		}
	}

	resp, err := req.Get(applicationsRoute)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return &result, nil
}

// GetApplicationPS retrieves the process status and runtime information for an application.
// It returns details about pods, containers, and their health status.
func (c *ApplicationClient) GetApplicationPS(id string) (*catalogTypes.ApplicationPSResponse, error) {
	var result catalogTypes.ApplicationPSResponse
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(getApplicationPSRoute, id))
	if err != nil {
		return nil, fmt.Errorf("get application ps: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return &result, nil
}

// DeleteApplication deletes an application by its ID.
// It removes the application and all its associated resources.
// Supports optional parameters via the params argument.
//
// Example:
//
//	client := NewApplicationClient()
//	err := client.DeleteApplication("rag", &DeleteApplicationParams{
//	    KeepData: true,
//	})
func (c *ApplicationClient) DeleteApplication(id string, params *DeleteApplicationParams) error {
	req := c.client.HTTPClient().R()

	if params != nil {
		if params.KeepData {
			req.SetQueryParam("keep_data", "true")
		}
	}

	resp, err := req.Delete(fmt.Sprintf(getApplicationRoute, id))
	if err != nil {
		return fmt.Errorf("delete application: %w", err)
	}

	if resp.IsError() {
		return &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return nil
}

// GetApplication retrieves full details for a specific application by ID.
func (c *ApplicationClient) GetApplication(id string) (*catalogTypes.Application, error) {
	var result catalogTypes.Application
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(getApplicationRoute, id))
	if err != nil {
		return nil, fmt.Errorf("get application: %w", err)
	}

	if resp.IsError() {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode(),
			Message:    utils.ParseErrorResponse(resp),
		}
	}

	return &result, nil
}

// GetApplicationWithRefresh retrieves full details for a specific application by ID.
// If the server returns 401 Unauthorized, it refreshes the access token once and retries.
func (c *ApplicationClient) GetApplicationWithRefresh(id string) (*catalogTypes.Application, error) {
	result, err := c.GetApplication(id)
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized {
			if refreshErr := c.client.RefreshToken(); refreshErr != nil {
				return nil, err
			}

			return c.GetApplication(id)
		}

		return nil, err
	}

	return result, nil
}

// CreateApplication creates a new application deployment via catalog API.
// It accepts a CreateApplicationRequest with catalog ID, name, services, and components configuration.
func (c *ApplicationClient) CreateApplication(req *models.CreateApplicationRequest) (*models.CreateApplicationResponse, error) {
	var result models.CreateApplicationResponse
	resp, err := c.client.HTTPClient().R().
		SetBody(req).
		SetResult(&result).
		Post(applicationsRoute)

	if err != nil {
		return nil, fmt.Errorf("create application: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("create application: server returned HTTP %d: %s",
			resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return &result, nil
}

// GetServiceDeployOptions retrieves deploy options for a specific service.
// It returns available providers and dependency rules for the service and its components.
func (c *ApplicationClient) GetServiceDeployOptions(serviceID string) (*catalogTypes.DeployOptionsService, error) {
	var result catalogTypes.DeployOptionsService
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(svcDeployOptionsRoute, serviceID))
	if err != nil {
		return nil, fmt.Errorf("get service deploy options: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get service deploy options: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return &result, nil
}

// GetArchitectureDeployOptions retrieves deploy options for an architecture.
// It returns available providers and dependency rules for all services in the architecture.
func (c *ApplicationClient) GetArchitectureDeployOptions(architectureID string) (*catalogTypes.DeployOptionsArchitecture, error) {
	var result catalogTypes.DeployOptionsArchitecture
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(archDeployOptionsRoute, architectureID))
	if err != nil {
		return nil, fmt.Errorf("get architecture deploy options: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get architecture deploy options: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return &result, nil
}

// GetComponentProviderParams retrieves the parameter schema for a specific component provider.
func (c *ApplicationClient) GetComponentProviderParams(componentType, providerID string) (map[string]any, error) {
	var result map[string]any
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(compProviderParamsRoute, componentType, providerID))
	if err != nil {
		return nil, fmt.Errorf("get component provider params: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get component provider params: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result, nil
}

// ListArchitectures retrieves a summary list of all available architecture templates.
func (c *ApplicationClient) ListArchitectures() ([]catalogTypes.ArchitectureSummary, error) {
	var result []catalogTypes.ArchitectureSummary
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get("/api/v1/architectures")
	if err != nil {
		return nil, fmt.Errorf("list architectures: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("list architectures: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result, nil
}

// ListServices retrieves a summary list of all available service templates.
func (c *ApplicationClient) ListServices() ([]catalogTypes.ServiceSummary, error) {
	var result []catalogTypes.ServiceSummary
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get("/api/v1/services")
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("list services: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result, nil
}

// GetServiceParams retrieves the JSON schema for a service's configurable parameters.
func (c *ApplicationClient) GetServiceParams(serviceID string) (map[string]any, error) {
	var result map[string]any
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(svcParamsRoute, serviceID))
	if err != nil {
		return nil, fmt.Errorf("get service params: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get service params: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result, nil
}

// imagesResponse is the JSON envelope returned by the /images endpoints.
type imagesResponse struct {
	Images []string `json:"images"`
}

// modelsResponse is the JSON envelope returned by the /models endpoints.
type modelsResponse struct {
	Models []string `json:"models"`
}

// GetServiceImages returns the container images needed to run a service template.
func (c *ApplicationClient) GetServiceImages(serviceID string) ([]string, error) {
	var result imagesResponse
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(svcImagesRoute, serviceID))
	if err != nil {
		return nil, fmt.Errorf("get service images: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get service images: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result.Images, nil
}

// GetArchitectureImages returns the container images needed to run an architecture template.
func (c *ApplicationClient) GetArchitectureImages(archID string) ([]string, error) {
	var result imagesResponse
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(archImagesRoute, archID))
	if err != nil {
		return nil, fmt.Errorf("get architecture images: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get architecture images: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result.Images, nil
}

// GetServiceModels returns the AI model identifiers for a service template's components.
func (c *ApplicationClient) GetServiceModels(serviceID string) ([]string, error) {
	var result modelsResponse
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(svcModelsRoute, serviceID))
	if err != nil {
		return nil, fmt.Errorf("get service models: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get service models: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result.Models, nil
}

// GetArchitectureModels returns the AI model identifiers for an architecture template's components.
func (c *ApplicationClient) GetArchitectureModels(archID string) ([]string, error) {
	var result modelsResponse
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(archModelsRoute, archID))
	if err != nil {
		return nil, fmt.Errorf("get architecture models: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get architecture models: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result.Models, nil
}

// GetServiceMD returns the raw Go text/template source strings for a service's
// markdown files (e.g. "info.md", "next.md"), keyed by filename.
// The CLI re-parses and executes these locally with runtime parameters.
func (c *ApplicationClient) GetServiceMD(serviceID string) (map[string]string, error) {
	var result map[string]string
	resp, err := c.client.HTTPClient().R().
		SetResult(&result).
		Get(fmt.Sprintf(svcMDRoute, serviceID))
	if err != nil {
		return nil, fmt.Errorf("get service md: %w", err)
	}

	if resp.IsError() {
		return nil, fmt.Errorf("get service md: server returned HTTP %d: %s", resp.StatusCode(), utils.ParseErrorResponse(resp))
	}

	return result, nil
}

// Made with Bob
