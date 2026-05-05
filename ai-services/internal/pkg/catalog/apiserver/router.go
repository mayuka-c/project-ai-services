package apiserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/project-ai-services/ai-services/assets"
	_ "github.com/project-ai-services/ai-services/docs" // Import generated docs
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/handlers"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/middleware"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/repository"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/auth"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// CreateRouter sets up the Gin router with the necessary routes and authentication middleware for the API server.
func CreateRouter(authSvc auth.Service, tokenMgr *auth.TokenManager, blacklist repository.TokenBlacklist, runtimeType types.RuntimeType) *gin.Engine {
	router := gin.Default()

	// Health check endpoint
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	// Swagger documentation endpoint
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	authHandler := handlers.NewAuthHandler(authSvc)
	optionsHandler := handlers.NewOptionsHandler(assets.CatalogFS)
	applicationHandler := handlers.NewApplicationHandler(runtimeType)

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", authHandler.Login)
		v1.POST("/auth/logout", middleware.AuthMiddleware(tokenMgr, blacklist), authHandler.Logout)
		v1.POST("/auth/refresh", authHandler.Refresh)
		v1.GET("/auth/me", middleware.AuthMiddleware(tokenMgr, blacklist), authHandler.Me)

		// Component selection options endpoints
		v1.GET("/architectures/:architecture_id/options", middleware.AuthMiddleware(tokenMgr, blacklist), optionsHandler.GetArchitectureOptions)
		v1.GET("/services/:service_id/options", middleware.AuthMiddleware(tokenMgr, blacklist), optionsHandler.GetServiceOptions)
		v1.GET("/components/:component_type/instances", middleware.AuthMiddleware(tokenMgr, blacklist), optionsHandler.GetComponentInstances)

		// Component selection parameters endpoints
		v1.GET("/architectures/:architecture_id/params", middleware.AuthMiddleware(tokenMgr, blacklist), optionsHandler.GetArchitectureParams)
		v1.GET("/services/:service_id/params", middleware.AuthMiddleware(tokenMgr, blacklist), optionsHandler.GetServiceParams)
		v1.GET("/components/:component_type/providers/:provider_id/params", middleware.AuthMiddleware(tokenMgr, blacklist), optionsHandler.GetComponentProviderParams)
	}

	applications := v1.Group("applications")
	applications.Use(middleware.AuthMiddleware(tokenMgr, blacklist))

	// Application management endpoints
	applications.GET("/templates", getTemplates)
	applications.POST("/", applicationHandler.CreateApplication)
	applications.GET("/:name", getApplication)
	applications.DELETE("/:name", deleteApplication)
	applications.GET("/:name/ps", getApplicationStatus)
	applications.POST("/:name/start", startApplication)
	applications.POST("/:name/stop", stopApplication)
	applications.GET("/:name/logs", getApplicationLogs)

	return router
}

// GetTemplates godoc
//
//	@Summary		List application templates
//	@Description	Get a list of available application templates
//	@Tags			Applications
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	map[string]interface{}	"List of templates"
//	@Router			/applications/templates [get]
func getTemplates(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "This is a placeholder endpoint for " + c.FullPath()})
}

// GetApplication godoc
//
//	@Summary		Get application details
//	@Description	Get detailed information about a specific application
//	@Tags			Applications
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path		string					true	"Application name"
//	@Success		200		{object}	map[string]interface{}	"Application details"
//	@Failure		404		{object}	map[string]interface{}	"Application not found"
//	@Router			/applications/{name} [get]
func getApplication(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "This is a placeholder endpoint for " + c.FullPath()})
}

// DeleteApplication godoc
//
//	@Summary		Delete application
//	@Description	Delete a specific application and all its resources
//	@Tags			Applications
//	@Security		BearerAuth
//	@Param			name	path		string					true	"Application name"
//	@Success		200		{object}	map[string]interface{}	"Application deleted"
//	@Failure		404		{object}	map[string]interface{}	"Application not found"
//	@Router			/applications/{name} [delete]
func deleteApplication(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "This is a placeholder endpoint for " + c.FullPath()})
}

// GetApplicationStatus godoc
//
//	@Summary		Get application status
//	@Description	Get the running status and health of an application
//	@Tags			Applications
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path		string					true	"Application name"
//	@Success		200		{object}	map[string]interface{}	"Application status"
//	@Failure		404		{object}	map[string]interface{}	"Application not found"
//	@Router			/applications/{name}/ps [get]
func getApplicationStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "This is a placeholder endpoint for " + c.FullPath()})
}

// StartApplication godoc
//
//	@Summary		Start application
//	@Description	Start a stopped application
//	@Tags			Applications
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path		string					true	"Application name"
//	@Success		200		{object}	map[string]interface{}	"Application started"
//	@Failure		404		{object}	map[string]interface{}	"Application not found"
//	@Router			/applications/{name}/start [post]
func startApplication(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "This is a placeholder endpoint for " + c.FullPath()})
}

// StopApplication godoc
//
//	@Summary		Stop application
//	@Description	Stop a running application
//	@Tags			Applications
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path		string					true	"Application name"
//	@Success		200		{object}	map[string]interface{}	"Application stopped"
//	@Failure		404		{object}	map[string]interface{}	"Application not found"
//	@Router			/applications/{name}/stop [post]
func stopApplication(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "This is a placeholder endpoint for " + c.FullPath()})
}

// GetApplicationLogs godoc
//
//	@Summary		Get application logs
//	@Description	Get logs from a specific application
//	@Tags			Applications
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path		string					true	"Application name"
//	@Success		200		{object}	map[string]interface{}	"Application logs"
//	@Failure		404		{object}	map[string]interface{}	"Application not found"
//	@Router			/applications/{name}/logs [get]
func getApplicationLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "This is a placeholder endpoint for " + c.FullPath()})
}
