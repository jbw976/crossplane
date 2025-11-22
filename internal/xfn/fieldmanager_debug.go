package xfn

import (
	"fmt"

	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DebugManagedFields returns debug information about an object's managed fields.
// This helps diagnose why field deletion isn't working.
func DebugManagedFields(obj *kunstructured.Unstructured, fieldManager string) map[string]interface{} {
	debug := make(map[string]interface{})

	allManagers := []string{}
	var targetManager map[string]interface{}

	for _, mf := range obj.GetManagedFields() {
		allManagers = append(allManagers, mf.Manager)

		if mf.Manager == fieldManager {
			targetManager = map[string]interface{}{
				"manager":   mf.Manager,
				"operation": mf.Operation,
				"apiVersion": mf.APIVersion,
				"fieldsV1":  string(mf.FieldsV1.Raw),
			}
		}
	}

	debug["allManagers"] = allManagers
	debug["targetManager"] = targetManager
	debug["lookingFor"] = fieldManager

	return debug
}

// DebugFieldPaths returns debug info about parsed field paths.
func DebugFieldPaths(obj *kunstructured.Unstructured, fieldManager string) (map[string]interface{}, error) {
	debug := make(map[string]interface{})

	ownedPaths, err := getOwnedFieldPaths(obj, fieldManager)
	if err != nil {
		return nil, err
	}

	debug["ownedPathsCount"] = len(ownedPaths)
	debug["ownedPaths"] = ownedPaths
	debug["fieldManager"] = fieldManager

	// Also show what managed fields exist
	managers := []string{}
	for _, mf := range obj.GetManagedFields() {
		managers = append(managers, fmt.Sprintf("%s (%s)", mf.Manager, mf.Operation))
	}
	debug["allManagers"] = managers

	return debug, nil
}

