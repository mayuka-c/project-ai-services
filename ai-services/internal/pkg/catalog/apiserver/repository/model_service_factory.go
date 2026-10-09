package repository

import (
	"github.com/project-ai-services/ai-services/internal/pkg/catalog"
	modelservice "github.com/project-ai-services/ai-services/internal/pkg/catalog/apiserver/repository/model_service"
	dbrepo "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/repository"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime"
	"github.com/project-ai-services/ai-services/internal/pkg/worker/stream"
)

// NewModelService creates the ModelServiceInterface wired with all required dependencies.
// runtimeFactory is used to create the local Podman runtime on first deploy; workerRegistry
// is used to forward deploys to remote workers over gRPC.
func NewModelService(
	componentRepo dbrepo.ComponentRepository,
	connectorRepo dbrepo.ConnectorRepository,
	keyRepo dbrepo.KeyRepository,
	workerRepo dbrepo.WorkerRepository,
	provider *catalog.CatalogProvider,
	runtimeFactory *runtime.RuntimeFactory,
	workerRegistry stream.WorkerRegistry,
) ModelServiceInterface {
	return modelservice.NewModelService(componentRepo, connectorRepo, keyRepo, workerRepo, provider, runtimeFactory, workerRegistry)
}

// Made with Bob
