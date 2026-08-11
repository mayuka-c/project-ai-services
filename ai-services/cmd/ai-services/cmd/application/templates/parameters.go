package templates

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	catalogClient "github.com/project-ai-services/ai-services/internal/pkg/catalog/client"
	"github.com/project-ai-services/ai-services/internal/pkg/logger"
	runtimetypes "github.com/project-ai-services/ai-services/internal/pkg/runtime/types"
	"github.com/project-ai-services/ai-services/internal/pkg/vars"
)

var (
	templateID string
)

// NewParametersCmd creates the parameters subcommand.
func NewParametersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "parameters",
		Short: "Display supported parameters for a specific template",
		Long:  `Display all supported parameters for a specific template ID (service or architecture) from the catalog`,
		Example: `  # Display parameters for a service
  ai-services application templates parameters --template digitize --runtime podman

  # Display parameters for an architecture
  ai-services application templates parameters --template rag --runtime podman`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			// Check runtime - only supported for Podman
			if vars.RuntimeFactory.GetRuntimeType() != runtimetypes.RuntimeTypePodman {
				return fmt.Errorf("templates parameters subcommand is only supported for Podman runtime")
			}

			if templateID == "" {
				return fmt.Errorf("--template flag is required")
			}

			client, err := catalogClient.NewApplicationClient()
			if err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}

			// Try to load as architecture first via API.
			arch, err := client.GetArchitectureDeployOptions(templateID)
			if err == nil {
				logger.Infof("Supported Parameters for '%s':", templateID)
				displayedComponents := make(map[string]bool)

				for _, svc := range arch.Services {
					// Service-level params
					if schema, sErr := client.GetServiceParams(svc.ID); sErr == nil && len(schema) > 0 {
						displaySchemaParameters(schema, svc.ID)
					}

					// Component params for each service
					for _, comp := range svc.Components {
						for _, provider := range comp.Providers {
							key := fmt.Sprintf("%s.%s", comp.Type, provider.ID)
							if displayedComponents[key] {
								continue
							}

							displayedComponents[key] = true

							if schema, cErr := client.GetComponentProviderParams(comp.Type, provider.ID); cErr == nil && len(schema) > 0 {
								displaySchemaParameters(schema, key)
							}
						}
					}
				}

				return nil
			}

			// Try as service.
			svc, err := client.GetServiceDeployOptions(templateID)
			if err == nil {
				logger.Infof("Supported Parameters for '%s':", templateID)

				if schema, sErr := client.GetServiceParams(templateID); sErr == nil && len(schema) > 0 {
					displaySchemaParameters(schema, templateID)
				}

				for _, comp := range svc.Components {
					for _, provider := range comp.Providers {
						key := fmt.Sprintf("%s.%s", comp.Type, provider.ID)

						if schema, cErr := client.GetComponentProviderParams(comp.Type, provider.ID); cErr == nil && len(schema) > 0 {
							displaySchemaParameters(schema, key)
						}
					}
				}

				return nil
			}

			return fmt.Errorf("template '%s' not found as service or architecture", templateID)
		},
	}

	cmd.Flags().StringVar(&templateID, "template", "", "Template ID (service or architecture)")
	_ = cmd.MarkFlagRequired("template")

	return cmd
}

// displaySchemaParameters displays parameters from a schema with the given prefix.
func displaySchemaParameters(schema map[string]any, prefix string) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return
	}

	displayPropertiesRecursive(properties, prefix)
}

// displayPropertiesRecursive recursively displays properties, handling nested objects.
// It skips fields marked with "x-ui-only": true (UI-only fields with no CLI meaning).
func displayPropertiesRecursive(properties map[string]any, prefix string) {
	for paramName, propValue := range properties {
		prop, ok := propValue.(map[string]any)
		if !ok {
			continue
		}

		// Skip fields explicitly marked as UI-only
		if uiOnly, _ := prop["x-ui-only"].(bool); uiOnly {
			continue
		}

		propType, _ := prop["type"].(string)
		description := cleanDescription(prop["description"])

		// If this is an object type with nested properties, recurse into it
		if propType == "object" {
			if nestedProps, ok := prop["properties"].(map[string]any); ok {
				displayPropertiesRecursive(nestedProps, fmt.Sprintf("%s.%s", prefix, paramName))

				continue
			}
		}

		// Append default value if present and not empty
		if defaultValue, hasDefault := prop["default"]; hasDefault && defaultValue != nil && defaultValue != "" {
			logger.Infof("  %s.%s: %s (Default: %v)", prefix, paramName, description, defaultValue)
		} else {
			logger.Infof("  %s.%s: %s", prefix, paramName, description)
		}
	}
}

// cleanDescription normalises a JSON schema description for CLI display:
// it collapses newlines to spaces and strips markdown bold markers.
func cleanDescription(raw any) string {
	s, _ := raw.(string)
	if s == "" {
		return ""
	}

	// Collapse newlines (and surrounding whitespace) to a single space
	s = strings.Join(strings.Fields(s), " ")

	// Strip markdown bold: **text** → text
	s = strings.ReplaceAll(s, "**", "")

	return s
}
