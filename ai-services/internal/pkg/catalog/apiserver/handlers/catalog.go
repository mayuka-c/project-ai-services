package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog"
	catalogpkg "github.com/project-ai-services/ai-services/internal/pkg/catalog"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
	"github.com/project-ai-services/ai-services/internal/pkg/vars"
)

// CatalogHandler handles catalog-related HTTP requests.
type CatalogHandler struct {
	provider *catalogpkg.CatalogProvider
}

// NewCatalogHandler creates a new catalog handler with the given provider.
func NewCatalogHandler(provider *catalogpkg.CatalogProvider) *CatalogHandler {
	return &CatalogHandler{provider: provider}
}

// ListArchitectures godoc
//
//	@Summary		List available architectures
//	@Description	Retrieves a list of all available architecture templates with summary information
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{array}		types.ArchitectureSummary
//	@Failure		401	{object}	ErrorResponse	"Unauthorized - Invalid or missing access token"
//	@Failure		500	{object}	ErrorResponse	"Internal Server Error"
//	@Router			/architectures [get]
func (h *CatalogHandler) ListArchitectures(c *gin.Context) {
	architectures, err := h.provider.ListArchitectures()
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: fmt.Sprintf("Failed to list architectures: %v", err),
		})

		return
	}

	// Convert to summaries
	summaries := make([]types.ArchitectureSummary, len(architectures))
	for i, arch := range architectures {
		summaries[i] = catalog.ToArchitectureSummary(&arch)
	}

	c.JSON(http.StatusOK, summaries)
}

// GetArchitectureDetails godoc
//
//	@Summary		Get architecture details
//	@Description	Retrieves detailed information about a specific architecture template
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Architecture template ID (e.g., 'rag')"
//	@Success		200	{object}	types.Architecture
//	@Failure		401	{object}	ErrorResponse	"Unauthorized - Invalid or missing access token"
//	@Failure		404	{object}	ErrorResponse	"Architecture not found"
//	@Failure		500	{object}	ErrorResponse	"Internal Server Error"
//	@Router			/architectures/{id} [get]
func (h *CatalogHandler) GetArchitectureDetails(c *gin.Context) {
	id := c.Param("id")

	architecture, err := h.provider.LoadArchitecture(id)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Architecture '%s' not found: %v", id, err),
		})

		return
	}

	c.JSON(http.StatusOK, architecture)
}

// ListServices godoc
//
//	@Summary		List available services
//	@Description	Retrieves a list of all deployable service templates. Dependency-only services are excluded from this list. Returns service summaries including standalone flag without endpoints and pod templates.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{array}		types.ServiceSummary
//	@Failure		401	{object}	ErrorResponse	"Unauthorized - Invalid or missing access token"
//	@Failure		500	{object}	ErrorResponse	"Internal Server Error"
//	@Router			/services [get]
func (h *CatalogHandler) ListServices(c *gin.Context) {
	// Get runtime from global factory
	runtime := vars.RuntimeFactory.GetRuntimeType()

	servicesList, err := h.provider.ListServicesWithRuntime(runtime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: fmt.Sprintf("Failed to list services: %v", err),
		})

		return
	}

	// Convert to summaries (exclude endpoints and pod_templates)
	summaries := make([]types.ServiceSummary, len(servicesList))
	for i, svc := range servicesList {
		summaries[i] = catalogpkg.ToServiceSummary(&svc)
	}

	c.JSON(http.StatusOK, summaries)
}

// GetServiceDetails godoc
//
//	@Summary		Get service details
//	@Description	Retrieves detailed information about a specific service template
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Service template ID (e.g., 'summarize')"
//	@Success		200	{object}	types.Service
//	@Failure		401	{object}	ErrorResponse	"Unauthorized - Invalid or missing access token"
//	@Failure		404	{object}	ErrorResponse	"Service not found"
//	@Failure		500	{object}	ErrorResponse	"Internal Server Error"
//	@Router			/services/{id} [get]
func (h *CatalogHandler) GetServiceDetails(c *gin.Context) {
	id := c.Param("id")

	service, err := h.provider.LoadService(id)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Service '%s' not found: %v", id, err),
		})

		return
	}

	c.JSON(http.StatusOK, service)
}

// GetArchitectureDeployOptions godoc
//
//	@Summary		Get architecture deploy options
//	@Description	Retrieves available providers and dependency rules for all services and their components within an architecture
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Architecture ID (e.g., 'rag')"
//	@Success		200	{object}	types.DeployOptionsArchitecture
//	@Failure		401	{object}	ErrorResponse	"Unauthorized - Invalid or missing access token"
//	@Failure		404	{object}	ErrorResponse	"Architecture not found"
//	@Failure		500	{object}	ErrorResponse	"Internal Server Error"
//	@Router			/architectures/{id}/deploy-options [get]
func (h *CatalogHandler) GetArchitectureDeployOptions(c *gin.Context) {
	architectureID := c.Param("id")

	deployOptions, err := h.provider.GetArchitectureDeployOptions(c.Request.Context(), architectureID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get deploy options for architecture '%s': %v", architectureID, err),
		})

		return
	}

	c.JSON(http.StatusOK, deployOptions)
}

// GetServiceDeployOptions godoc
//
//	@Summary		Get service deploy options
//	@Description	Retrieves available providers and dependency rules for a specific service
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Service ID (e.g., 'digitize', 'chat')"
//	@Success		200	{object}	types.DeployOptionsService
//	@Failure		401	{object}	ErrorResponse	"Unauthorized - Invalid or missing access token"
//	@Failure		404	{object}	ErrorResponse	"Service not found"
//	@Failure		500	{object}	ErrorResponse	"Internal Server Error"
//	@Router			/services/{id}/deploy-options [get]
func (h *CatalogHandler) GetServiceDeployOptions(c *gin.Context) {
	serviceID := c.Param("id")

	deployOptions, err := h.provider.GetServiceDeployOptions(c.Request.Context(), serviceID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get deploy options for service '%s': %v", serviceID, err),
		})

		return
	}

	c.JSON(http.StatusOK, deployOptions)
}

// GetComponentProviderParams godoc
//
//	@Summary		Get component provider parameters
//	@Description	Retrieves the configuration schema (JSON Schema) for a specific provider within a component type. Returns a JSON Schema object with properties that may include x-data-id for fields that should be populated from metadata specifications.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			component_type	path		string					true	"Component type (e.g., 'vector_db', 'llm', 'embedding', 'reranker')"
//	@Param			provider_id		path		string					true	"Provider identifier (e.g., 'opensearch', 'vllm', 'watsonx')"
//	@Success		200				{object}	map[string]interface{}	"JSON Schema object with $schema, type, and properties. Properties may include x-data-id field indicating data should be populated from metadata specifications (e.g., supported_models)"
//	@Failure		400				{object}	ErrorResponse			"Bad Request - Invalid component_type or provider_id"
//	@Failure		401				{object}	ErrorResponse			"Unauthorized - Invalid or missing access token"
//	@Failure		404				{object}	ErrorResponse			"Component type or provider not found"
//	@Failure		500				{object}	ErrorResponse			"Internal Server Error"
//	@Router			/components/{component_type}/providers/{provider_id}/params [get]
func (h *CatalogHandler) GetComponentProviderParams(c *gin.Context) {
	componentType := c.Param("component_type")
	providerID := c.Param("provider_id")

	schema, err := h.provider.GetComponentProviderParams(c.Request.Context(), componentType, providerID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get parameters for provider '%s/%s': %v", componentType, providerID, err),
		})

		return
	}

	c.JSON(http.StatusOK, schema)
}

// GetServiceParams godoc
//
//	@Summary		Get service parameters
//	@Description	Retrieves the configuration schema (JSON Schema) for a specific service. Returns a JSON Schema object with properties that define the service's configurable parameters.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string					true	"Service ID (e.g., 'chat', 'digitize', 'similarity')"
//	@Success		200	{object}	map[string]interface{}	"JSON Schema object with $schema, type, and properties defining service parameters"
//	@Failure		400	{object}	ErrorResponse			"Bad Request - Invalid service ID"
//	@Failure		401	{object}	ErrorResponse			"Unauthorized - Invalid or missing access token"
//	@Failure		404	{object}	ErrorResponse			"Service not found"
//	@Failure		500	{object}	ErrorResponse			"Internal Server Error"
//	@Router			/services/{id}/params [get]
func (h *CatalogHandler) GetServiceParams(c *gin.Context) {
	serviceID := c.Param("id")

	schema, err := h.provider.GetServiceParams(c.Request.Context(), serviceID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get parameters for service '%s': %v", serviceID, err),
		})

		return
	}

	c.JSON(http.StatusOK, schema)
}

// GetServiceImages godoc
//
//	@Summary		Get container images for a service
//	@Description	Returns all container images required to run a specific service template and its component dependencies. Includes the catalog infrastructure image.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string		true	"Service ID"
//	@Success		200	{object}	map[string][]string	"images: list of fully-qualified image references"
//	@Failure		401	{object}	ErrorResponse
//	@Failure		404	{object}	ErrorResponse
//	@Router			/services/{id}/images [get]
func (h *CatalogHandler) GetServiceImages(c *gin.Context) {
	serviceID := c.Param("id")

	images, err := h.provider.GetCatalogImages(c.Request.Context(), serviceID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get images for service '%s': %v", serviceID, err),
		})

		return
	}

	c.JSON(http.StatusOK, gin.H{"images": images})
}

// GetArchitectureImages godoc
//
//	@Summary		Get container images for an architecture
//	@Description	Returns all container images required to run all services in an architecture template, including component dependencies and catalog infrastructure.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string		true	"Architecture ID"
//	@Success		200	{object}	map[string][]string	"images: list of fully-qualified image references"
//	@Failure		401	{object}	ErrorResponse
//	@Failure		404	{object}	ErrorResponse
//	@Router			/architectures/{id}/images [get]
func (h *CatalogHandler) GetArchitectureImages(c *gin.Context) {
	archID := c.Param("id")

	images, err := h.provider.GetCatalogImages(c.Request.Context(), archID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get images for architecture '%s': %v", archID, err),
		})

		return
	}

	c.JSON(http.StatusOK, gin.H{"images": images})
}

// GetServiceModels godoc
//
//	@Summary		Get AI models for a service
//	@Description	Returns all model identifiers extracted from component schemas for a service template.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string		true	"Service ID"
//	@Success		200	{object}	map[string][]string	"models: list of model identifiers"
//	@Failure		401	{object}	ErrorResponse
//	@Failure		404	{object}	ErrorResponse
//	@Router			/services/{id}/models [get]
func (h *CatalogHandler) GetServiceModels(c *gin.Context) {
	serviceID := c.Param("id")

	models, err := h.provider.GetCatalogModels(c.Request.Context(), serviceID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get models for service '%s': %v", serviceID, err),
		})

		return
	}

	c.JSON(http.StatusOK, gin.H{"models": models})
}

// GetArchitectureModels godoc
//
//	@Summary		Get AI models for an architecture
//	@Description	Returns all model identifiers extracted from component schemas for all services in an architecture template.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string		true	"Architecture ID"
//	@Success		200	{object}	map[string][]string	"models: list of model identifiers"
//	@Failure		401	{object}	ErrorResponse
//	@Failure		404	{object}	ErrorResponse
//	@Router			/architectures/{id}/models [get]
func (h *CatalogHandler) GetArchitectureModels(c *gin.Context) {
	archID := c.Param("id")

	models, err := h.provider.GetCatalogModels(c.Request.Context(), archID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to get models for architecture '%s': %v", archID, err),
		})

		return
	}

	c.JSON(http.StatusOK, gin.H{"models": models})
}

// GetServiceMD godoc
//
//	@Summary		Get markdown template content for a service
//	@Description	Returns the raw markdown template strings (info.md, next.md, etc.) for a service. These are Go text/template sources that the CLI renders locally with runtime parameters.
//	@Tags			Catalog
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string					true	"Service ID"
//	@Success		200	{object}	map[string]string		"map of filename → raw template string"
//	@Failure		401	{object}	ErrorResponse
//	@Failure		404	{object}	ErrorResponse
//	@Router			/services/{id}/md [get]
func (h *CatalogHandler) GetServiceMD(c *gin.Context) {
	serviceID := c.Param("id")

	tmpls, err := h.provider.LoadServicesMD(serviceID)
	if err != nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("Failed to load markdown templates for service '%s': %v", serviceID, err),
		})

		return
	}

	// Serialize the parsed templates back to their source strings so the
	// CLI can re-parse them locally for execution.
	out := make(map[string]string, len(tmpls))
	for name, tmpl := range tmpls {
		out[name] = tmpl.Root.String()
	}

	c.JSON(http.StatusOK, out)
}

// ErrorResponse represents an error response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Made with Bob
