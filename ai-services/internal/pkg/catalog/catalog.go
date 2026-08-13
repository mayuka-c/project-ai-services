package catalog

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	texttemplate "text/template"

	"github.com/project-ai-services/ai-services/assets"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/constants"
	dbrepo "github.com/project-ai-services/ai-services/internal/pkg/catalog/db/repository"
	"github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
	clitemplates "github.com/project-ai-services/ai-services/internal/pkg/cli/templates"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	runtimeTypes "github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/utils"
	"github.com/project-ai-services/ai-services/internal/pkg/vars"
	"go.yaml.in/yaml/v3"
)

// catalogItem represents a cached catalog item with its metadata and path.
// itemFS is the filesystem used to read this item's files:
//   - assets.CatalogFS for built-in embedded items
//   - os.DirFS(bundleDir) for customer-created on-disk bundles
type catalogItem struct {
	Path         string // Application path (e.g., "services/chat" or "my-service")
	itemFS       fs.FS  // FS to read files from — either embedded or on-disk
	Architecture *types.Architecture
	Service      *types.Service
	Component    *types.Component
}

// CatalogProvider provides access to catalog items.
// It holds a mutex-protected items map that is rebuilt on Reload().
type CatalogProvider struct {
	mu         sync.RWMutex
	items      map[string]*catalogItem
	bundleRepo dbrepo.BundleRepository // nil for CLI paths without DB access
}

// NewCatalogProvider creates a new CatalogProvider, loading all embedded catalog items
// and any active customer-created bundles from the DB (if bundleRepo is non-nil).
func NewCatalogProvider(bundleRepo dbrepo.BundleRepository) (*CatalogProvider, error) {
	p := &CatalogProvider{
		items:      make(map[string]*catalogItem),
		bundleRepo: bundleRepo,
	}

	if err := p.load(context.Background()); err != nil {
		return nil, err
	}

	return p, nil
}

// Reload rebuilds the items map from scratch: re-reads the embedded FS and re-queries
// all active bundle paths from the DB. Safe to call concurrently with reads.
func (p *CatalogProvider) Reload(ctx context.Context) error {
	fresh := make(map[string]*catalogItem)

	if err := loadEmbeddedItems(ctx, fresh); err != nil {
		return err
	}

	if p.bundleRepo != nil {
		if err := p.loadBundleItems(ctx, fresh); err != nil {
			return err
		}
	}

	p.mu.Lock()
	p.items = fresh
	p.mu.Unlock()

	return nil
}

// load is the internal initial load — same as Reload but sets items directly.
func (p *CatalogProvider) load(ctx context.Context) error {
	if err := loadEmbeddedItems(ctx, p.items); err != nil {
		return err
	}

	if p.bundleRepo != nil {
		if err := p.loadBundleItems(ctx, p.items); err != nil {
			return err
		}
	}

	return nil
}

// loadBundleItems queries all active bundle rows from the DB and loads their metadata
// into the items map using os.DirFS rooted at the bundle's on-disk directory.
func (p *CatalogProvider) loadBundleItems(ctx context.Context, items map[string]*catalogItem) error {
	bundles, err := p.bundleRepo.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("failed to list active bundles: %w", err)
	}

	for _, b := range bundles {
		if b.Status != "active" {
			continue
		}

		// Bundle directory: <bundleStorageRoot>/<catalog_type>/<catalog_id>-<version>
		// e.g. /data/catalog-bundles/service/mayuka-service-1.0.0
		//      /data/catalog-bundles/component/llm--my-provider-1.0.0
		// The archive is extracted with the top-level directory stripped, so
		// metadata.yaml sits directly at <bundleDir>/metadata.yaml.
		bundleDir := filepath.Join(bundleStorageRoot, b.CatalogType, b.CatalogID+"-"+b.Version)
		bundleFS := os.DirFS(bundleDir)

		metaPath := "metadata.yaml"
		data, err := fs.ReadFile(bundleFS, metaPath)
		if err != nil {
			logger.WarningfCtx(ctx, "bundle %s: failed to read metadata.yaml at %s: %v", b.ID, metaPath, err)

			continue
		}

		// parseAndStoreMetadataWithFS dispatches on the plural path prefix used in the
		// embedded FS ("services", "components", "architectures"). The DB stores the
		// singular form ("service", "component"), so we append "s" to convert.
		catalogType := b.CatalogType + "s" // "service" → "services", "component" → "components"
		appPath := "."                     // bundle FS root contains the service/component directly

		if err := parseAndStoreMetadataWithFS(ctx, catalogType, metaPath, appPath, bundleFS, data, items); err != nil {
			logger.WarningfCtx(ctx, "bundle %s: failed to parse metadata: %v", b.ID, err)
		}
	}

	return nil
}

// bundleStorageRoot is duplicated here to avoid a circular import with the bundle package.
// It matches the value in internal/pkg/catalog/apiserver/services/bundle/service.go.
const bundleStorageRoot = "/data/catalog-bundles"

// loadEmbeddedItems walks assets.CatalogFS and stores all embedded catalog items.
func loadEmbeddedItems(ctx context.Context, items map[string]*catalogItem) error {
	err := fs.WalkDir(&assets.CatalogFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || filepath.Base(path) != "metadata.yaml" {
			return nil
		}

		return processEmbeddedMetadataFile(ctx, path, items)
	})

	if err != nil {
		return fmt.Errorf("failed to walk catalog filesystem: %w", err)
	}

	return nil
}

// processEmbeddedMetadataFile processes a single metadata.yaml from assets.CatalogFS.
func processEmbeddedMetadataFile(ctx context.Context, path string, items map[string]*catalogItem) error {
	parts := strings.Split(path, "/")
	if len(parts) < constants.MinPathPartsForArchOrService {
		return nil
	}

	catalogType := parts[0]

	if !isValidMetadataPath(catalogType, len(parts)) {
		return nil
	}

	data, readErr := assets.CatalogFS.ReadFile(path)
	if readErr != nil {
		logger.DebugfCtx(ctx, "failed to read metadata at %s: %v", path, readErr)

		return nil
	}

	appPath := filepath.Dir(path)

	return parseAndStoreMetadataWithFS(ctx, catalogType, path, appPath, &assets.CatalogFS, data, items)
}

// isValidMetadataPath checks if the metadata file path is valid for the catalog type.
func isValidMetadataPath(catalogType string, pathLength int) bool {
	switch catalogType {
	case constants.CatalogTypeArchitectures, constants.CatalogTypeServices:
		return pathLength == constants.MinPathPartsForArchOrService
	case constants.CatalogTypeComponents:
		return pathLength == constants.MinPathPartsForComponent
	default:
		return false
	}
}

// parseAndStoreMetadataWithFS parses metadata and stores it with the given FS reference.
func parseAndStoreMetadataWithFS(ctx context.Context, catalogType, path, appPath string, itemFS fs.FS, data []byte, items map[string]*catalogItem) error {
	switch catalogType {
	case constants.CatalogTypeArchitectures:
		return parseArchitecture(ctx, path, appPath, itemFS, data, items)
	case constants.CatalogTypeServices:
		return parseService(ctx, path, appPath, itemFS, data, items)
	case constants.CatalogTypeComponents:
		return parseComponent(ctx, path, appPath, itemFS, data, items)
	}

	return nil
}

// parseArchitecture parses and stores an architecture.
func parseArchitecture(ctx context.Context, path, appPath string, itemFS fs.FS, data []byte, items map[string]*catalogItem) error {
	var arch types.Architecture
	if unmarshalErr := yaml.Unmarshal(data, &arch); unmarshalErr != nil {
		logger.DebugfCtx(ctx, "failed to parse architecture at %s: %v", path, unmarshalErr)

		return nil
	}

	items[arch.ID] = &catalogItem{
		Path:         appPath,
		itemFS:       itemFS,
		Architecture: &arch,
	}

	return nil
}

// parseService parses and stores a service.
func parseService(ctx context.Context, path, appPath string, itemFS fs.FS, data []byte, items map[string]*catalogItem) error {
	var svc types.Service
	if unmarshalErr := yaml.Unmarshal(data, &svc); unmarshalErr != nil {
		logger.DebugfCtx(ctx, "failed to parse service at %s: %v", path, unmarshalErr)

		return nil
	}

	items[svc.ID] = &catalogItem{
		Path:    appPath,
		itemFS:  itemFS,
		Service: &svc,
	}

	return nil
}

// parseComponent parses and stores a component.
func parseComponent(ctx context.Context, path, appPath string, itemFS fs.FS, data []byte, items map[string]*catalogItem) error {
	var comp types.Component
	if unmarshalErr := yaml.Unmarshal(data, &comp); unmarshalErr != nil {
		logger.DebugfCtx(ctx, "failed to parse component at %s: %v", path, unmarshalErr)

		return nil
	}

	componentKey := fmt.Sprintf("%s/%s", comp.ComponentType, comp.ID)
	items[componentKey] = &catalogItem{
		Path:      appPath,
		itemFS:    itemFS,
		Component: &comp,
	}

	return nil
}

// ---- Read methods -----------------------------------------------------------

// getItem returns the item for the given key under a read lock.
func (p *CatalogProvider) getItem(key string) (*catalogItem, bool) {
	p.mu.RLock()
	item, ok := p.items[key]
	p.mu.RUnlock()

	return item, ok
}

// LoadArchitecture loads an architecture by ID from cache.
func (p *CatalogProvider) LoadArchitecture(id string) (*types.Architecture, error) {
	item, ok := p.getItem(id)
	if !ok || item.Architecture == nil {
		return nil, fmt.Errorf("architecture '%s' not found", id)
	}

	return item.Architecture, nil
}

// LoadService loads a service by ID from cache.
func (p *CatalogProvider) LoadService(id string) (*types.Service, error) {
	item, ok := p.getItem(id)
	if !ok || item.Service == nil {
		return nil, fmt.Errorf("service '%s' not found", id)
	}

	return item.Service, nil
}

// LoadComponent loads a component by component type and ID from cache.
func (p *CatalogProvider) LoadComponent(componentType, id string) (*types.Component, error) {
	componentKey := fmt.Sprintf("%s/%s", componentType, id)
	item, ok := p.getItem(componentKey)
	if !ok || item.Component == nil {
		return nil, fmt.Errorf("component '%s/%s' not found", componentType, id)
	}

	return item.Component, nil
}

// GetCatalogItemPath returns the application path for a given ID.
func (p *CatalogProvider) GetCatalogItemPath(id string) (string, error) {
	item, ok := p.getItem(id)
	if !ok {
		return "", fmt.Errorf("item '%s' not found", id)
	}

	return item.Path, nil
}

// getItemWithFS returns the item and its associated FS for the given key.
func (p *CatalogProvider) getItemWithFS(key string) (*catalogItem, error) {
	item, ok := p.getItem(key)
	if !ok {
		return nil, fmt.Errorf("item '%s' not found in catalog", key)
	}

	return item, nil
}

// ToServiceSummary converts a Service to ServiceSummary.
func ToServiceSummary(service *types.Service) types.ServiceSummary {
	return types.ServiceSummary{
		ID:            service.ID,
		Name:          service.Name,
		Description:   service.Description,
		CertifiedBy:   service.CertifiedBy,
		Architectures: service.Architectures,
		Standalone:    service.Standalone,
	}
}

// ToArchitectureSummary converts an Architecture to ArchitectureSummary.
func ToArchitectureSummary(arch *types.Architecture) types.ArchitectureSummary {
	services := make([]string, len(arch.Services))
	for i, svc := range arch.Services {
		services[i] = svc.ID
	}

	return types.ArchitectureSummary{
		ID:          arch.ID,
		Name:        arch.Name,
		Description: arch.Description,
		CertifiedBy: arch.CertifiedBy,
		Services:    services,
	}
}

// ToComponentSummary converts a Component to ComponentSummary.
func ToComponentSummary(component *types.Component) types.ComponentSummary {
	return types.ComponentSummary{
		ID:            component.ID,
		Name:          component.Name,
		Description:   component.Description,
		ComponentType: component.ComponentType,
	}
}

// ListArchitectures lists all available architectures from cache.
func (p *CatalogProvider) ListArchitectures() ([]types.Architecture, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	architectures := make([]types.Architecture, 0)
	for _, item := range p.items {
		if item.Architecture != nil {
			architectures = append(architectures, *item.Architecture)
		}
	}

	return architectures, nil
}

// ListServices lists all available services from cache.
func (p *CatalogProvider) ListServices() ([]types.Service, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	services := make([]types.Service, 0)
	for _, item := range p.items {
		if item.Service != nil {
			services = append(services, *item.Service)
		}
	}

	return services, nil
}

// ListComponents lists all available components from cache.
func (p *CatalogProvider) ListComponents() ([]types.Component, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	components := make([]types.Component, 0)
	for _, item := range p.items {
		if item.Component != nil {
			components = append(components, *item.Component)
		}
	}

	return components, nil
}

// ListServicesWithRuntime lists all available deployable services.
// Runtime parameter kept for API compatibility but not used.
func (p *CatalogProvider) ListServicesWithRuntime(_ runtimeTypes.RuntimeType) ([]types.Service, error) {
	return p.ListServices()
}

// ArchitectureExists checks if an architecture exists.
func (p *CatalogProvider) ArchitectureExists(id string) bool {
	_, err := p.LoadArchitecture(id)

	return err == nil
}

// ServiceExists checks if a service exists.
func (p *CatalogProvider) ServiceExists(id string) bool {
	_, err := p.LoadService(id)

	return err == nil
}

// ComponentExists checks if a component exists.
func (p *CatalogProvider) ComponentExists(componentType, id string) bool {
	_, err := p.LoadComponent(componentType, id)

	return err == nil
}

// ResolveServiceDependencies resolves all dependencies for one or more services recursively.
func (p *CatalogProvider) ResolveServiceDependencies(services ...interface{}) ([]string, error) {
	visited := make(map[string]bool)
	var result []string

	for _, svc := range services {
		var serviceID string
		switch v := svc.(type) {
		case string:
			serviceID = v
		case types.ServiceReference:
			serviceID = v.ID
		default:
			return nil, fmt.Errorf("invalid service type: %T", svc)
		}

		if err := p.resolveDependenciesRecursive(serviceID, visited, &result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// resolveDependenciesRecursive performs depth-first traversal of dependencies.
func (p *CatalogProvider) resolveDependenciesRecursive(serviceID string, visited map[string]bool, result *[]string) error {
	if visited[serviceID] {
		return nil
	}

	service, err := p.LoadService(serviceID)
	if err != nil {
		return fmt.Errorf("failed to load service '%s': %w", serviceID, err)
	}

	visited[serviceID] = true

	for _, dep := range service.Dependencies {
		if err := p.resolveDependenciesRecursive(dep.ID, visited, result); err != nil {
			return err
		}
	}

	*result = append(*result, serviceID)

	return nil
}

// GetDeploymentOrder returns services grouped into deployment layers.
func (p *CatalogProvider) GetDeploymentOrder(serviceIDs []string) ([][]string, error) {
	graph, inDegree, err := p.buildDependencyGraph(serviceIDs)
	if err != nil {
		return nil, err
	}

	layers := performTopologicalSort(graph, inDegree)

	if err := validateNoCircularDependencies(layers, serviceIDs); err != nil {
		return nil, err
	}

	return layers, nil
}

// buildDependencyGraph creates a dependency graph for the given services.
func (p *CatalogProvider) buildDependencyGraph(serviceIDs []string) (map[string][]string, map[string]int, error) {
	graph := make(map[string][]string)
	inDegree := make(map[string]int)

	for _, svcID := range serviceIDs {
		if _, exists := graph[svcID]; !exists {
			graph[svcID] = []string{}
			inDegree[svcID] = 0
		}
	}

	for _, svcID := range serviceIDs {
		service, err := p.LoadService(svcID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load service '%s': %w", svcID, err)
		}

		for _, dep := range service.Dependencies {
			if _, exists := graph[dep.ID]; exists {
				graph[dep.ID] = append(graph[dep.ID], svcID)
				inDegree[svcID]++
			}
		}
	}

	return graph, inDegree, nil
}

// performTopologicalSort performs Kahn's algorithm for topological sorting.
func performTopologicalSort(graph map[string][]string, inDegree map[string]int) [][]string {
	var layers [][]string
	queue := getServicesWithNoDependencies(inDegree)

	for len(queue) > 0 {
		currentLayer := make([]string, len(queue))
		copy(currentLayer, queue)
		layers = append(layers, currentLayer)

		queue = processLayer(queue, graph, inDegree)
	}

	return layers
}

// getServicesWithNoDependencies returns services with no dependencies.
func getServicesWithNoDependencies(inDegree map[string]int) []string {
	var queue []string
	for svcID, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, svcID)
		}
	}

	return queue
}

// processLayer processes a layer and returns the next queue.
func processLayer(queue []string, graph map[string][]string, inDegree map[string]int) []string {
	var nextQueue []string
	for _, svcID := range queue {
		for _, dependent := range graph[svcID] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				nextQueue = append(nextQueue, dependent)
			}
		}
	}

	return nextQueue
}

// validateNoCircularDependencies checks for circular dependencies.
func validateNoCircularDependencies(layers [][]string, serviceIDs []string) error {
	processedCount := 0
	for _, layer := range layers {
		processedCount += len(layer)
	}
	if processedCount != len(serviceIDs) {
		return fmt.Errorf("circular dependency detected in services")
	}

	return nil
}

// ValidateDependencies checks if all dependencies for given services exist.
func (p *CatalogProvider) ValidateDependencies(serviceIDs []string) error {
	for _, svcID := range serviceIDs {
		service, err := p.LoadService(svcID)
		if err != nil {
			return fmt.Errorf("service '%s' not found: %w", svcID, err)
		}

		for _, dep := range service.Dependencies {
			if !p.ServiceExists(dep.ID) {
				return fmt.Errorf("service '%s' requires dependency '%s' which does not exist", svcID, dep.ID)
			}
		}
	}

	return nil
}

// LoadServiceValues loads the values.yaml for a service with optional parameter overrides.
func (p *CatalogProvider) LoadServiceValues(serviceID string, argParams map[string]string) (map[string]any, error) {
	item, err := p.getItemWithFS(serviceID)
	if err != nil || item.Service == nil {
		return nil, fmt.Errorf("service not found: %s", serviceID)
	}

	runtime := vars.RuntimeFactory.GetRuntimeType()
	valuesPath := filepath.Join(item.Path, string(runtime), "values.yaml")

	valuesData, err := fs.ReadFile(item.itemFS, valuesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read values.yaml at %s: %w", valuesPath, err)
	}

	processedData, err := utils.ProcessGenerateAnnotationsFromYAML(valuesData)
	if err != nil {
		return nil, fmt.Errorf("failed to process generate annotations: %w", err)
	}

	values := make(map[string]any)
	if err := yaml.Unmarshal(processedData, &values); err != nil {
		return nil, fmt.Errorf("failed to parse values.yaml: %w", err)
	}

	for key, val := range argParams {
		utils.SetNestedValue(values, key, val)
	}

	return values, nil
}

// LoadComponentValues loads the values.yaml for a component with optional parameter overrides.
func (p *CatalogProvider) LoadComponentValues(componentType, providerID string, argParams map[string]string) (map[string]any, error) {
	componentKey := fmt.Sprintf("%s/%s", componentType, providerID)

	item, err := p.getItemWithFS(componentKey)
	if err != nil || item.Component == nil {
		return nil, fmt.Errorf("component not found: %s/%s", componentType, providerID)
	}

	runtime := vars.RuntimeFactory.GetRuntimeType()
	valuesPath := filepath.Join(item.Path, string(runtime), "values.yaml")

	valuesData, err := fs.ReadFile(item.itemFS, valuesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read values.yaml at %s: %w", valuesPath, err)
	}

	processedData, err := utils.ProcessGenerateAnnotationsFromYAML(valuesData)
	if err != nil {
		return nil, fmt.Errorf("failed to process generate annotations: %w", err)
	}

	values := make(map[string]any)
	if err := yaml.Unmarshal(processedData, &values); err != nil {
		return nil, fmt.Errorf("failed to parse values.yaml: %w", err)
	}

	for key, val := range argParams {
		utils.SetNestedValue(values, key, val)
	}

	return values, nil
}

// LoadComponentRuntimeMetadata loads runtime-specific metadata for a component.
func (p *CatalogProvider) LoadComponentRuntimeMetadata(componentType, providerID string) (*clitemplates.AppMetadata, error) {
	componentKey := fmt.Sprintf("%s/%s", componentType, providerID)

	item, err := p.getItemWithFS(componentKey)
	if err != nil || item.Component == nil {
		return nil, fmt.Errorf("component not found: %s/%s", componentType, providerID)
	}

	runtime := vars.RuntimeFactory.GetRuntimeType()
	metadataPath := filepath.Join(item.Path, string(runtime), "metadata.yaml")

	metadataData, err := fs.ReadFile(item.itemFS, metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read runtime metadata %s: %w", metadataPath, err)
	}

	var metadata clitemplates.AppMetadata
	if err := yaml.Unmarshal(metadataData, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse runtime metadata: %w", err)
	}

	return &metadata, nil
}

// LoadComponentTemplates loads all pod templates for a component.
func (p *CatalogProvider) LoadComponentTemplates(componentType, providerID string) (map[string]*texttemplate.Template, error) {
	componentKey := fmt.Sprintf("%s/%s", componentType, providerID)

	item, err := p.getItemWithFS(componentKey)
	if err != nil || item.Component == nil {
		return nil, fmt.Errorf("component not found: %s/%s", componentType, providerID)
	}

	runtime := vars.RuntimeFactory.GetRuntimeType()
	catalogPath := filepath.Join(item.Path, string(runtime), "templates")

	return loadTemplatesFromFS(item.itemFS, catalogPath, ".tmpl")
}

// LoadServiceRuntimeMetadata loads runtime-specific metadata for a service.
func (p *CatalogProvider) LoadServiceRuntimeMetadata(serviceID string) (*clitemplates.AppMetadata, error) {
	item, err := p.getItemWithFS(serviceID)
	if err != nil || item.Service == nil {
		return nil, fmt.Errorf("service not found: %s", serviceID)
	}

	runtime := vars.RuntimeFactory.GetRuntimeType()
	metadataPath := filepath.Join(item.Path, string(runtime), "metadata.yaml")

	metadataData, err := fs.ReadFile(item.itemFS, metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read runtime metadata %s: %w", metadataPath, err)
	}

	var metadata clitemplates.AppMetadata
	if err := yaml.Unmarshal(metadataData, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse runtime metadata: %w", err)
	}

	return &metadata, nil
}

// LoadServiceTemplates loads all pod templates for a service.
func (p *CatalogProvider) LoadServiceTemplates(serviceID string) (map[string]*texttemplate.Template, error) {
	item, err := p.getItemWithFS(serviceID)
	if err != nil || item.Service == nil {
		return nil, fmt.Errorf("service not found: %s", serviceID)
	}

	runtime := vars.RuntimeFactory.GetRuntimeType()
	catalogPath := filepath.Join(item.Path, string(runtime), "templates")

	return loadTemplatesFromFS(item.itemFS, catalogPath, ".tmpl")
}

// LoadServicesMD loads all step markdown files for a service.
func (p *CatalogProvider) LoadServicesMD(serviceID string) (map[string]*texttemplate.Template, error) {
	item, err := p.getItemWithFS(serviceID)
	if err != nil || item.Service == nil {
		return nil, fmt.Errorf("service not found: %s", serviceID)
	}

	runtime := vars.RuntimeFactory.GetRuntimeType()
	catalogPath := filepath.Join(item.Path, string(runtime), "steps")

	return loadTemplatesFromFS(item.itemFS, catalogPath, ".md")
}

// loadTemplatesFromFS walks the given FS under catalogPath, loading files with the
// given suffix as text templates. Returns an error if no matching files are found.
func loadTemplatesFromFS(itemFS fs.FS, catalogPath, suffix string) (map[string]*texttemplate.Template, error) {
	templates := make(map[string]*texttemplate.Template)

	err := fs.WalkDir(itemFS, catalogPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil
		}

		templateData, err := fs.ReadFile(itemFS, path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}

		templateName := filepath.Base(path)
		tmpl, err := texttemplate.New(templateName).Parse(string(templateData))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", templateName, err)
		}

		templates[templateName] = tmpl

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to load templates from %s: %w", catalogPath, err)
	}

	if len(templates) == 0 {
		return nil, fmt.Errorf("no %s files found in %s", suffix, catalogPath)
	}

	return templates, nil
}

