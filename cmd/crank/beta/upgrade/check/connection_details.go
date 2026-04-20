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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
)

const (
	categoryCompositeConnectionDetails = "composite-connection-details"

	remediationCompositeConnectionDetails = "Compose a Kubernetes Secret explicitly from the desired connection detail values."
	docsCompositeConnectionDetails        = ""
)

// CompositeConnectionDetails finds Compositions, XRs, and Claims that rely on
// built-in composite resource connection details, which are removed in
// Crossplane v2.
type CompositeConnectionDetails struct {
	Client client.Client
	// Namespace, if non-empty, restricts Claim instance lookups to a single
	// namespace. Empty means list across all namespaces.
	Namespace string
}

// Category returns the check's stable identifier.
func (c *CompositeConnectionDetails) Category() string {
	return categoryCompositeConnectionDetails
}

// Title returns the check's human-readable title.
func (c *CompositeConnectionDetails) Title() string {
	return "Composite resource connection details"
}

// Description explains what this check looks for.
func (c *CompositeConnectionDetails) Description() string {
	return "Crossplane v2 removes built-in composite resource connection details. Compositions and composites must publish connection details explicitly via composed Secrets."
}

// Remediation returns the once-per-section advice for this check.
func (c *CompositeConnectionDetails) Remediation() string {
	return remediationCompositeConnectionDetails
}

// DocsURL returns the documentation link for this check.
func (c *CompositeConnectionDetails) DocsURL() string { return docsCompositeConnectionDetails }

// Run emits a Finding for each Composition that sets
// spec.writeConnectionSecretsToNamespace and each XR or Claim instance that
// sets spec.writeConnectionSecretToRef.
func (c *CompositeConnectionDetails) Run(ctx context.Context) ([]Finding, error) {
	var findings []Finding

	comps := &apiextensionsv1.CompositionList{}
	if err := c.Client.List(ctx, comps); err != nil {
		return nil, errors.Wrap(err, "cannot list Compositions")
	}

	compAPIVersion := apiextensionsv1.SchemeGroupVersion.String()
	for i := range comps.Items {
		comp := &comps.Items[i]
		if comp.Spec.WriteConnectionSecretsToNamespace == nil || *comp.Spec.WriteConnectionSecretsToNamespace == "" {
			continue
		}
		findings = append(findings, Finding{
			Category: categoryCompositeConnectionDetails,
			Resource: ResourceRef{
				APIVersion: compAPIVersion,
				Kind:       apiextensionsv1.CompositionKind,
				Name:       comp.Name,
			},
			FieldPath: ".spec.writeConnectionSecretsToNamespace",
		})
	}

	types, err := DiscoverXRsAndClaims(ctx, c.Client)
	if err != nil {
		return nil, errors.Wrap(err, "cannot discover XR and Claim types")
	}

	for _, t := range types {
		instances, err := ListInstances(ctx, c.Client, t, c.Namespace)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot list instances of %s", t.GVK.String())
		}
		for i := range instances {
			inst := instances[i]
			ref, found, err := unstructured.NestedMap(inst.Object, "spec", "writeConnectionSecretToRef")
			if err != nil {
				return nil, errors.Wrapf(err, "cannot read spec.writeConnectionSecretToRef on %s/%s", inst.GetKind(), inst.GetName())
			}
			if !found || ref == nil {
				continue
			}
			findings = append(findings, Finding{
				Category:  categoryCompositeConnectionDetails,
				Resource:  ResourceRefFromUnstructured(inst),
				FieldPath: ".spec.writeConnectionSecretToRef",
			})
		}
	}

	return findings, nil
}
