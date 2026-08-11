package model

import (
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/project-ai-services/ai-services/assets"
	catalogClient "github.com/project-ai-services/ai-services/internal/pkg/catalog/client"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/helpers"
	"github.com/project-ai-services/ai-services/internal/pkg/cli/templates"
)

var (
	ModelCmd = &cobra.Command{
		Use:   "model",
		Short: "Manage application models",
		Long: `Manage AI models for application templates.
This command provides subcommands to list and download models required by application templates.`,
		Args: cobra.MaximumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	hiddenTemplates bool
	legacyModel     bool
)

func init() {
	ModelCmd.AddCommand(listCmd)
	ModelCmd.AddCommand(downloadCmd)
	ModelCmd.PersistentFlags().BoolVar(&legacyModel, "legacy", false, "Use legacy application model implementation")
}

// models is the legacy path — still reads embedded ApplicationFS templates directly.
func models(template string) ([]string, error) {
	tp := templates.NewEmbedTemplateProvider(&assets.ApplicationFS)
	apps, err := tp.ListApplications(hiddenTemplates)
	if err != nil {
		return nil, fmt.Errorf("failed to list the applications, err: %w", err)
	}

	if !slices.Contains(apps, template) {
		return nil, fmt.Errorf("application template %s does not exist", template)
	}

	return helpers.ListModels(template, "")
}

// getCatalogModels fetches model identifiers for a template (service or architecture)
// from the server API. The server resolves the template type and extracts model names
// from component schemas — no local file access required.
func getCatalogModels(templateID string, excludeComponentProviders ...string) ([]string, error) {
	client, err := catalogClient.NewApplicationClient()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %w", err)
	}

	// Try as architecture first, then service.
	models, err := client.GetArchitectureModels(templateID)
	if err == nil {
		return filterModels(models, excludeComponentProviders), nil
	}

	models, err = client.GetServiceModels(templateID)
	if err != nil {
		return nil, fmt.Errorf("template '%s' not found as service or architecture: %w", templateID, err)
	}

	return filterModels(models, excludeComponentProviders), nil
}

// filterModels removes model entries that match any excludeComponentProviders prefix.
// The server returns all models; the caller may want to exclude specific providers.
func filterModels(models []string, exclude []string) []string {
	if len(exclude) == 0 {
		return models
	}

	out := models[:0:0]
	for _, m := range models {
		keep := true
		for _, ex := range exclude {
			if m == ex {
				keep = false

				break
			}
		}

		if keep {
			out = append(out, m)
		}
	}

	return out
}
