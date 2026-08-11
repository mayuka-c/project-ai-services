package apiserver

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	_ "github.com/project-ai-services/ai-services/docs" // Import generated docs
	catalogpkg "github.com/project-ai-services/ai-services/internal/pkg/catalog"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/handlers"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/middleware"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/repository"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/auth"
	bundlesvc "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/services/bundle"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// CreateRouter sets up the Gin router with the necessary routes and authentication middleware for the API server.
func CreateRouter(authSvc auth.Service, tokenMgr *auth.TokenManager, blacklist repository.TokenBlacklist, appService repository.ApplicationServiceInterface, bundleService bundlesvc.BundleServiceInterface, catalogProvider *catalogpkg.CatalogProvider) *gin.Engine {
	if mode := os.Getenv("GIN_MODE"); mode != "" {
		gin.SetMode(mode)
	}
	router := gin.Default()

	// Apply RequestID middleware to all routes
	router.Use(middleware.RequestIDMiddleware())

	// Health check endpoint
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	// Expose /health for liveness probes
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	// Swagger documentation endpoint
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	authHandler := handlers.NewAuthHandler(authSvc)
	catalogHandler := handlers.NewCatalogHandler(catalogProvider)
	resourcesHandler := handlers.NewResourcesHandler()
	applicationHandler := handlers.NewApplicationHandler(appService)
	bundleHandler := handlers.NewBundleHandler(bundleService)

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", authHandler.Login)
		v1.POST("/auth/logout", middleware.AuthMiddleware(tokenMgr, blacklist), authHandler.Logout)
		v1.POST("/auth/refresh", authHandler.Refresh)
		v1.GET("/auth/me", middleware.AuthMiddleware(tokenMgr, blacklist), authHandler.Me)
	}

	// Catalog endpoints
	catalog := v1.Group("")
	catalog.Use(middleware.AuthMiddleware(tokenMgr, blacklist))
	{
		catalog.GET("/resources", resourcesHandler.GetResources)
		catalog.GET("/architectures", catalogHandler.ListArchitectures)
		catalog.GET("/architectures/:id", catalogHandler.GetArchitectureDetails)
		catalog.GET("/architectures/:id/deploy-options", catalogHandler.GetArchitectureDeployOptions)
		catalog.GET("/architectures/:id/images", catalogHandler.GetArchitectureImages)
		catalog.GET("/architectures/:id/models", catalogHandler.GetArchitectureModels)
		catalog.GET("/services", catalogHandler.ListServices)
		catalog.GET("/services/:id", catalogHandler.GetServiceDetails)
		catalog.GET("/services/:id/deploy-options", catalogHandler.GetServiceDeployOptions)
		catalog.GET("/services/:id/params", catalogHandler.GetServiceParams)
		catalog.GET("/services/:id/images", catalogHandler.GetServiceImages)
		catalog.GET("/services/:id/models", catalogHandler.GetServiceModels)
		catalog.GET("/services/:id/md", catalogHandler.GetServiceMD)
		catalog.GET("/components/:component_type/providers/:provider_id/params", catalogHandler.GetComponentProviderParams)
	}

	applications := v1.Group("applications")
	applications.Use(middleware.AuthMiddleware(tokenMgr, blacklist))
	{
		applications.GET("/", applicationHandler.ListApplications)
		applications.GET("/:id", applicationHandler.GetApplicationByID)
		applications.GET("/:id/resources", applicationHandler.GetApplicationResources)
		applications.POST("/", applicationHandler.CreateApplication)
		applications.PUT("/:id", applicationHandler.UpdateApplication)
		applications.DELETE("/:id", applicationHandler.DeleteApplication)
		applications.GET("/:id/ps", applicationHandler.ApplicationPS)
	}

	bundles := v1.Group("catalog/bundles")
	bundles.Use(middleware.AuthMiddleware(tokenMgr, blacklist))
	{
		bundles.GET("", bundleHandler.ListBundles)
		bundles.POST("", bundleHandler.UploadBundle)
		bundles.GET("/:bundle_id", bundleHandler.GetBundle)
		bundles.PUT("/:bundle_id", bundleHandler.UpdateBundle)
		bundles.DELETE("/:bundle_id", bundleHandler.DeleteBundle)
	}

	return router
}
