package image

import (
	"fmt"

	"github.com/spf13/cobra"

	catalogClient "github.com/project-ai-services/ai-services/internal/pkg/catalog/client"
)

var (
	templateName string
	legacyImage  bool
)

var ImageCmd = &cobra.Command{
	Use:   "image",
	Short: "Manage application images",
	Long:  ``,
	Args:  cobra.MaximumNArgs(0),
	RunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},
}

// getCatalogImages fetches container images for a template (service or architecture)
// from the server API. The server resolves whether the ID is a service or architecture
// and returns all required images including component dependencies.
func getCatalogImages(templateID string) ([]string, error) {
	client, err := catalogClient.NewApplicationClient()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %w", err)
	}

	// Try as architecture first, then service — mirrors the server-side logic in
	// GetCatalogImages which tries LoadArchitecture before LoadService.
	images, err := client.GetArchitectureImages(templateID)
	if err == nil {
		return images, nil
	}

	images, err = client.GetServiceImages(templateID)
	if err != nil {
		return nil, fmt.Errorf("template '%s' not found as service or architecture: %w", templateID, err)
	}

	return images, nil
}

func init() {
	ImageCmd.AddCommand(listCmd)
	ImageCmd.AddCommand(pullCmd)
	ImageCmd.PersistentFlags().StringVarP(&templateName, "template", "t", "", "Application template name (Required)")
	_ = ImageCmd.MarkPersistentFlagRequired("template")
	ImageCmd.PersistentFlags().BoolVar(&legacyImage, "legacy", false, "Use legacy application image implementation")
}
