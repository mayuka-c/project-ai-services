package application

import (
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/project-ai-services/ai-services/assets"
	appTemplates "github.com/project-ai-services/ai-services/cmd/ai-services/cmd/application/templates"
	catalogClient "github.com/project-ai-services/ai-services/internal/pkg/catalog/client"
	catalogTypes "github.com/project-ai-services/ai-services/internal/pkg/catalog/types"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/templates"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	"github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/vars"
)

var (
	legacyTemplates bool
)

var templatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "Lists the offered application templates and their supported parameters",
	Long:  `Retrieves information about the offered application templates and their supported parameters`,
	Example: `  For Podman:
	 # List all available application templates (Podman)
	 ai-services application templates --runtime podman

	 # List parameters for a specific template (see subcommand)
	 ai-services application templates parameters --template digitize --runtime podman

	 # List templates using legacy implementation
	 ai-services application templates --legacy --runtime podman

	 For OpenShift:
	 # List all available application templates (OpenShift)
	 ai-services application templates --runtime openshift

	 # List parameters for a specific template (see subcommand)
	 ai-services application templates parameters --template digitize --runtime openshift `,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Once precheck passes, silence usage for any *later* internal errors.
		cmd.SilenceUsage = true

		// When legacyTemplates is true and runtime is podman, use the older/stable code path
		// For openshift runtime, always use the older/stable code path regardless of legacy flag
		if !legacyTemplates && vars.RuntimeFactory.GetRuntimeType() == types.RuntimeTypePodman {
			// Use catalog templates listing (architectures and services)
			return listCatalogTemplates(cmd)
		}

		tp := templates.NewEmbedTemplateProvider(&assets.ApplicationFS)

		appTemplateNames, err := tp.ListApplications(hiddenTemplates)
		if err != nil {
			return fmt.Errorf("failed to list application templates: %w", err)
		}

		if len(appTemplateNames) == 0 {
			logger.Infoln("No application templates found.")

			return nil
		}

		// sort appTemplateNames alphabetically
		sort.Strings(appTemplateNames)

		logger.Infoln("Available application templates:")
		for _, name := range appTemplateNames {
			appTemplatesParametersWithDescription, err := tp.ListApplicationTemplateValues(name)
			if err != nil {
				// Skip applications that don't support the current runtime (silently)
				if errors.Is(err, templates.ErrRuntimeNotSupported) {
					continue
				}
				// Log other errors
				logger.Errorf("failed to list application template values: %v", err)

				continue
			}

			logger.Infof("- %s", name)
			var metadata templates.AppMetadata
			if err := tp.LoadMetadata(name, false, &metadata); err != nil {
				logger.Errorf("failed to load application metadata: %v", err)

				continue
			}
			if metadata.Description != "" {
				logger.Infof("  Description: %s", metadata.Description)
			}

			logger.Infoln("\n  Supported Parameters:")
			if len(appTemplatesParametersWithDescription) == 0 {
				logger.Infoln("\t" + "NONE")
			}

			for k, v := range appTemplatesParametersWithDescription {
				logger.Infoln("\t" + k + ":  " + v)
			}
		}

		return nil
	},
}

func init() {
	templatesCmd.Flags().BoolVar(&legacyTemplates, "legacy", false, "Use legacy application templates implementation")

	// Add parameters subcommand
	templatesCmd.AddCommand(appTemplates.NewParametersCmd())
}

// listCatalogTemplates lists architectures and services from the server API.
// No local catalog provider or embedded file access required.
func listCatalogTemplates(_ *cobra.Command) error {
	client, err := catalogClient.NewApplicationClient()
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	architectureSummaries, err := client.ListArchitectures()
	if err != nil {
		return fmt.Errorf("failed to list architectures: %w", err)
	}

	serviceSummaries, err := client.ListServices()
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	// Section 1: Deployment Architectures
	logger.Infoln("Available Deployment Architectures:")
	for _, arch := range architectureSummaries {
		displayArchitectureSummary(arch)
	}

	// Section 2: Deployment Services
	logger.Infoln("\nAvailable Services:")
	for _, svc := range serviceSummaries {
		displayServiceSummary(svc)
	}

	// Inform user about parameters subcommand
	logger.Infoln("\nTo list supported parameters for each template use: application templates parameters --template <Template ID>\n")

	return nil
}

// displayArchitectureWithServiceList displays an architecture with just the list of service IDs.
// displayArchitectureSummary displays an architecture with its service list.
func displayArchitectureSummary(arch catalogTypes.ArchitectureSummary) {
	logger.Infof("- %s (%s)", arch.ID, arch.Name)
	if arch.Description != "" {
		logger.Infof("  Description: %s", arch.Description)
	}

	if len(arch.Services) > 0 {
		logger.Infoln("  Services:")
		for _, svcID := range arch.Services {
			logger.Infof("     - %s", svcID)
		}
	}
}

// displayServiceSummary displays a service with its metadata.
func displayServiceSummary(svc catalogTypes.ServiceSummary) {
	logger.Infof("- %s (%s)", svc.ID, svc.Name)
	if svc.Description != "" {
		logger.Infof("  Description: %s", svc.Description)
	}
}
