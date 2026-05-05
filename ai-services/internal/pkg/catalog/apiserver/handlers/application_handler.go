package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/project-ai-services/ai-services/internal/pkg/image"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
)

type ApplicationHandler struct {
	runtimeType types.RuntimeType
}

func NewApplicationHandler(runtimeType types.RuntimeType) *ApplicationHandler {
	return &ApplicationHandler{runtimeType: runtimeType}
}

type ComponentParam struct {
	Type          string                 `json:"type" binding:"required"`
	ComponentType string                 `json:"component_type" binding:"required"`
	ProviderID    string                 `json:"provider_id" binding:"required"`
	InstanceID    string                 `json:"instance_id,omitempty"`
	Params        map[string]interface{} `json:"params,omitempty"`
}

type ServiceParam struct {
	Type       string                 `json:"type" binding:"required"`
	ServiceID  string                 `json:"service_id" binding:"required"`
	Enabled    bool                   `json:"enabled"`
	Version    string                 `json:"version,omitempty"`
	Params     map[string]interface{} `json:"params,omitempty"`
	Components []ComponentParam       `json:"components,omitempty"`
}

type ArchitectureParam struct {
	Type          string                 `json:"type" binding:"required"`
	ComponentType string                 `json:"component_type" binding:"required"`
	ProviderID    string                 `json:"provider_id" binding:"required"`
	Config        map[string]interface{} `json:"config,omitempty"`
}

type createApplicationReq struct {
	Name              string              `json:"name" binding:"required"`
	Template          string              `json:"template" binding:"required"`
	CreatedBy         string              `json:"created_by,omitempty"`
	Params            []ArchitectureParam `json:"params,omitempty"`
	Services          []ServiceParam      `json:"services" binding:"required"`
	SkipModelDownload bool                `json:"skip_model_download,omitempty"`
	SkipImageDownload bool                `json:"skip_image_download,omitempty"`
	ImagePullPolicy   string              `json:"image_pull_policy,omitempty"`
}

// CreateApplication godoc
//
//	@Summary		Create new application
//	@Description	Create a new application instance from a template
//	@Tags			Applications
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			application	body		createApplicationReq	true	"Application creation request"
//	@Success		201			{object}	map[string]interface{}	"Application created successfully"
//	@Failure		400			{object}	map[string]interface{}	"Invalid request payload"
//	@Failure		500			{object}	map[string]interface{}	"Failed to create application"
//	@Router			/applications [post]
func (h *ApplicationHandler) CreateApplication(c *gin.Context) {
	var req createApplicationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload", "details": err.Error()})
		return
	}

	// Validate request structure
	if err := h.validateApplicationRequest(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "validation failed", "details": err.Error()})
		return
	}

	ctx := context.Background()

	// Set default image pull policy if not provided
	pullPolicy := image.PullIfNotPresent
	if req.ImagePullPolicy != "" {
		pullPolicy = image.ImagePullPolicy(req.ImagePullPolicy)
		if !pullPolicy.Valid() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image_pull_policy", "details": "must be one of: Always, Never, IfNotPresent"})
			return
		}
	}

	// Deploy the application using custom deployment logic
	appName, err := h.deployApplication(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to deploy application", "details": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":  "application created successfully",
		"name":     appName,
		"template": req.Template,
		"services": len(req.Services),
	})
}

// validateApplicationRequest validates the application creation request
func (h *ApplicationHandler) validateApplicationRequest(req *createApplicationReq) error {
	// Validate architecture params
	for i, param := range req.Params {
		if param.Type != "component" {
			return fmt.Errorf("params[%d]: type must be 'component', got '%s'", i, param.Type)
		}
		if param.ComponentType == "" {
			return fmt.Errorf("params[%d]: component_type is required", i)
		}
		if param.ProviderID == "" {
			return fmt.Errorf("params[%d]: provider_id is required", i)
		}
	}

	// Validate services
	if len(req.Services) == 0 {
		return fmt.Errorf("at least one service is required")
	}

	for i, service := range req.Services {
		if service.Type != "service" {
			return fmt.Errorf("services[%d]: type must be 'service', got '%s'", i, service.Type)
		}
		if service.ServiceID == "" {
			return fmt.Errorf("services[%d]: service_id is required", i)
		}

		// Validate components within each service
		for j, component := range service.Components {
			if err := h.validateComponent(&component, i, j); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateComponent validates a single component configuration
func (h *ApplicationHandler) validateComponent(component *ComponentParam, serviceIdx, componentIdx int) error {
	if component.Type != "component" {
		return fmt.Errorf("services[%d].components[%d]: type must be 'component', got '%s'", serviceIdx, componentIdx, component.Type)
	}
	if component.ComponentType == "" {
		return fmt.Errorf("services[%d].components[%d]: component_type is required", serviceIdx, componentIdx)
	}
	if component.ProviderID == "" {
		return fmt.Errorf("services[%d].components[%d]: provider_id is required", serviceIdx, componentIdx)
	}

	// Validate that either instance_id or params is provided, but not both
	hasInstanceID := component.InstanceID != ""
	hasParams := component.Params != nil && len(component.Params) > 0

	if hasInstanceID && hasParams {
		return fmt.Errorf("services[%d].components[%d]: cannot specify both instance_id and params - use instance_id to reuse existing component or params to create new one", serviceIdx, componentIdx)
	}

	return nil
}

// Made with Bob
