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
	nativePatchAndTransformCategory = "native-patch-and-transform"

	nativePatchAndTransformRemediation = `Migrate to Composition Functions (spec.mode: Pipeline with spec.pipeline steps). Run "crossplane beta convert pipeline-composition" to convert existing Compositions.`
	nativePatchAndTransformDocs        = "https://docs.crossplane.io/latest/guides/upgrade-to-crossplane-v2/#native-patch-and-transform-composition"
	nativePatchAndTransformDocsConvert = "https://docs.crossplane.io/v1.20/cli/command-reference/#beta-convert"
)

// NativePatchAndTransform finds Compositions that rely on native
// patch-and-transform, which is removed in Crossplane v2.
type NativePatchAndTransform struct {
	Client client.Client
}

// Category returns the check's stable identifier.
func (c *NativePatchAndTransform) Category() string {
	return nativePatchAndTransformCategory
}

// Title returns the check's human-readable title.
func (c *NativePatchAndTransform) Title() string {
	return "Native patch-and-transform Compositions"
}

// Severity returns the severity of findings produced by this check.
func (c *NativePatchAndTransform) Severity() Severity { return SeverityError }

// Description explains what this check looks for.
func (c *NativePatchAndTransform) Description() string {
	return "Crossplane v2 removes native patch-and-transform (P&T) Composition. Compositions must use mode: Pipeline with Composition Functions."
}

// Remediation returns the once-per-section advice for this check.
func (c *NativePatchAndTransform) Remediation() string { return nativePatchAndTransformRemediation }

// DocsURLs returns documentation links for this check.
func (c *NativePatchAndTransform) DocsURLs() []string {
	return []string{
		nativePatchAndTransformDocs,
		nativePatchAndTransformDocsConvert,
	}
}

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

		// A nil Mode defaults to Resources; only an explicit Pipeline opts out.
		if comp.Spec.Mode == nil || *comp.Spec.Mode == apiextensionsv1.CompositionModeResources {
			findings = append(findings, Finding{
				Category:  nativePatchAndTransformCategory,
				Resource:  ref,
				FieldPath: ".spec.mode",
			})
		}

		if len(comp.Spec.Resources) > 0 { //nolint:staticcheck // intentional read of deprecated field
			findings = append(findings, Finding{
				Category:  nativePatchAndTransformCategory,
				Resource:  ref,
				FieldPath: ".spec.resources",
			})
		}

		if len(comp.Spec.PatchSets) > 0 { //nolint:staticcheck // intentional read of deprecated field
			findings = append(findings, Finding{
				Category:  nativePatchAndTransformCategory,
				Resource:  ref,
				FieldPath: ".spec.patchSets",
			})
		}
	}

	return findings, nil
}
