package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/middleware"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/models"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/repository"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
)

// ModelHandler handles model management HTTP requests.
type ModelHandler struct {
	modelSvc repository.ModelServiceInterface
}

// NewModelHandler creates a new ModelHandler.
func NewModelHandler(modelSvc repository.ModelServiceInterface) *ModelHandler {
	return &ModelHandler{modelSvc: modelSvc}
}

// CreateModel godoc
//
//	@Summary		Create a model (polymorphic)
//	@Description	deployment_type=local: deploys a pod and registers a LiteLLM route (async, 202). deployment_type=remote: registers a remote connector, probes connectivity, returns 201.
//	@Tags			Models
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		models.CreateModelRequest	true	"Model create request"
//	@Success		202		{object}	models.CreateModelResponse	"Local deploy initiated"
//	@Success		201		{object}	models.CreateModelResponse	"Remote connector created"
//	@Failure		400		{object}	ErrorResponse				"Invalid request body, unknown deployment_type/type/provider_id"
//	@Failure		401		{object}	ErrorResponse				"Unauthorized"
//	@Failure		404		{object}	ErrorResponse				"worker_selector or provider_id not found"
//	@Failure		409		{object}	ErrorResponse				"Conflict (duplicate name or already deploying)"
//	@Failure		422		{object}	ErrorResponse				"Pre-flight or connectivity check failed"
//	@Failure		500		{object}	ErrorResponse				"Internal Server Error"
//	@Router			/models [post]
func (h *ModelHandler) CreateModel(c *gin.Context) {
	var req models.CreateModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("invalid request body: %v", err)})
		return
	}

	userID := c.GetString(middleware.CtxUserIDKey)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "unauthorized: user ID not found in context"})
		return
	}
	req.CreatedBy = userID

	resp, err := h.modelSvc.CreateModel(c.Request.Context(), req)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})
			return
		}
		logger.ErrorfCtx(c.Request.Context(), "failed to create model: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: fmt.Sprintf("failed to create model: %v", err)})
		return
	}

	if resp.DeploymentType == "remote" {
		c.JSON(http.StatusCreated, resp)
	} else {
		c.JSON(http.StatusAccepted, resp)
	}
}

// ListModels godoc
//
//	@Summary		List models
//	@Description	Returns a paginated list of all models — both local (components) and remote (connectors). Filter by ?type= and ?deployment_type=.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			type			query		string	false	"Filter by role: llm, embedding, reranker"
//	@Param			deployment_type	query		string	false	"Filter by kind: local, remote"
//	@Param			page			query		int		false	"Page number (1-indexed)"	default(1)
//	@Param			page_size		query		int		false	"Items per page (max 100)"	default(20)
//	@Success		200				{object}	models.ListModelsResponse
//	@Failure		400				{object}	ErrorResponse	"Invalid query parameters"
//	@Failure		401				{object}	ErrorResponse	"Unauthorized"
//	@Failure		500				{object}	ErrorResponse	"Internal Server Error"
//	@Router			/models [get]
func (h *ModelHandler) ListModels(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	page, pageSize, err := repository.ValidatePaginationParams(page, pageSize)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	req := models.ListModelsRequest{
		Type:           c.Query("type"),
		DeploymentType: c.Query("deployment_type"),
		Page:           page,
		PageSize:       pageSize,
	}

	resp, err := h.modelSvc.ListModels(c.Request.Context(), req)
	if err != nil {
		logger.ErrorfCtx(c.Request.Context(), "failed to list models: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to list models"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetModel godoc
//
//	@Summary		Get model details
//	@Description	Returns full details of any model by UUID. Resolves against components first, then connectors. The deployment_type field in the response indicates which table was matched.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string					true	"Model UUID"
//	@Success		200	{object}	models.GetModelResponse	"Model detail"
//	@Failure		400	{object}	ErrorResponse			"Invalid UUID"
//	@Failure		401	{object}	ErrorResponse			"Unauthorized"
//	@Failure		404	{object}	ErrorResponse			"Model not found"
//	@Failure		500	{object}	ErrorResponse			"Internal Server Error"
//	@Router			/models/{id} [get]
func (h *ModelHandler) GetModel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("invalid model ID: %v", err)})
		return
	}

	resp, err := h.modelSvc.GetModel(c.Request.Context(), id)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})
			return
		}
		logger.ErrorfCtx(c.Request.Context(), "failed to get model: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to get model"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// UpdateRemoteModel godoc
//
//	@Summary		Update remote model credentials
//	@Description	Updates the credential (Authentication) fields of a remote model connector. Only fields marked ui:section="Authentication" in the provider's schema.json are applied. Structural fields are immutable. Returns 405 if the UUID resolves to a local model.
//	@Tags			Models
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string								true	"Model UUID"
//	@Param			request	body		models.UpdateRemoteModelRequest		true	"Credential update"
//	@Success		200		{object}	models.UpdateRemoteModelResponse	"Updated"
//	@Failure		400		{object}	ErrorResponse						"Invalid request"
//	@Failure		401		{object}	ErrorResponse						"Unauthorized"
//	@Failure		403		{object}	ErrorResponse						"Not the owner"
//	@Failure		404		{object}	ErrorResponse						"Model not found"
//	@Failure		405		{object}	ErrorResponse						"UUID resolves to a local model — not updatable"
//	@Failure		422		{object}	ErrorResponse						"Connectivity check failed"
//	@Failure		500		{object}	ErrorResponse						"Internal Server Error"
//	@Router			/models/{id} [put]
func (h *ModelHandler) UpdateRemoteModel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("invalid model ID: %v", err)})
		return
	}

	userID := c.GetString(middleware.CtxUserIDKey)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "unauthorized: user ID not found in context"})
		return
	}

	var req models.UpdateRemoteModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("invalid request body: %v", err)})
		return
	}

	resp, err := h.modelSvc.UpdateRemoteModel(c.Request.Context(), id, userID, req)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})
			return
		}
		logger.ErrorfCtx(c.Request.Context(), "failed to update remote model: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to update remote model"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// UndeployModel godoc
//
//	@Summary		Delete / undeploy a model
//	@Description	Resolves the UUID against components first, then connectors. Local: async teardown (stop pod, deregister route, revoke key, delete row) → 202. Remote: sync deregister + delete → 204.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string						true	"Model UUID"
//	@Success		202	{object}	models.UndeployModelResponse	"Local undeploy initiated"
//	@Success		204	nil											"Remote connector deleted"
//	@Failure		400	{object}	ErrorResponse				"Invalid UUID"
//	@Failure		401	{object}	ErrorResponse				"Unauthorized"
//	@Failure		403	{object}	ErrorResponse				"Not the owner"
//	@Failure		404	{object}	ErrorResponse				"Model not found"
//	@Failure		409	{object}	ErrorResponse				"Model in use or already deleting"
//	@Failure		500	{object}	ErrorResponse				"Internal Server Error"
//	@Router			/models/{id} [delete]
func (h *ModelHandler) UndeployModel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("invalid model ID: %v", err)})
		return
	}

	userID := c.GetString(middleware.CtxUserIDKey)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "unauthorized: user ID not found in context"})
		return
	}

	resp, err := h.modelSvc.UndeployModel(c.Request.Context(), id, userID)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})
			return
		}
		logger.ErrorfCtx(c.Request.Context(), "failed to undeploy model: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to undeploy model"})
		return
	}

	if resp.DeploymentType == "remote" {
		c.Status(http.StatusNoContent)
	} else {
		c.JSON(http.StatusAccepted, resp)
	}
}

// GetModelKey godoc
//
//	@Summary		Get virtual key for a model
//	@Description	Returns the LiteLLM virtual key for any model (local component or remote connector). Used by consumer service pods at startup to retrieve their bearer token.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			instance_id	query		string						true	"Model UUID (component or connector)"
//	@Success		200			{object}	models.GetModelKeyResponse	"Virtual key"
//	@Failure		400			{object}	ErrorResponse				"Missing or invalid instance_id"
//	@Failure		401			{object}	ErrorResponse				"Unauthorized"
//	@Failure		404			{object}	ErrorResponse				"Model or key not found"
//	@Failure		500			{object}	ErrorResponse				"Internal Server Error"
//	@Router			/models/keys [get]
func (h *ModelHandler) GetModelKey(c *gin.Context) {
	raw := c.Query("instance_id")
	if raw == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "instance_id query parameter is required"})
		return
	}

	instanceID, err := uuid.Parse(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("invalid instance_id: %v", err)})
		return
	}

	resp, err := h.modelSvc.GetModelKey(c.Request.Context(), instanceID)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})
			return
		}
		logger.ErrorfCtx(c.Request.Context(), "failed to get model key: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "failed to get model key"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// Made with Bob
