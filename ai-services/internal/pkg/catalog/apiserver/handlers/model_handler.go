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

// DeployModel godoc
//
//	@Summary		Deploy a local model
//	@Description	Validates the request, inserts a Deploying component row, and starts async pod creation + LiteLLM route registration. Returns 202 immediately. Poll GET /api/v1/models/:id for status.
//	@Tags			Models
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		models.DeployModelRequest	true	"Model deploy request"
//	@Success		202		{object}	models.DeployModelResponse	"Deploy initiated"
//	@Failure		400		{object}	ErrorResponse				"Missing required fields, unknown type, or unknown provider_id"
//	@Failure		401		{object}	ErrorResponse				"Unauthorized"
//	@Failure		404		{object}	ErrorResponse				"worker_selector refers to an unknown worker"
//	@Failure		409		{object}	ErrorResponse				"A component of this type is already Running or Deploying"
//	@Failure		422		{object}	ErrorResponse				"Pre-flight resource check failed"
//	@Failure		500		{object}	ErrorResponse				"Internal Server Error"
//	@Router			/models [post]
func (h *ModelHandler) DeployModel(c *gin.Context) {
	var req models.DeployModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("Invalid request body: %v", err),
		})

		return
	}

	userID := c.GetString(middleware.CtxUserIDKey)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, ErrorResponse{
			Error: "Unauthorized: user ID not found in context",
		})

		return
	}

	req.CreatedBy = userID

	resp, err := h.modelSvc.DeployModel(c.Request.Context(), req)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

			return
		}

		logger.ErrorfCtx(c.Request.Context(), "failed to deploy model: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: fmt.Sprintf("Failed to deploy model: %v", err),
		})

		return
	}

	c.JSON(http.StatusAccepted, resp)
}

// ListModels godoc
//
//	@Summary		List local models
//	@Description	Returns a paginated list of all managed local model components. Filter by component type with ?type=.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			type		query		string	false	"Filter by component type: llm, embedding, reranker"
//	@Param			page		query		int		false	"Page number (1-indexed)"		default(1)
//	@Param			page_size	query		int		false	"Items per page (max 100)"		default(20)
//	@Success		200			{object}	models.ListModelsResponse
//	@Failure		400			{object}	ErrorResponse	"Invalid query parameters"
//	@Failure		401			{object}	ErrorResponse	"Unauthorized"
//	@Failure		500			{object}	ErrorResponse	"Internal Server Error"
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
		Type:     c.Query("type"),
		Page:     page,
		PageSize: pageSize,
	}

	resp, err := h.modelSvc.ListModels(c.Request.Context(), req)
	if err != nil {
		logger.ErrorfCtx(c.Request.Context(), "failed to list models: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: "Failed to list models",
		})

		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetModel godoc
//
//	@Summary		Get model details
//	@Description	Returns the full record for a managed local model by UUID.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string					true	"Model component UUID"
//	@Success		200	{object}	models.GetModelResponse	"Model detail"
//	@Failure		400	{object}	ErrorResponse			"Invalid UUID format"
//	@Failure		401	{object}	ErrorResponse			"Unauthorized"
//	@Failure		404	{object}	ErrorResponse			"Model not found"
//	@Failure		500	{object}	ErrorResponse			"Internal Server Error"
//	@Router			/models/{id} [get]
func (h *ModelHandler) GetModel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("Invalid model ID format: %v", err),
		})

		return
	}

	resp, err := h.modelSvc.GetModel(c.Request.Context(), id)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

			return
		}

		logger.ErrorfCtx(c.Request.Context(), "failed to get model: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: "Failed to get model",
		})

		return
	}

	c.JSON(http.StatusOK, resp)
}

// UndeployModel godoc
//
//	@Summary		Undeploy a local model
//	@Description	Initiates async teardown: deregisters the LiteLLM route, revokes the virtual key, and deletes the component row. Returns 202 immediately.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string						true	"Model component UUID"
//	@Param			keep_data	query		bool						false	"Preserve host volume (stop pod but keep weights)"	default(false)
//	@Success		202			{object}	models.UndeployModelResponse	"Undeploy initiated"
//	@Failure		400			{object}	ErrorResponse				"Invalid UUID format"
//	@Failure		401			{object}	ErrorResponse				"Unauthorized"
//	@Failure		403			{object}	ErrorResponse				"Not the deploying user"
//	@Failure		404			{object}	ErrorResponse				"Model not found"
//	@Failure		409			{object}	ErrorResponse				"Model in use by active applications or already deleting"
//	@Failure		500			{object}	ErrorResponse				"Internal Server Error"
//	@Router			/models/{id} [delete]
func (h *ModelHandler) UndeployModel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("Invalid model ID format: %v", err),
		})

		return
	}

	userID := c.GetString(middleware.CtxUserIDKey)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, ErrorResponse{
			Error: "Unauthorized: user ID not found in context",
		})

		return
	}

	resp, err := h.modelSvc.UndeployModel(c.Request.Context(), id, userID)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

			return
		}

		logger.ErrorfCtx(c.Request.Context(), "failed to undeploy model: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: "Failed to undeploy model",
		})

		return
	}

	c.JSON(http.StatusAccepted, resp)
}

// GetModelKey godoc
//
//	@Summary		Get virtual key for a model
//	@Description	Returns the LiteLLM virtual key for a deployed local model. Used by consumer service pods at startup to retrieve their bearer token.
//	@Tags			Models
//	@Produce		json
//	@Security		BearerAuth
//	@Param			instance_id	query		string						true	"Model component UUID"
//	@Success		200			{object}	models.GetModelKeyResponse	"Virtual key"
//	@Failure		400			{object}	ErrorResponse				"Missing or invalid instance_id"
//	@Failure		401			{object}	ErrorResponse				"Unauthorized"
//	@Failure		404			{object}	ErrorResponse				"Model or key not found"
//	@Failure		500			{object}	ErrorResponse				"Internal Server Error"
//	@Router			/models/keys [get]
func (h *ModelHandler) GetModelKey(c *gin.Context) {
	raw := c.Query("instance_id")
	if raw == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "instance_id query parameter is required",
		})

		return
	}

	componentID, err := uuid.Parse(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("Invalid instance_id format: %v", err),
		})

		return
	}

	resp, err := h.modelSvc.GetModelKey(c.Request.Context(), componentID)
	if err != nil {
		if valErr, ok := err.(*repository.ValidationError); ok {
			c.JSON(valErr.Code, ErrorResponse{Error: valErr.Message})

			return
		}

		logger.ErrorfCtx(c.Request.Context(), "failed to get model key: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: "Failed to get model key",
		})

		return
	}

	c.JSON(http.StatusOK, resp)
}

// Made with Bob
