/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use
this file except in compliance with the License. You may obtain a copy of the
License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed
under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.
*/

package xfn

import (
	"bytes"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured"
	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// PrepareResourceForFieldDeletion modifies the desired resource to explicitly
// set fields to nil for any fields that are owned by the specified field
// manager in the observed resource but are missing from the desired resource.
// This is necessary because server-side apply interprets omitted fields as
// "leave unchanged" rather than "delete".
func PrepareResourceForFieldDeletion(observed, desired runtime.Object, fieldManager string) ([]string, error) {
	obsUnstructured, err := toUnstructured(observed)
	if err != nil {
		return nil, errors.Wrap(err, "cannot convert observed to unstructured")
	}

	desiredUnstructured, err := toUnstructured(desired)
	if err != nil {
		return nil, errors.Wrap(err, "cannot convert desired to unstructured")
	}

	// Extract field paths owned by our field manager from the observed resource
	ownedPaths, err := getOwnedFieldPaths(obsUnstructured, fieldManager)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get owned field paths")
	}

	// If there are no owned paths, nothing to do
	if len(ownedPaths) == 0 {
		return []string{}, nil
	}

	// Find which owned paths are missing in desired
	pathsToDelete := findMissingFields(obsUnstructured, desiredUnstructured, ownedPaths)

	// Group paths by their parent and replace with parent if all siblings are being deleted.
	// This prevents invalid JSON like {"parent": {"child1": null, "child2": null}}
	pathsToDelete = replaceWithParentIfAllSiblingsDeleted(obsUnstructured, desiredUnstructured, pathsToDelete)

	// Set those fields to nil in desired
	return pathsToDelete, setFieldsToNil(desiredUnstructured, pathsToDelete)
}

// toUnstructured converts a runtime.Object to *kunstructured.Unstructured.
// This follows the same pattern as AsStruct in utils.go.
func toUnstructured(obj runtime.Object) (*kunstructured.Unstructured, error) {
	// If the supplied object is already *Unstructured, return it directly.
	if u, ok := obj.(*kunstructured.Unstructured); ok {
		return u, nil
	}

	// If the supplied object wraps *Unstructured (e.g., composed.Unstructured),
	// extract it. This is the Crossplane-idiomatic way.
	if w, ok := obj.(unstructured.Wrapper); ok {
		return w.GetUnstructured(), nil
	}

	// Fall back to using the standard Kubernetes converter for other types.
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, errors.Wrap(err, "cannot convert to unstructured")
	}

	return &kunstructured.Unstructured{Object: content}, nil
}

// getOwnedFieldPaths extracts all field paths owned by the specified field
// manager from the object's managed fields.
func getOwnedFieldPaths(obj *kunstructured.Unstructured, fieldManager string) ([]string, error) {
	var ownedPaths []string

	for _, mf := range obj.GetManagedFields() {
		// Check if this managed field entry matches our field manager exactly.
		// We need exact match to ensure we only delete fields owned by this
		// specific XR, not fields owned by other XRs.
		if mf.Manager != fieldManager {
			continue
		}

		// Parse the FieldsV1 structure
		if mf.FieldsV1 == nil {
			continue
		}

		paths, err := parseFieldsV1(mf.FieldsV1.Raw)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot parse FieldsV1 for manager %s", mf.Manager)
		}

		ownedPaths = append(ownedPaths, paths...)
	}

	return ownedPaths, nil
}

// parseFieldsV1 parses the FieldsV1 raw JSON and extracts field paths using
// the official Kubernetes structured-merge-diff library. This avoids brittle
// string parsing and uses the same code that Kubernetes itself uses.
func parseFieldsV1(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	// The FieldsV1 JSON needs to be decoded into a fieldpath.Set
	// We use FromJSON which properly handles the FieldsV1 format
	set := &fieldpath.Set{}
	if err := set.FromJSON(bytes.NewReader(raw)); err != nil {
		// If FromJSON fails, return the error with context
		return nil, errors.Wrapf(err, "cannot deserialize FieldsV1 (raw: %s)", string(raw))
	}

	if set.Empty() {
		// Empty set means no paths were parsed
		return nil, nil
	}

	// Convert the fieldpath.Set to string paths
	var paths []string
	set.Iterate(func(p fieldpath.Path) {
		pathStr := pathToString(p)
		paths = append(paths, pathStr)
	})

	return paths, nil
}

// pathToString converts a fieldpath.Path to a dot-separated string suitable
// for use with kunstructured.NestedFieldNoCopy.
//
// We can't use Path.String() because it:
//  1. Prefixes each field with "." (e.g., ".spec.forProvider")
//  2. Includes list keys/indices (e.g., "[uid=123]", "[0]")
//
// We need: "spec.forProvider.maintenancePolicy" (no leading dot, field names only)
func pathToString(p fieldpath.Path) string {
	parts := make([]string, 0, len(p))
	for _, pe := range p {
		// Only include field names, skip array indices and map keys
		if pe.FieldName != nil {
			parts = append(parts, *pe.FieldName)
		}
	}
	return strings.Join(parts, ".")
}

// replaceWithParentIfAllSiblingsDeleted optimizes deletion paths by replacing
// all children with their parent when all siblings are being deleted AND the
// parent has no other children in desired.
//
// For example, if we're deleting ["a.b.x", "a.b.y"] and those are the ONLY
// children of "a.b" in observed, AND "a.b" doesn't exist or is empty in desired,
// we replace them with ["a.b"]. This prevents invalid JSON like {"a": {"b": {"x": null, "y": null}}}.
func replaceWithParentIfAllSiblingsDeleted(observed, desired *kunstructured.Unstructured, paths []string) []string {
	if len(paths) == 0 {
		return paths
	}

	// Group paths by their parent
	type parentInfo struct {
		parent   string
		children []string
	}
	parentMap := make(map[string]*parentInfo)

	for _, path := range paths {
		parts := strings.Split(path, ".")
		if len(parts) <= 1 {
			// Top-level field, can't optimize
			continue
		}

		parentPath := strings.Join(parts[:len(parts)-1], ".")
		if parentMap[parentPath] == nil {
			parentMap[parentPath] = &parentInfo{parent: parentPath, children: []string{}}
		}
		parentMap[parentPath].children = append(parentMap[parentPath].children, path)
	}

	// For each parent, check if we're deleting all its children
	result := make([]string, 0, len(paths))
	processedPaths := make(map[string]bool)

	for parentPath, info := range parentMap {
		// Get the parent object from observed
		parentParts := strings.Split(parentPath, ".")
		parentObj, exists, err := kunstructured.NestedFieldNoCopy(observed.Object, parentParts...)
		if !exists || err != nil {
			// Parent doesn't exist, just keep the original paths
			for _, child := range info.children {
				if !processedPaths[child] {
					result = append(result, child)
					processedPaths[child] = true
				}
			}
			continue
		}

		// Check if parent is a map and count its children
		parentMap, ok := parentObj.(map[string]interface{})
		if !ok {
			// Not a map, keep original paths
			for _, child := range info.children {
				if !processedPaths[child] {
					result = append(result, child)
					processedPaths[child] = true
				}
			}
			continue
		}

		// If we're deleting all children of this parent, check if we can delete the parent
		if len(info.children) == len(parentMap) {
			// Before deleting the parent, verify it doesn't have children in desired
			parentParts := strings.Split(parentPath, ".")
			desiredParent, desiredExists, _ := kunstructured.NestedFieldNoCopy(desired.Object, parentParts...)

			// Only delete parent if it doesn't exist in desired or is empty/nil
			canDeleteParent := !desiredExists || desiredParent == nil
			if !canDeleteParent {
				if desiredParentMap, ok := desiredParent.(map[string]interface{}); ok {
					canDeleteParent = len(desiredParentMap) == 0
				}
			}

			if canDeleteParent {
				if !processedPaths[parentPath] {
					result = append(result, parentPath)
					processedPaths[parentPath] = true
				}
				for _, child := range info.children {
					processedPaths[child] = true
				}
			} else {
				// Parent has content in desired, keep the original child paths
				for _, child := range info.children {
					if !processedPaths[child] {
						result = append(result, child)
						processedPaths[child] = true
					}
				}
			}
		} else {
			// Not all children, keep the originals
			for _, child := range info.children {
				if !processedPaths[child] {
					result = append(result, child)
					processedPaths[child] = true
				}
			}
		}
	}

	// Add any paths that weren't part of a group (top-level fields)
	for _, path := range paths {
		if !processedPaths[path] {
			result = append(result, path)
		}
	}

	// Recursively apply optimization - the parent we just added might itself
	// be the only child of its parent
	if len(result) != len(paths) {
		return replaceWithParentIfAllSiblingsDeleted(observed, desired, result)
	}

	return result
}

// findMissingFields compares observed and desired resources to find field paths
// that exist in observed but not in desired.
func findMissingFields(observed, desired *kunstructured.Unstructured, ownedPaths []string) []string {
	var missing []string

	for _, path := range ownedPaths {
		parts := strings.Split(path, ".")

		// Check if field exists in observed
		observedVal, observedExists, err := kunstructured.NestedFieldNoCopy(
			observed.Object,
			parts...,
		)
		if err != nil || !observedExists || observedVal == nil {
			// Field doesn't exist in observed or is already nil
			continue
		}

		// Check if field exists in desired
		desiredVal, desiredExists, err := kunstructured.NestedFieldNoCopy(
			desired.Object,
			parts...,
		)

		// If exists in observed but not in desired (or is nil in desired), mark for deletion
		if !desiredExists || (err == nil && desiredVal == nil) {
			missing = append(missing, path)
		}
	}

	return missing
}

// setFieldsToNil explicitly sets the specified field paths to nil in the object.
// This is necessary for server-side apply to recognize that these fields should
// be deleted.
func setFieldsToNil(obj *kunstructured.Unstructured, pathsToDelete []string) error {
	for _, path := range pathsToDelete {
		parts := strings.Split(path, ".")
		if len(parts) == 0 {
			continue
		}

		// We need to set the field to nil, not remove it entirely
		// Navigate to the parent and set the field to nil
		if len(parts) == 1 {
			// Top-level field
			obj.Object[parts[0]] = nil
			continue
		}

		parentPath := parts[:len(parts)-1]
		fieldName := parts[len(parts)-1]

		// Ensure parent path exists
		if err := ensureParentPath(obj.Object, parentPath); err != nil {
			return errors.Wrapf(err, "cannot ensure parent path for %s", path)
		}

		// Get the parent object
		parent, found, err := kunstructured.NestedFieldNoCopy(obj.Object, parentPath...)
		if err != nil {
			return errors.Wrapf(err, "cannot get parent for path %s", path)
		}

		if !found {
			// Parent doesn't exist, which is unexpected since we just ensured it
			continue
		}

		// Set the field to nil in the parent
		if parentMap, ok := parent.(map[string]interface{}); ok {
			parentMap[fieldName] = nil
		}
	}

	return nil
}

// ensureParentPath ensures that the parent path exists in the object, creating
// empty maps as needed.
func ensureParentPath(obj map[string]interface{}, path []string) error {
	current := obj
	for i, segment := range path {
		if val, exists := current[segment]; exists {
			// Path segment exists, check if it's a map
			if nextMap, ok := val.(map[string]interface{}); ok {
				current = nextMap
			} else {
				// Path exists but is not a map, we can't create nested structure
				return errors.Errorf("path segment %s at position %d is not a map", segment, i)
			}
		} else {
			// Path segment doesn't exist, create it
			newMap := make(map[string]interface{})
			current[segment] = newMap
			current = newMap
		}
	}
	return nil
}
