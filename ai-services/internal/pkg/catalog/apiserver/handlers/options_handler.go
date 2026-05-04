package handlers

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// OptionsHandler handles component selection options endpoints
type OptionsHandler struct {
	assetsFS embed.FS
}

// NewOptionsHandler creates a new options handler
func NewOptionsHandler(assetsFS embed.FS) *OptionsHandler {
	return &OptionsHandler{
		assetsFS: assetsFS,
	}
}

// Provider metadata structures
type ProviderMetadata struct {
	ProviderID      string   `yaml:"provider_id"`
	Provider        string   `yaml:"provider"`
	Label           string   `yaml:"label"`
	Description     string   `yaml:"description"`
	ComponentType   string   `yaml:"component_type"`
	SupportedModels []string `yaml:"supported_models"`
	Architectures   []string `yaml:"architectures"`
}

// ServiceMetadata structures
type ServiceMetadata struct {
	ID            string                `yaml:"id"`
	Name          string                `yaml:"name"`
	Description   string                `yaml:"description"`
	Type          string                `yaml:"type"`
	Architectures []string              `yaml:"architectures"`
	Dependencies  []ComponentDependency `yaml:"dependencies"`
}

type ComponentDependency struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
}

// ArchitectureMetadata structures
type ArchitectureMetadata struct {
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Version     string       `yaml:"version"`
	Type        string       `yaml:"type"`
	Services    []ServiceRef `yaml:"services"`
}

type ServiceRef struct {
	ID       string `yaml:"id"`
	Version  string `yaml:"version"`
	Optional bool   `yaml:"optional"`
}

// Response structures
type ProviderResponse struct {
	ProviderID      string   `json:"provider_id"`
	Provider        string   `json:"provider"`
	Label           string   `json:"label"`
	Description     string   `json:"description,omitempty"`
	SupportedModels []string `json:"supported_models,omitempty"`
}

type ComponentResponse struct {
	Label     string             `json:"label"`
	Required  bool               `json:"required"`
	Providers []ProviderResponse `json:"providers"`
}

type ServiceOptionsResponse struct {
	ServiceID   string                       `json:"service_id"`
	ServiceName string                       `json:"service_name"`
	Components  map[string]ComponentResponse `json:"components"`
}

type ArchitectureOptionsResponse struct {
	ArchitectureID   string                            `json:"architecture_id"`
	ArchitectureName string                            `json:"architecture_name"`
	Services         map[string]ServiceOptionsResponse `json:"services"`
}

// GetArchitectureOptions godoc
//
//	@Summary		Get architecture options
//	@Description	Get available providers and dependency rules for all services in an architecture
//	@Tags			Options
//	@Produce		json
//	@Security		BearerAuth
//	@Param			architecture_id	path		string	true	"Architecture ID"
//	@Success		200				{object}	ArchitectureOptionsResponse
//	@Failure		404				{object}	map[string]interface{}	"Architecture not found"
//	@Failure		500				{object}	map[string]interface{}	"Internal server error"
//	@Router			/architectures/{architecture_id}/options [get]
func (h *OptionsHandler) GetArchitectureOptions(c *gin.Context) {
	architectureID := c.Param("architecture_id")

	// Read architecture metadata
	archPath := filepath.Join("architectures", architectureID, "metadata.yaml")
	archData, err := h.assetsFS.ReadFile(archPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "architecture not found", "details": err.Error()})
		return
	}

	var archMeta ArchitectureMetadata
	if err := yaml.Unmarshal(archData, &archMeta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse architecture metadata", "details": err.Error()})
		return
	}

	response := ArchitectureOptionsResponse{
		ArchitectureID:   archMeta.ID,
		ArchitectureName: archMeta.Name,
		Services:         make(map[string]ServiceOptionsResponse),
	}

	// Process each service in the architecture
	for _, svcRef := range archMeta.Services {
		svcOptions, err := h.getServiceOptions(svcRef.ID, architectureID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get options for service %s", svcRef.ID), "details": err.Error()})
			return
		}
		response.Services[svcRef.ID] = svcOptions
	}

	c.JSON(http.StatusOK, response)
}

// GetServiceOptions godoc
//
//	@Summary		Get service options
//	@Description	Get available providers and dependency rules for a specific service
//	@Tags			Options
//	@Produce		json
//	@Security		BearerAuth
//	@Param			service_id	path		string	true	"Service ID"
//	@Success		200			{object}	ServiceOptionsResponse
//	@Failure		404			{object}	map[string]interface{}	"Service not found"
//	@Failure		500			{object}	map[string]interface{}	"Internal server error"
//	@Router			/services/{service_id}/options [get]
func (h *OptionsHandler) GetServiceOptions(c *gin.Context) {
	serviceID := c.Param("service_id")

	svcOptions, err := h.getServiceOptions(serviceID, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get service options", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, svcOptions)
}

// getServiceOptions is a helper function to get service options
func (h *OptionsHandler) getServiceOptions(serviceID, architectureID string) (ServiceOptionsResponse, error) {
	// Read service metadata
	svcPath := filepath.Join("services", serviceID, "metadata.yaml")
	svcData, err := h.assetsFS.ReadFile(svcPath)
	if err != nil {
		return ServiceOptionsResponse{}, fmt.Errorf("service not found: %w", err)
	}

	var svcMeta ServiceMetadata
	if err := yaml.Unmarshal(svcData, &svcMeta); err != nil {
		return ServiceOptionsResponse{}, fmt.Errorf("failed to parse service metadata: %w", err)
	}

	response := ServiceOptionsResponse{
		ServiceID:   svcMeta.ID,
		ServiceName: svcMeta.Name,
		Components:  make(map[string]ComponentResponse),
	}

	// Component type to label mapping
	componentLabels := map[string]string{
		"vector_db": "Vector store",
		"llm":       "LLM Model",
		"embedding": "Embedding Model",
		"reranker":  "Reranker Model",
	}

	// Dynamically build components from service metadata dependencies
	for _, dep := range svcMeta.Dependencies {
		providers, err := h.getProvidersForComponentType(dep.Type, architectureID)
		if err != nil {
			continue // Skip if no providers found
		}

		label := componentLabels[dep.Type]
		if label == "" {
			label = dep.Type // Fallback to type if label not found
		}

		response.Components[dep.Type] = ComponentResponse{
			Label:     label,
			Required:  dep.Required,
			Providers: providers,
		}
	}

	return response, nil
}

// getProvidersForComponentType gets all providers for a component type
func (h *OptionsHandler) getProvidersForComponentType(componentType, architectureID string) ([]ProviderResponse, error) {
	var providers []ProviderResponse

	// Read component directory
	componentPath := filepath.Join("components", componentType)
	entries, err := h.assetsFS.ReadDir(componentPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		providerPath := filepath.Join(componentPath, entry.Name(), "metadata.yaml")
		providerData, err := h.assetsFS.ReadFile(providerPath)
		if err != nil {
			continue
		}

		var providerMeta ProviderMetadata
		if err := yaml.Unmarshal(providerData, &providerMeta); err != nil {
			continue
		}

		// Filter by architecture if specified
		if architectureID != "" {
			found := false
			for _, arch := range providerMeta.Architectures {
				if arch == architectureID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		providers = append(providers, ProviderResponse{
			ProviderID:      providerMeta.ProviderID,
			Provider:        providerMeta.Provider,
			Label:           providerMeta.Label,
			Description:     providerMeta.Description,
			SupportedModels: providerMeta.SupportedModels,
		})
	}

	return providers, nil
}

// Instance structures
type InstanceResponse struct {
	InstanceID string `json:"instance_id"`
	Label      string `json:"label"`
	Provider   string `json:"provider"`
}

// GetComponentInstances godoc
//
//	@Summary		Get component instances
//	@Description	Get all running instances for a specific component type
//	@Tags			Components
//	@Produce		json
//	@Security		BearerAuth
//	@Param			component_type	path		string	true	"Component type (vector_db, llm, embedding, reranker)"
//	@Success		200				{array}		InstanceResponse
//	@Failure		400				{object}	map[string]interface{}	"Invalid component type"
//	@Failure		500				{object}	map[string]interface{}	"Internal server error"
//	@Router			/components/{component_type}/instances [get]
func (h *OptionsHandler) GetComponentInstances(c *gin.Context) {
	// TODO: This should query actual running instances from the DB
	// For now, return empty array as placeholder
	instances := []InstanceResponse{}

	c.JSON(http.StatusOK, instances)
}

// GetServiceParams godoc
//
//	@Summary		Get service parameters
//	@Description	Get configuration schema (JSON Schema) for a specific service
//	@Tags			Parameters
//	@Produce		json
//	@Security		BearerAuth
//	@Param			service_id	path		string	true	"Service ID"
//	@Success		200			{object}	map[string]interface{}	"JSON Schema for service configuration"
//	@Failure		404			{object}	map[string]interface{}	"Service not found"
//	@Failure		500			{object}	map[string]interface{}	"Internal server error"
//	@Router			/services/{service_id}/params [get]
func (h *OptionsHandler) GetServiceParams(c *gin.Context) {
	serviceID := c.Param("service_id")

	fmt.Println("ServiceID: ", serviceID)

	// Read service schema file from podman directory
	schemaPath := filepath.Join("services", serviceID, "podman", "values.schema.json")
	schemaData, err := h.assetsFS.ReadFile(schemaPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "service schema not found", "details": err.Error()})
		return
	}

	// Parse and return the JSON schema
	var schema map[string]interface{}
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse schema", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, schema)
}

// GetComponentProviderParams godoc
//
//	@Summary		Get component provider parameters
//	@Description	Get configuration schema (JSON Schema) for a specific provider within a component type
//	@Tags			Parameters
//	@Produce		json
//	@Security		BearerAuth
//	@Param			component_type	path		string	true	"Component type (vector_db, llm, embedding, reranker)"
//	@Param			provider_id		path		string	true	"Provider ID (e.g., opensearch, vllm, watsonx)"
//	@Success		200				{object}	map[string]interface{}	"JSON Schema for provider configuration"
//	@Failure		404				{object}	map[string]interface{}	"Provider schema not found"
//	@Failure		500				{object}	map[string]interface{}	"Internal server error"
//	@Router			/components/{component_type}/providers/{provider_id}/params [get]
func (h *OptionsHandler) GetComponentProviderParams(c *gin.Context) {
	componentType := c.Param("component_type")
	providerID := c.Param("provider_id")

	// Read provider schema file from podman directory
	schemaPath := filepath.Join("components", componentType, providerID, "podman", "values.schema.json")
	schemaData, err := h.assetsFS.ReadFile(schemaPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider schema not found", "details": err.Error()})
		return
	}

	// Parse and return the JSON schema
	var schema map[string]interface{}
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse schema", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, schema)
}

// Made with Bob
