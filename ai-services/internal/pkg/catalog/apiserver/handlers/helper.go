package handlers

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"text/template"

	"github.com/project-ai-services/ai-services/assets"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/helpers"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/podman"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/templates"
	"github.com/project-ai-services/ai-services/internal/pkg/constants"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/models"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime"
	"gopkg.in/yaml.v3"
)

// ComponentInfo holds the information derived from a deployed component
type ComponentInfo struct {
	Endpoint string
	Domain   string
	Port     string
	Model    string
}

// deployApplication deploys services and components based on the request
func (h *ApplicationHandler) deployApplication(ctx context.Context, req *createApplicationReq) error {
	logger.Infof("Deploying application '%s' with template '%s'\n", req.Name, req.Template)

	// Create runtime client
	runtimeFactory := runtime.NewRuntimeFactory(h.runtimeType)
	runtimeClient, err := runtimeFactory.Create(req.Name)
	if err != nil {
		return fmt.Errorf("failed to create runtime client: %w", err)
	}

	// Template provider for loading component/service templates
	tp := templates.NewEmbedTemplateProvider(&assets.ApplicationFS)

	// Parse global params into a map for easy lookup
	globalParams := h.parseGlobalParams(req.Params)

	// Determine if template is an architecture or a service
	isArchitecture, err := h.isArchitectureTemplate(req.Template)
	if err != nil {
		return fmt.Errorf("failed to determine template type: %w", err)
	}

	if isArchitecture {
		logger.Infof("Template '%s' is an architecture, resolving service dependencies\n", req.Template)
		// For architecture: resolve services and their component dependencies
		return h.deployArchitecture(ctx, runtimeClient, tp, req, globalParams)
	} else {
		logger.Infof("Template '%s' is a service, resolving component dependencies\n", req.Template)
		// For service: resolve component dependencies only
		return h.deployServiceTemplate(ctx, runtimeClient, tp, req, globalParams)
	}
}

// parseGlobalParams converts the params array into a map for easy lookup
func (h *ApplicationHandler) parseGlobalParams(params []ArchitectureParam) map[string]map[string]interface{} {
	globalParams := make(map[string]map[string]interface{})

	for _, param := range params {
		key := fmt.Sprintf("%s_%s", param.ComponentType, param.ProviderID)
		globalParams[key] = param.Config
	}

	return globalParams
}

// isArchitectureTemplate checks if the template is an architecture or a service
func (h *ApplicationHandler) isArchitectureTemplate(templateName string) (bool, error) {
	// Check if template exists in architectures directory
	archPath := filepath.Join("architectures", templateName, "metadata.yaml")
	if _, err := assets.CatalogFS.ReadFile(archPath); err == nil {
		return true, nil
	}

	// Check if template exists in services directory
	servicePath := filepath.Join("services", templateName, "metadata.yaml")
	if _, err := assets.CatalogFS.ReadFile(servicePath); err == nil {
		return false, nil
	}

	return false, fmt.Errorf("template '%s' not found in architectures or services", templateName)
}

// deployArchitecture deploys an architecture with all its services and components
func (h *ApplicationHandler) deployArchitecture(ctx context.Context, runtimeClient runtime.Runtime, tp templates.Template, req *createApplicationReq, globalParams map[string]map[string]interface{}) error {
	logger.Infof("Deploying architecture: %s\n", req.Template)

	// Load architecture metadata to get service dependencies
	archPath := filepath.Join("architectures", req.Template, "metadata.yaml")
	archData, err := assets.CatalogFS.ReadFile(archPath)
	if err != nil {
		return fmt.Errorf("failed to read architecture metadata: %w", err)
	}

	var archMetadata struct {
		ID       string `yaml:"id"`
		Name     string `yaml:"name"`
		Services []struct {
			ID       string `yaml:"id"`
			Version  string `yaml:"version"`
			Optional bool   `yaml:"optional"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(archData, &archMetadata); err != nil {
		return fmt.Errorf("failed to parse architecture metadata: %w", err)
	}

	// Extract service IDs for logging
	serviceIDs := make([]string, len(archMetadata.Services))
	for i, svc := range archMetadata.Services {
		serviceIDs[i] = svc.ID
	}
	logger.Infof("Architecture '%s' requires services: %v\n", req.Template, serviceIDs)

	// Resolve component dependencies: separate global vs service-specific components
	globalComponents, serviceComponents := h.resolveComponentDependencies(req)

	fmt.Println("Global Components: ", globalComponents)
	fmt.Println("Service Components: ", serviceComponents)

	// Calculate and allocate Spyre cards
	pool, err := h.calculateAndAllocateSpyreCards(ctx, runtimeClient, tp, req.Name, globalComponents, serviceComponents)
	if err != nil {
		return fmt.Errorf("failed to allocate Spyre cards: %w", err)
	}

	// Deploy global components first
	globalComponentInfos := make(map[string]*ComponentInfo)
	for key, component := range globalComponents {
		logger.Infof("Deploying global component: %s/%s\n", component.ComponentType, component.ProviderID)
		info, err := h.deployComponent(ctx, runtimeClient, tp, req.Name, "global", component, nil, pool)
		if err != nil {
			return fmt.Errorf("failed to deploy global component %s: %w", key, err)
		}
		if info != nil {
			globalComponentInfos[component.ComponentType] = info
		}
	}

	// Deploy each service from the request with their service-specific components
	for _, service := range req.Services {
		if !service.Enabled {
			logger.Infof("Service '%s' is disabled, skipping deployment\n", service.ServiceID)
			continue
		}

		// Get service-specific components for this service
		serviceSpecificComponents := serviceComponents[service.ServiceID]

		if err := h.deployServiceWithResolvedComponents(ctx, runtimeClient, tp, req.Name, &service, serviceSpecificComponents, globalComponentInfos, pool); err != nil {
			return fmt.Errorf("failed to deploy service %s: %w", service.ServiceID, err)
		}
	}

	logger.Infof("Architecture '%s' deployed successfully\n", req.Template)
	return nil
}

var envMutex sync.Mutex

// SpyreCardPool manages allocation of PCI addresses to components
type SpyreCardPool struct {
	addresses []string
	mutex     sync.Mutex
}

// Allocate takes n addresses from the pool and returns them
func (p *SpyreCardPool) Allocate(n int) ([]string, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.addresses) < n {
		return nil, fmt.Errorf("insufficient Spyre cards in pool: need %d, have %d", n, len(p.addresses))
	}

	allocated := make([]string, n)
	copy(allocated, p.addresses[:n])
	p.addresses = p.addresses[n:]

	return allocated, nil
}

// calculateAndAllocateSpyreCards calculates required Spyre cards and creates allocation pool
func (h *ApplicationHandler) calculateAndAllocateSpyreCards(
	ctx context.Context,
	runtimeClient runtime.Runtime,
	tp templates.Template,
	appName string,
	globalComponents map[string]*ComponentParam,
	serviceComponents map[string][]*ComponentParam,
) (*SpyreCardPool, error) {
	// Calculate total required Spyre cards from all components
	totalRequired := 0

	// Check global components
	for _, component := range globalComponents {
		required, err := h.getRequiredSpyreCardsForComponent(tp, appName, component)
		if err != nil {
			return nil, fmt.Errorf("failed to get Spyre card requirements for global component %s: %w", component.ComponentType, err)
		}
		totalRequired += required
		if required > 0 {
			logger.Infof("Global component %s/%s requires %d Spyre cards\n", component.ComponentType, component.ProviderID, required)
		}
	}

	// Check service-specific components
	for serviceID, components := range serviceComponents {
		for _, component := range components {
			required, err := h.getRequiredSpyreCardsForComponent(tp, appName, component)
			if err != nil {
				return nil, fmt.Errorf("failed to get Spyre card requirements for service %s component %s: %w", serviceID, component.ComponentType, err)
			}
			totalRequired += required
			if required > 0 {
				logger.Infof("Service %s component %s/%s requires %d Spyre cards\n", serviceID, component.ComponentType, component.ProviderID, required)
			}
		}
	}

	if totalRequired == 0 {
		logger.Infof("No Spyre cards required for this deployment\n")
		return nil, nil
	}

	logger.Infof("Total Spyre cards required: %d\n", totalRequired)

	// Find available Spyre cards
	pciAddresses, err := helpers.FindFreeSpyreCards()
	if err != nil {
		return nil, fmt.Errorf("failed to find free Spyre cards: %w", err)
	}

	availableCount := len(pciAddresses)
	logger.Infof("Available Spyre cards: %d\n", availableCount)

	// Validate we have enough Spyre cards
	if availableCount < totalRequired {
		return nil, fmt.Errorf("insufficient Spyre cards: required %d, available %d", totalRequired, availableCount)
	}

	// Create pool with available addresses
	pool := &SpyreCardPool{
		addresses: pciAddresses,
	}

	return pool, nil
}

// getRequiredSpyreCardsForComponent calculates Spyre cards needed for a component
func (h *ApplicationHandler) getRequiredSpyreCardsForComponent(tp templates.Template, appName string, component *ComponentParam) (int, error) {
	// Load component template to check annotations
	componentPath := filepath.Join("components", component.ComponentType, component.ProviderID, "podman")
	templateFiles, err := assets.CatalogFS.ReadDir(filepath.Join(componentPath, "templates"))
	if err != nil {
		return 0, fmt.Errorf("failed to read component templates: %w", err)
	}

	totalSpyreCards := 0

	for _, file := range templateFiles {
		if file.IsDir() {
			continue
		}

		templatePath := filepath.Join(componentPath, "templates", file.Name())
		templateData, err := assets.CatalogFS.ReadFile(templatePath)
		if err != nil {
			continue
		}

		// Parse template
		tmpl, err := template.New(file.Name()).Parse(string(templateData))
		if err != nil {
			continue
		}

		// Prepare minimal params for rendering
		params := map[string]interface{}{
			"AppName":       appName,
			"ServiceID":     "temp",
			"Values":        component.Params,
			"ComponentType": component.ComponentType,
			"ProviderID":    component.ProviderID,
			"env":           map[string]map[string]string{},
		}

		// Render template
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, params); err != nil {
			continue
		}

		// Parse rendered YAML to get pod spec
		var podSpec models.PodSpec
		if err := yaml.Unmarshal(rendered.Bytes(), &podSpec); err != nil {
			continue
		}

		// Extract Spyre card requirements from annotations
		spyreCards, _, err := h.fetchSpyreCardsFromPodAnnotations(podSpec.Annotations)
		if err != nil {
			return 0, err
		}

		totalSpyreCards += spyreCards
	}

	return totalSpyreCards, nil
}

// fetchSpyreCardsFromPodAnnotations extracts Spyre card requirements from pod annotations
func (h *ApplicationHandler) fetchSpyreCardsFromPodAnnotations(annotations map[string]string) (int, map[string]int, error) {
	var spyreCards int
	spyreCardContainerMap := map[string]int{}

	spyreCardAnnotationRegex := regexp.MustCompile(`^ai-services\.io\/([A-Za-z0-9][-A-Za-z0-9_.]*)--spyre-cards$`)

	isSpyreCardAnnotation := func(annotation string) (string, bool) {
		matches := spyreCardAnnotationRegex.FindStringSubmatch(annotation)
		if matches == nil {
			return "", false
		}
		return matches[1], true
	}

	for annotationKey, val := range annotations {
		if containerName, ok := isSpyreCardAnnotation(annotationKey); ok {
			valInt, err := strconv.Atoi(val)
			if err != nil {
				return 0, spyreCardContainerMap, fmt.Errorf("failed to convert to int. Provided val: %s is not of int type", val)
			}
			spyreCardContainerMap[containerName] = valInt
			spyreCards += valInt
		}
	}

	return spyreCards, spyreCardContainerMap, nil
}

// getEnvParamsForComponent returns environment parameters for a component including Spyre card PCI addresses
// It allocates PCI addresses from the pool for this specific component
func (h *ApplicationHandler) getEnvParamsForComponent(podSpec *models.PodSpec, pool *SpyreCardPool) (map[string]map[string]string, error) {
	env := make(map[string]map[string]string)

	// Get container names from pod spec
	for _, container := range podSpec.Spec.Containers {
		env[container.Name] = make(map[string]string)
	}

	if pool == nil {
		return env, nil
	}

	// Fetch Spyre card requirements from annotations
	spyreCards, spyreCardContainerMap, err := h.fetchSpyreCardsFromPodAnnotations(podSpec.Annotations)
	if err != nil {
		return env, err
	}

	if spyreCards == 0 {
		return env, nil
	}

	// Allocate PCI addresses to containers that need them
	for containerName, spyreCount := range spyreCardContainerMap {
		if spyreCount != 0 {
			// Allocate addresses from the pool (thread-safe)
			allocatedAddresses, err := pool.Allocate(spyreCount)
			if err != nil {
				return env, fmt.Errorf("failed to allocate Spyre cards for container %s: %w", containerName, err)
			}

			// Join addresses with space separator
			pciAddressStr := ""
			for i, addr := range allocatedAddresses {
				if i > 0 {
					pciAddressStr += " "
				}
				pciAddressStr += addr
			}

			env[containerName][string(constants.PCIAddressKey)] = pciAddressStr

			logger.Infof("Allocated %d Spyre cards to container '%s' in pod '%s': %s\n",
				spyreCount, containerName, podSpec.Name, pciAddressStr)
		}
	}

	return env, nil
}

// deployServiceTemplate deploys a single service template with its components

// resolveComponentDependencies separates components into global and service-specific maps
// Global components are those in req.Params that are NOT present in ALL enabled services
// Service-specific components are those defined per service
func (h *ApplicationHandler) resolveComponentDependencies(req *createApplicationReq) (map[string]*ComponentParam, map[string][]*ComponentParam) {
	logger.Infof("Resolving component dependencies\n")

	// Build global components map from req.Params
	globalComponents := make(map[string]*ComponentParam)
	for i := range req.Params {
		param := &req.Params[i]
		key := fmt.Sprintf("%s_%s", param.ComponentType, param.ProviderID)
		globalComponents[key] = &ComponentParam{
			Type:          "component",
			ComponentType: param.ComponentType,
			ProviderID:    param.ProviderID,
			Params:        param.Config,
		}
		logger.Infof("Added global component: %s\n", key)
	}

	// Build service-specific components map
	serviceComponents := make(map[string][]*ComponentParam)

	// Track which component types are present in ALL enabled services
	componentInAllServices := make(map[string]bool)
	enabledServiceCount := 0

	// First pass: count enabled services and track component occurrences
	componentOccurrences := make(map[string]int)
	for _, service := range req.Services {
		if !service.Enabled {
			continue
		}
		enabledServiceCount++

		// Track which components this service has
		serviceHasComponent := make(map[string]bool)
		for i := range service.Components {
			component := &service.Components[i]
			key := fmt.Sprintf("%s_%s", component.ComponentType, component.ProviderID)
			serviceHasComponent[key] = true
		}

		// Increment occurrence count for each component in this service
		for key := range serviceHasComponent {
			componentOccurrences[key]++
		}
	}

	// Determine which components are in ALL enabled services
	for key, count := range componentOccurrences {
		if count == enabledServiceCount {
			componentInAllServices[key] = true
			logger.Infof("Component %s is present in all %d enabled services\n", key, enabledServiceCount)
		}
	}

	// Second pass: build service-specific component lists and remove from global if needed
	for _, service := range req.Services {
		if !service.Enabled {
			continue
		}

		var serviceSpecificComps []*ComponentParam
		for i := range service.Components {
			component := &service.Components[i]
			key := fmt.Sprintf("%s_%s", component.ComponentType, component.ProviderID)

			// Add to service-specific components
			serviceSpecificComps = append(serviceSpecificComps, component)

			// If this component is in ALL services, remove it from global
			if componentInAllServices[key] {
				if _, exists := globalComponents[key]; exists {
					logger.Infof("Removing %s from global components (present in all services)\n", key)
					delete(globalComponents, key)
				}
			}
		}

		serviceComponents[service.ServiceID] = serviceSpecificComps
	}

	logger.Infof("Resolved %d global components and %d services with specific components\n",
		len(globalComponents), len(serviceComponents))

	return globalComponents, serviceComponents
}

// deployServiceWithResolvedComponents deploys a service with both global and service-specific component info
func (h *ApplicationHandler) deployServiceWithResolvedComponents(
	ctx context.Context,
	runtimeClient runtime.Runtime,
	tp templates.Template,
	appName string,
	service *ServiceParam,
	serviceSpecificComponents []*ComponentParam,
	globalComponentInfos map[string]*ComponentInfo,
	pool *SpyreCardPool,
) error {
	logger.Infof("Deploying service '%s' with %d service-specific components and %d global components\n",
		service.ServiceID, len(serviceSpecificComponents), len(globalComponentInfos))

	// Start with global component infos
	allComponentInfos := make(map[string]*ComponentInfo)
	for componentType, info := range globalComponentInfos {
		allComponentInfos[componentType] = info
		logger.Infof("Using global component '%s': %s\n", componentType, info.Endpoint)
	}

	// Deploy service-specific components and add their info
	for _, component := range serviceSpecificComponents {
		info, err := h.deployComponent(ctx, runtimeClient, tp, appName, service.ServiceID, component, service.Params, pool)
		if err != nil {
			return fmt.Errorf("failed to deploy service-specific component %s: %w", component.ComponentType, err)
		}

		if info != nil {
			// Service-specific component info overrides global
			allComponentInfos[component.ComponentType] = info
			logger.Infof("Service-specific component '%s' info - Endpoint: %s, Model: %s\n",
				component.ComponentType, info.Endpoint, info.Model)
		}
	}

	// Deploy the service itself with all component information
	if err := h.deployService(ctx, runtimeClient, tp, appName, service, allComponentInfos); err != nil {
		return fmt.Errorf("failed to deploy service %s: %w", service.ServiceID, err)
	}

	return nil
}
func (h *ApplicationHandler) deployServiceTemplate(ctx context.Context, runtimeClient runtime.Runtime, tp templates.Template, req *createApplicationReq, globalParams map[string]map[string]interface{}) error {
	logger.Infof("Deploying service template: %s\n", req.Template)

	// For service template, there should be exactly one service in the request
	if len(req.Services) != 1 {
		return fmt.Errorf("service template deployment requires exactly one service, got %d", len(req.Services))
	}

	service := req.Services[0]
	if !service.Enabled {
		return fmt.Errorf("service '%s' is disabled", service.ServiceID)
	}

	// Merge global params with service-specific component params
	mergedService := h.mergeServiceParams(&service, globalParams)

	if err := h.deployServiceWithComponents(ctx, runtimeClient, tp, req.Name, mergedService); err != nil {
		return fmt.Errorf("failed to deploy service %s: %w", service.ServiceID, err)
	}

	logger.Infof("Service template '%s' deployed successfully\n", req.Template)
	return nil
}

// mergeServiceParams merges global params with service-specific component params
func (h *ApplicationHandler) mergeServiceParams(service *ServiceParam, globalParams map[string]map[string]interface{}) *ServiceParam {
	mergedService := *service
	mergedComponents := make([]ComponentParam, len(service.Components))

	for i, component := range service.Components {
		mergedComponent := component

		// If component has params, merge with global params
		if component.Params != nil {
			key := fmt.Sprintf("%s_%s", component.ComponentType, component.ProviderID)
			if globalConfig, exists := globalParams[key]; exists {
				// Merge global params with component params (component params take precedence)
				mergedParams := make(map[string]interface{})
				for k, v := range globalConfig {
					mergedParams[k] = v
				}
				for k, v := range component.Params {
					mergedParams[k] = v
				}
				mergedComponent.Params = mergedParams
			}
		}

		mergedComponents[i] = mergedComponent
	}

	mergedService.Components = mergedComponents
	return &mergedService
}

// deployServiceWithComponents deploys a service and all its components
func (h *ApplicationHandler) deployServiceWithComponents(ctx context.Context, runtimeClient runtime.Runtime, tp templates.Template, appName string, service *ServiceParam) error {
	logger.Infof("Deploying service '%s' with %d components\n", service.ServiceID, len(service.Components))

	// Map to store component information
	componentInfos := make(map[string]*ComponentInfo)

	// Deploy components first and collect their information
	for _, component := range service.Components {
		info, err := h.deployComponent(ctx, runtimeClient, tp, appName, service.ServiceID, &component, service.Params, nil)
		if err != nil {
			return fmt.Errorf("failed to deploy component %s for service %s: %w", component.ComponentType, service.ServiceID, err)
		}

		// Store component info for this component type
		if info != nil {
			componentInfos[component.ComponentType] = info
			logger.Infof("Component '%s' info - Endpoint: %s, Domain: %s, Port: %s, Model: %s\n",
				component.ComponentType, info.Endpoint, info.Domain, info.Port, info.Model)
		}
	}

	// Deploy the service itself with component information
	if err := h.deployService(ctx, runtimeClient, tp, appName, service, componentInfos); err != nil {
		return fmt.Errorf("failed to deploy service %s: %w", service.ServiceID, err)
	}

	return nil
}

// deployComponent deploys a single component (either new or reuses existing) and returns its information
func (h *ApplicationHandler) deployComponent(ctx context.Context, runtimeClient runtime.Runtime, tp templates.Template, appName, serviceID string, component *ComponentParam, serviceParams map[string]interface{}, pool *SpyreCardPool) (*ComponentInfo, error) {
	// If reusing existing instance, get its information
	if component.InstanceID != "" {
		logger.Infof("Reusing existing component instance: %s\n", component.InstanceID)
		info, err := h.getComponentInfo(ctx, runtimeClient, component.InstanceID, component)
		if err != nil {
			return nil, fmt.Errorf("failed to get info for instance %s: %w", component.InstanceID, err)
		}
		return info, nil
	}

	logger.Infof("Deploying new component: %s/%s\n", component.ComponentType, component.ProviderID)

	// Load component template
	componentPath := filepath.Join("components", component.ComponentType, component.ProviderID, "podman")

	// Load component metadata
	metadataPath := filepath.Join("components", component.ComponentType, component.ProviderID, "metadata.yaml")
	metadataData, err := assets.CatalogFS.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read component metadata: %w", err)
	}

	var metadata struct {
		ID    string `yaml:"id"`
		Label string `yaml:"label"`
		Type  string `yaml:"type"`
	}
	if err := yaml.Unmarshal(metadataData, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse component metadata: %w", err)
	}

	// Load template files
	templateFiles, err := assets.CatalogFS.ReadDir(filepath.Join(componentPath, "templates"))
	if err != nil {
		return nil, fmt.Errorf("failed to read component templates: %w", err)
	}

	var componentInfo *ComponentInfo

	// Process each template file
	for _, file := range templateFiles {
		if file.IsDir() {
			continue
		}

		templatePath := filepath.Join(componentPath, "templates", file.Name())
		templateData, err := assets.CatalogFS.ReadFile(templatePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read template %s: %w", file.Name(), err)
		}

		// Parse template
		tmpl, err := template.New(file.Name()).Parse(string(templateData))
		if err != nil {
			return nil, fmt.Errorf("failed to parse template %s: %w", file.Name(), err)
		}

		// First, render template with minimal params to get pod spec for Spyre card calculation
		initialParams := map[string]interface{}{
			"AppName":       appName,
			"ServiceID":     serviceID,
			"Values":        component.Params,
			"ComponentType": component.ComponentType,
			"ProviderID":    component.ProviderID,
			"env":           map[string]map[string]string{}, // Empty env for initial render
		}

		var initialRendered bytes.Buffer
		if err := tmpl.Execute(&initialRendered, initialParams); err != nil {
			return nil, fmt.Errorf("failed to render template %s: %w", file.Name(), err)
		}

		// Parse rendered YAML to get pod spec
		var podSpec models.PodSpec
		if err := yaml.Unmarshal(initialRendered.Bytes(), &podSpec); err != nil {
			return nil, fmt.Errorf("failed to parse rendered pod spec: %w", err)
		}

		// Get env params for this component (including Spyre card PCI addresses if needed)
		env, err := h.getEnvParamsForComponent(&podSpec, pool)
		if err != nil {
			return nil, fmt.Errorf("failed to get env params for component: %w", err)
		}

		// Prepare final template parameters with env
		params := map[string]interface{}{
			"AppName":       appName,
			"ServiceID":     serviceID,
			"Values":        component.Params,
			"ComponentType": component.ComponentType,
			"ProviderID":    component.ProviderID,
			"env":           env,
		}

		fmt.Printf("Component: %s with params: %+v\n", component.ProviderID, params)

		// Render template again with env params
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, params); err != nil {
			return nil, fmt.Errorf("failed to render template %s: %w", file.Name(), err)
		}

		// Parse final rendered YAML
		if err := yaml.Unmarshal(rendered.Bytes(), &podSpec); err != nil {
			return nil, fmt.Errorf("failed to parse rendered pod spec: %w", err)
		}

		// Deploy the pod
		reader := bytes.NewReader(rendered.Bytes())
		if err := podman.DeployPodAndReadinessCheck(runtimeClient, &podSpec, file.Name(), reader, podman.ConstructPodDeployOptions(podSpec.Annotations)); err != nil {
			return nil, fmt.Errorf("failed to deploy pod %s: %w", podSpec.Name, err)
		}

		// Extract information from the deployed pod
		// This information will be used to populate service values for template rendering
		componentInfo = &ComponentInfo{}

		// 1. Extract domain (hostname) from pod name
		// Example: podSpec.Name = "myapp--instruct" → Domain = "myapp--instruct"
		componentInfo.Domain = podSpec.Name

		// 2. Extract port from the pod spec's first container
		// Example: Ports[0].ContainerPort = 8000 → Port = "8000"
		if len(podSpec.Spec.Containers) > 0 && len(podSpec.Spec.Containers[0].Ports) > 0 {
			componentInfo.Port = fmt.Sprintf("%d", podSpec.Spec.Containers[0].Ports[0].ContainerPort)
		}

		// 3. Compute endpoint URL using domain and port
		// Example: Domain = "myapp--instruct", Port = "8000" → Endpoint = "http://myapp--instruct:8000"
		if componentInfo.Port != "" {
			componentInfo.Endpoint = fmt.Sprintf("http://%s:%s", componentInfo.Domain, componentInfo.Port)
		}

		// 4. Extract model name from component params
		// Example: component.Params["model"] = "ibm-granite/granite-3.3-8b-instruct" → Model = "ibm-granite/granite-3.3-8b-instruct"
		// This is used for LLM, embedding, and reranker components
		if component.Params != nil {
			if model, ok := component.Params["model"]; ok {
				componentInfo.Model = fmt.Sprintf("%v", model)
			}
		}

		logger.Infof("Component pod '%s' deployed - Domain: %s, Port: %s, Endpoint: %s, Model: %s\n",
			podSpec.Name, componentInfo.Domain, componentInfo.Port, componentInfo.Endpoint, componentInfo.Model)
	}

	return componentInfo, nil
}

// getComponentInfo retrieves the information for an existing component instance
func (h *ApplicationHandler) getComponentInfo(ctx context.Context, runtimeClient runtime.Runtime, instanceID string, component *ComponentParam) (*ComponentInfo, error) {
	logger.Infof("Getting info for existing instance: %s\n", instanceID)

	// For existing instances, construct component info
	// This is a placeholder - in production, you'd query the instance registry
	info := &ComponentInfo{
		Domain:   instanceID,
		Endpoint: fmt.Sprintf("http://%s", instanceID),
	}

	// Extract model from component params if available
	if component.Params != nil {
		if model, ok := component.Params["model"]; ok {
			info.Model = fmt.Sprintf("%v", model)
		}
	}

	return info, nil
}

// deployService deploys a service with component information
func (h *ApplicationHandler) deployService(ctx context.Context, runtimeClient runtime.Runtime, tp templates.Template, appName string, service *ServiceParam, componentInfos map[string]*ComponentInfo) error {
	logger.Infof("Deploying service: %s with component information\n", service.ServiceID)

	// Load service template
	servicePath := filepath.Join("services", service.ServiceID, "podman")

	// Load service metadata
	metadataPath := filepath.Join("services", service.ServiceID, "metadata.yaml")
	metadataData, err := assets.CatalogFS.ReadFile(metadataPath)
	if err != nil {
		return fmt.Errorf("failed to read service metadata: %w", err)
	}

	var metadata struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
		Type string `yaml:"type"`
	}
	if err := yaml.Unmarshal(metadataData, &metadata); err != nil {
		return fmt.Errorf("failed to parse service metadata: %w", err)
	}

	// Load template files
	templateFiles, err := assets.CatalogFS.ReadDir(filepath.Join(servicePath, "templates"))
	if err != nil {
		return fmt.Errorf("failed to read service templates: %w", err)
	}

	// Load service values.yaml file first to get default configuration
	// Example path: "services/chat/podman/values.yaml"
	// This file contains default values like: {llm: {endpoint: "", model: ""}, embedding: {endpoint: "", model: ""}}
	valuesPath := filepath.Join(servicePath, "values.yaml")
	valuesData, err := assets.CatalogFS.ReadFile(valuesPath)
	if err != nil {
		return fmt.Errorf("failed to read service values.yaml: %w", err)
	}

	// Parse values.yaml into mergedValues map
	// After parsing: mergedValues = {llm: {endpoint: "", model: ""}, embedding: {endpoint: "", model: ""}, ...}
	mergedValues := make(map[string]interface{})
	if err := yaml.Unmarshal(valuesData, &mergedValues); err != nil {
		return fmt.Errorf("failed to parse service values.yaml: %w", err)
	}

	// Merge service-specific params on top of default values
	// This allows overriding default values with user-provided configuration
	if service.Params != nil {
		for k, v := range service.Params {
			mergedValues[k] = v
		}
	}

	// Add component information to the values under their respective component types
	// For each component, we populate: endpoint, domain (host), port, and model
	//
	// Example flow:
	// 1. values.yaml has: {llm: {endpoint: "", model: ""}, embedding: {endpoint: "", model: ""}}
	// 2. componentInfos has: {"llm": {Endpoint: "http://app--instruct:8000", Domain: "app--instruct", Port: "8000", Model: "granite-3.3-8b"}}
	// 3. After this loop: {llm: {endpoint: "http://app--instruct:8000", host: "app--instruct", port: "8000", model: "granite-3.3-8b"}}
	// 4. Templates can access: {{ .Values.llm.endpoint }}, {{ .Values.llm.model }}, etc.
	for componentType, info := range componentInfos {
		if info == nil {
			continue
		}

		// Get or create the component map from mergedValues
		// componentType examples: "llm", "embedding", "reranker", "opensearch"
		// This gets the nested map: mergedValues["llm"] = {endpoint: "", model: ""}
		if _, exists := mergedValues[componentType]; !exists {
			mergedValues[componentType] = make(map[string]interface{})
		}

		// componentMap is a reference to mergedValues[componentType]
		// Any changes to componentMap directly modify mergedValues
		componentMap, ok := mergedValues[componentType].(map[string]interface{})
		if !ok {
			continue
		}

		// Populate all component information into the map
		// Example: componentMap["endpoint"] = "http://app--instruct:8000"
		// This actually sets: mergedValues["llm"]["endpoint"] = "http://app--instruct:8000"
		if info.Endpoint != "" {
			componentMap["endpoint"] = info.Endpoint
		}
		if info.Domain != "" {
			componentMap["host"] = info.Domain
		}
		if info.Port != "" {
			componentMap["port"] = info.Port
		}
		if info.Model != "" {
			componentMap["model"] = info.Model
		}

		logger.Infof("Populated values for component '%s': endpoint=%s, host=%s, port=%s, model=%s\n",
			componentType, info.Endpoint, info.Domain, info.Port, info.Model)
	}

	logger.Infof("Merged values for service %s: %v\n", service.ServiceID, mergedValues)

	// Process each template file
	for _, file := range templateFiles {
		if file.IsDir() {
			continue
		}

		templatePath := filepath.Join(servicePath, "templates", file.Name())
		templateData, err := assets.CatalogFS.ReadFile(templatePath)
		if err != nil {
			return fmt.Errorf("failed to read template %s: %w", file.Name(), err)
		}

		// Parse template
		tmpl, err := template.New(file.Name()).Parse(string(templateData))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", file.Name(), err)
		}

		// Prepare template parameters with merged values
		params := map[string]interface{}{
			"AppName":   appName,
			"ServiceID": service.ServiceID,
			"Values":    mergedValues,
		}

		fmt.Printf("Service: %s with params: %+v\n", service.ServiceID, params)
		return nil

		// Render template
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, params); err != nil {
			return fmt.Errorf("failed to render template %s: %w", file.Name(), err)
		}

		// Parse rendered YAML to get pod spec
		var podSpec models.PodSpec
		if err := yaml.Unmarshal(rendered.Bytes(), &podSpec); err != nil {
			return fmt.Errorf("failed to parse rendered pod spec: %w", err)
		}

		// Deploy the pod
		reader := bytes.NewReader(rendered.Bytes())
		if err := podman.DeployPodAndReadinessCheck(runtimeClient, &podSpec, file.Name(), reader, podman.ConstructPodDeployOptions(podSpec.Annotations)); err != nil {
			return fmt.Errorf("failed to deploy pod %s: %w", podSpec.Name, err)
		}

		logger.Infof("Service pod '%s' deployed successfully\n", podSpec.Name)
	}

	return nil
}

// Made with Bob
