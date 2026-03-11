package openshift

import (
	"fmt"
	"maps"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/project-ai-services/ai-services/internal/pkg/logger"
)

// TemplateExists checks if an OpenShift Template exists in the specified namespace.
func TemplateExists(name, namespace string) (bool, error) {
	ocClient, err := NewOpenshiftClient()
	if err != nil {
		return false, fmt.Errorf("failed to create OpenShift client: %w", err)
	}

	return templateExists(ocClient, name, namespace)
}

// GetTemplate retrieves an OpenShift Template from the cluster.
func GetTemplate(name, namespace string) (*unstructured.Unstructured, error) {
	ocClient, err := NewOpenshiftClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenShift client: %w", err)
	}

	return getTemplate(ocClient, name, namespace)
}

// ProcessTemplateWithParameters processes an OpenShift Template with the given parameters.
func ProcessTemplateWithParameters(template *unstructured.Unstructured, parameters map[string]string) ([]unstructured.Unstructured, error) {
	return processTemplate(template, parameters)
}

// ApplyObjects applies the processed template objects to the cluster
// This is an idempotent operation - it will create objects if they don't exist,
// or update them if they do exist.
func ApplyObjects(objects []unstructured.Unstructured, namespace string) error {
	return ApplyObjectsWithLabels(objects, namespace, nil)
}

// ApplyObjectsWithLabels applies the processed template objects to the cluster with additional labels
// This is an idempotent operation - it will create objects if they don't exist,
// or update them if they do exist. Additional labels are merged with existing labels.
func ApplyObjectsWithLabels(objects []unstructured.Unstructured, namespace string, labels map[string]string) error {
	ocClient, err := NewOpenshiftClient()
	if err != nil {
		return fmt.Errorf("failed to create OpenShift client: %w", err)
	}

	return applyProcessedObjectsWithLabels(ocClient, objects, namespace, labels)
}

// templateExists checks if an OpenShift Template exists in the specified namespace.
func templateExists(ocClient *OpenshiftClient, name, namespace string) (bool, error) {
	template := &unstructured.Unstructured{}
	template.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "template.openshift.io",
		Version: "v1",
		Kind:    "Template",
	})

	err := ocClient.Client.Get(ocClient.Ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, template)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// getTemplate retrieves an OpenShift Template from the cluster.
func getTemplate(ocClient *OpenshiftClient, name, namespace string) (*unstructured.Unstructured, error) {
	template := &unstructured.Unstructured{}
	template.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "template.openshift.io",
		Version: "v1",
		Kind:    "Template",
	})

	err := ocClient.Client.Get(ocClient.Ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, template)

	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	return template, nil
}

// processTemplate processes an OpenShift Template with the given parameters.
func processTemplate(template *unstructured.Unstructured, parameters map[string]string) ([]unstructured.Unstructured, error) {
	// Get the template parameters
	templateParams, found, err := unstructured.NestedSlice(template.Object, "parameters")
	if err != nil {
		return nil, fmt.Errorf("failed to get template parameters: %w", err)
	}

	// Build parameter map with defaults
	paramMap := make(map[string]string)
	if found {
		for _, param := range templateParams {
			paramObj, ok := param.(map[string]any)
			if !ok {
				continue
			}
			name, _ := paramObj["name"].(string)
			value, hasValue := paramObj["value"].(string)
			if hasValue {
				paramMap[name] = value
			}
		}
	}

	// Override with provided parameters
	maps.Copy(paramMap, parameters)

	// Get the objects from the template
	objects, found, err := unstructured.NestedSlice(template.Object, "objects")
	if err != nil {
		return nil, fmt.Errorf("failed to get template objects: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("template has no objects")
	}

	// Process each object and substitute parameters
	processedObjects := make([]unstructured.Unstructured, 0, len(objects))
	for _, obj := range objects {
		objMap, ok := obj.(map[string]any)
		if !ok {
			continue
		}

		// Substitute parameters in the object
		processedObj := substituteParameters(objMap, paramMap)

		u := unstructured.Unstructured{Object: processedObj}
		processedObjects = append(processedObjects, u)
	}

	return processedObjects, nil
}

// substituteParameters recursively substitutes template parameters in an object.
func substituteParameters(obj map[string]any, params map[string]string) map[string]any {
	result := make(map[string]any)

	for key, value := range obj {
		switch v := value.(type) {
		case string:
			// Simple parameter substitution: ${PARAM_NAME}
			result[key] = substituteString(v, params)
		case map[string]any:
			result[key] = substituteParameters(v, params)
		case []any:
			result[key] = substituteSlice(v, params)
		default:
			result[key] = value
		}
	}

	return result
}

// substituteSlice substitutes parameters in a slice.
func substituteSlice(slice []any, params map[string]string) []any {
	result := make([]any, len(slice))

	for i, item := range slice {
		switch v := item.(type) {
		case string:
			result[i] = substituteString(v, params)
		case map[string]any:
			result[i] = substituteParameters(v, params)
		case []any:
			result[i] = substituteSlice(v, params)
		default:
			result[i] = item
		}
	}

	return result
}

// substituteString performs simple parameter substitution in a string.
func substituteString(s string, params map[string]string) string {
	// Simple implementation - can be enhanced with regex for ${PARAM} syntax
	result := s
	for key, value := range params {
		placeholder := fmt.Sprintf("${%s}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}

// applyProcessedObjectsWithLabels applies the processed template objects to the cluster with additional labels
// This is an idempotent operation - it will create objects if they don't exist,
// or update them if they do exist. Additional labels are merged with existing labels.
func applyProcessedObjectsWithLabels(ocClient *OpenshiftClient, objects []unstructured.Unstructured, namespace string, labels map[string]string) error {
	for _, obj := range objects {
		// Set the namespace if not already set
		if obj.GetNamespace() == "" {
			obj.SetNamespace(namespace)
		}

		// Add additional labels if provided
		if len(labels) > 0 {
			existingLabels := obj.GetLabels()
			if existingLabels == nil {
				existingLabels = make(map[string]string)
			}
			// Merge labels (additional labels take precedence)
			maps.Copy(existingLabels, labels)
			obj.SetLabels(existingLabels)
		}

		// Check if the object already exists
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(obj.GroupVersionKind())

		err := ocClient.Client.Get(ocClient.Ctx, client.ObjectKey{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}, existing)
		if err != nil && client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to check if object exists %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}

		// Handle object not found - create it
		if client.IgnoreNotFound(err) == nil {
			if createErr := ocClient.Client.Create(ocClient.Ctx, &obj); createErr != nil {
				return fmt.Errorf("failed to create object %s/%s: %w", obj.GetKind(), obj.GetName(), createErr)
			}
			logger.Infof("Created %s/%s\n", obj.GetKind(), obj.GetName())

			continue
		}

		// Object exists, update it using Update
		// We preserve the resource version from the existing object
		obj.SetResourceVersion(existing.GetResourceVersion())
		if updateErr := ocClient.Client.Update(ocClient.Ctx, &obj); updateErr != nil {
			return fmt.Errorf("failed to update object %s/%s: %w", obj.GetKind(), obj.GetName(), updateErr)
		}
		logger.Infof("Updated %s/%s\n", obj.GetKind(), obj.GetName())
	}

	return nil
}

// Made with Bob
