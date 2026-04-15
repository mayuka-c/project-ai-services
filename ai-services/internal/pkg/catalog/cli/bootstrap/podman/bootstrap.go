package podman

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"text/template"

	"github.com/project-ai-services/ai-services/internal/pkg/cli/helpers"
	clipodman "github.com/project-ai-services/ai-services/internal/pkg/cli/podman"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/templates"
	"github.com/project-ai-services/ai-services/internal/pkg/constants"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/podman"
	"github.com/project-ai-services/ai-services/internal/pkg/spinner"
)

const (
	catalogAppName     = "ai-services"
	catalogAppTemplate = "catalog"
)

// DeployCatalog deploys the catalog service using the assets/catalog template for podman runtime.
func DeployCatalog(ctx context.Context, podmanURI, passwordHash string, argParams map[string]string) error {
	s := spinner.New("Deploying catalog service...")
	s.Start(ctx)

	// Initialize runtime
	rt, err := podman.NewPodmanClient()
	if err != nil {
		s.Fail("failed to initialize podman client")
		return fmt.Errorf("failed to initialize podman client: %w", err)
	}

	// Check if catalog pod already exists
	existingPods, err := helpers.CheckExistingPodsForApplication(rt, catalogAppName)
	if err != nil {
		s.Fail("failed to check existing pods")
		return fmt.Errorf("failed to check existing pods: %w", err)
	}

	if len(existingPods) > 0 {
		s.Stop("Catalog service already deployed")
		logger.Infof("Catalog pod already exists: %v\n", existingPods)
		return nil
	}

	// Load template provider
	// Catalog is at assets/catalog (so set root to "")
	tp := templates.NewEmbedTemplateProvider(templates.EmbedOptions{
		Root: "", // This will look directly under assets/
	})

	// Load metadata from catalog/podman
	appMetadata, err := tp.LoadMetadata(catalogAppTemplate, true)
	if err != nil {
		s.Fail("failed to load catalog metadata")
		return fmt.Errorf("failed to load catalog metadata: %w", err)
	}

	// Load all templates from catalog
	tmpls, err := tp.LoadAllTemplates(catalogAppTemplate)
	if err != nil {
		s.Fail("failed to load catalog templates")
		return fmt.Errorf("failed to load catalog templates: %w", err)
	}

	// Prepare argParams with bootstrap-specific values
	if argParams == nil {
		argParams = make(map[string]string)
	}

	// Set bootstrap-specific values
	argParams["backend.adminPasswordHash"] = passwordHash
	argParams["backend.runtime"] = "podman"
	argParams["backend.podman.uri"] = podmanURI

	// Load values from catalog with merged params
	values, err := tp.LoadValues(catalogAppTemplate, nil, argParams)
	if err != nil {
		s.Fail("failed to load values")
		return fmt.Errorf("failed to load values: %w", err)
	}

	// Execute pod templates - following the same pattern as application/podman/create.go:432
	for i, layer := range appMetadata.PodTemplateExecutions {
		logger.Infof("\n Executing Layer %d/%d: %v\n", i+1, len(appMetadata.PodTemplateExecutions), layer)
		logger.Infoln("-------")
		var wg sync.WaitGroup
		errCh := make(chan error, len(layer))

		// for each layer, fetch all the pod Template Names and do the pod deploy
		for _, podTemplateName := range layer {
			wg.Add(1)
			go func(t string) {
				defer wg.Done()
				if err := executePodTemplate(rt, tp, tmpls, t, catalogAppTemplate, catalogAppName, values, appMetadata.Version, nil, argParams); err != nil {
					errCh <- err
				}
			}(podTemplateName)
		}

		wg.Wait()
		close(errCh)

		// collect all errors for this layer
		var errs []error
		for e := range errCh {
			errs = append(errs, fmt.Errorf("layer %d: %w", i+1, e))
		}

		// If an error exist for a given layer, then return (do not process further layers)
		if len(errs) > 0 {
			s.Fail("failed to deploy catalog pod")
			return errors.Join(errs...)
		}

		logger.Infof("Layer %d completed\n", i+1)
	}

	s.Stop("Catalog service deployed successfully")

	logger.Infoln("-------")

	// Print next steps similar to application create
	if err := helpers.PrintNextSteps(rt, catalogAppName, catalogAppTemplate); err != nil {
		// do not want to fail the overall bootstrap if we cannot print next steps
		logger.Infof("failed to display next steps: %v\n", err)
	}

	return nil
}

// executePodTemplate executes a single pod template.
func executePodTemplate(rt *podman.PodmanClient, tp templates.Template, tmpls map[string]*template.Template,
	podTemplateName, appTemplateName, appName string, values map[string]any, version string,
	valuesFiles []string, argParams map[string]string) error {

	logger.Infof("Processing template: %s\n", podTemplateName)

	// Fetch pod spec
	podSpec, err := tp.LoadPodTemplateWithValues(appTemplateName, podTemplateName, appName, valuesFiles, argParams)
	if err != nil {
		return fmt.Errorf("failed to load pod template: %w", err)
	}

	// Prepare template parameters
	params := map[string]any{
		"AppName":         appName,
		"AppTemplateName": appTemplateName,
		"Version":         version,
		"Values":          values,
		"env":             map[string]map[string]string{},
	}

	// Get the template
	podTemplate := tmpls[podTemplateName]

	// Render template
	var rendered bytes.Buffer
	if err := podTemplate.Execute(&rendered, params); err != nil {
		return fmt.Errorf("failed to render pod template: %w", err)
	}

	// Deploy the pod with readiness checks
	reader := bytes.NewReader(rendered.Bytes())
	opts := map[string]string{"start": constants.PodStartOn}

	if err := clipodman.DeployPodAndReadinessCheck(rt, podSpec, podTemplateName, reader, opts); err != nil {
		return fmt.Errorf("failed to deploy pod: %w", err)
	}

	return nil
}

// Made with Bob
