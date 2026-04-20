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

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
)

const (
	categoryNativePatchAndTransform = "native-patch-and-transform"

	remediationNativePatchAndTransform = "Migrate to Composition Functions (spec.mode: Pipeline with spec.pipeline steps)."
	docsNativePatchAndTransform        = "https://docs.crossplane.io/latest/concepts/compositions/#use-composition-functions"
)

// NativePatchAndTransform finds Compositions that rely on native
// patch-and-transform, which is removed in Crossplane v2.
type NativePatchAndTransform struct {
	Client client.Client
}

// Category returns the check's stable identifier.
func (c *NativePatchAndTransform) Category() string {
	return categoryNativePatchAndTransform
}

// Title returns the check's human-readable title.
func (c *NativePatchAndTransform) Title() string {
	return "Native patch-and-transform Compositions"
}

// Description explains what this check looks for.
func (c *NativePatchAndTransform) Description() string {
	return "Crossplane v2 removes native patch-and-transform (P&T) Composition. Compositions must use mode: Pipeline with Composition Functions."
}

// Remediation returns the once-per-section advice for this check.
func (c *NativePatchAndTransform) Remediation() string { return remediationNativePatchAndTransform }

// DocsURL returns the documentation link for this check.
func (c *NativePatchAndTransform) DocsURL() string { return docsNativePatchAndTransform }

// Run lists Compositions and emits a Finding for each field that indicates
// native P&T usage. A single Composition may produce multiple findings.
func (c *NativePatchAndTransform) Run(ctx context.Context) ([]Finding, error) {
	list := &apiextensionsv1.CompositionList{}
	if err := c.Client.List(ctx, list); err != nil {
		return nil, errors.Wrap(err, "cannot list Compositions")
	}

	apiVersion := apiextensionsv1.SchemeGroupVersion.String()

	var findings []Finding
	for i := range list.Items {
		comp := &list.Items[i]
		ref := ResourceRef{
			APIVersion: apiVersion,
			Kind:       apiextensionsv1.CompositionKind,
			Name:       comp.Name,
		}

		// A nil Mode defaults to Resources (native P&T); only Pipeline opts out.
		if comp.Spec.Mode == nil || *comp.Spec.Mode == apiextensionsv1.CompositionModeResources {
			findings = append(findings, Finding{
				Category:  categoryNativePatchAndTransform,
				Resource:  ref,
				FieldPath: ".spec.mode",
			})
		}

		// Reading deprecated fields is the whole point of this check.
		if len(comp.Spec.Resources) > 0 { //nolint:staticcheck // intentional read of deprecated field
			findings = append(findings, Finding{
				Category:  categoryNativePatchAndTransform,
				Resource:  ref,
				FieldPath: ".spec.resources",
			})
		}

		if len(comp.Spec.PatchSets) > 0 { //nolint:staticcheck // intentional read of deprecated field
			findings = append(findings, Finding{
				Category:  categoryNativePatchAndTransform,
				Resource:  ref,
				FieldPath: ".spec.patchSets",
			})
		}
	}

	return findings, nil
}
