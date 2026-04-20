/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package check

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
)

// DiscoveredType describes an XR or Claim type defined by an XRD.
type DiscoveredType struct {
	// GVK of the instance resource served on the cluster.
	GVK schema.GroupVersionKind
	// Namespaced is true for claim types, false for composite (XR) types.
	Namespaced bool
	// XRDName is the name of the XRD that defined this type; useful for error
	// messages when instance discovery fails.
	XRDName string
}

// DiscoverXRsAndClaims walks all XRDs and returns the GVKs of XR and Claim
// instance types. Uses the XRD's referenceable version, falling back to the
// first served version if none is marked referenceable.
func DiscoverXRsAndClaims(ctx context.Context, c client.Client) ([]DiscoveredType, error) {
	xrds := &xv1.CompositeResourceDefinitionList{}
	if err := c.List(ctx, xrds); err != nil {
		return nil, err
	}
	types := make([]DiscoveredType, 0, len(xrds.Items)*2)
	for _, xrd := range xrds.Items {
		v := pickVersion(xrd)
		if v == "" {
			continue
		}
		types = append(types, DiscoveredType{
			GVK:        schema.GroupVersionKind{Group: xrd.Spec.Group, Version: v, Kind: xrd.Spec.Names.Kind},
			Namespaced: false,
			XRDName:    xrd.Name,
		})
		if xrd.Spec.ClaimNames != nil {
			types = append(types, DiscoveredType{
				GVK:        schema.GroupVersionKind{Group: xrd.Spec.Group, Version: v, Kind: xrd.Spec.ClaimNames.Kind},
				Namespaced: true,
				XRDName:    xrd.Name,
			})
		}
	}
	return types, nil
}

// pickVersion returns the name of the XRD version we should query for
// instances. We must query a Served version — the apiserver doesn't expose
// non-served versions, so listing against one returns nothing and silently
// hides resources that need migration. Returns "" if no served version
// exists; the caller should skip the type.
func pickVersion(xrd xv1.CompositeResourceDefinition) string {
	for _, v := range xrd.Spec.Versions {
		if v.Referenceable && v.Served {
			return v.Name
		}
	}
	for _, v := range xrd.Spec.Versions {
		if v.Served {
			return v.Name
		}
	}
	return ""
}

// ListInstances lists instances of a given type. For namespaced types, a
// non-empty namespace restricts the list to that namespace; an empty namespace
// lists across all namespaces. The namespace argument is ignored for
// cluster-scoped types.
func ListInstances(ctx context.Context, c client.Client, t DiscoveredType, namespace string) ([]unstructured.Unstructured, error) {
	listGVK := t.GVK
	listGVK.Kind += "List"

	u := &unstructured.UnstructuredList{}
	u.SetGroupVersionKind(listGVK)
	var opts []client.ListOption
	if t.Namespaced && namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := c.List(ctx, u, opts...); err != nil {
		return nil, err
	}
	return u.Items, nil
}

// ResourceRefFromUnstructured builds a ResourceRef from an unstructured object.
func ResourceRefFromUnstructured(u unstructured.Unstructured) ResourceRef {
	return ResourceRef{
		APIVersion: u.GetAPIVersion(),
		Kind:       u.GetKind(),
		Namespace:  u.GetNamespace(),
		Name:       u.GetName(),
	}
}
