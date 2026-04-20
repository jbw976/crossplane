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
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	secretsv1alpha1 "github.com/crossplane/crossplane/apis/secrets/v1alpha1"
)

// ExternalSecretStores surfaces use of the external secret stores alpha
// feature, which is removed in Crossplane v2. It looks for StoreConfig CRs,
// Compositions that set publishConnectionDetailsWithStoreConfigRef, and
// XR/Claim instances that set spec.publishConnectionDetailsTo.
type ExternalSecretStores struct {
	Client     client.Client
	Namespaces []string
}

const (
	externalSecretStoresCategory    = "external-secret-stores"
	externalSecretStoresTitle       = "External secret stores"
	externalSecretStoresDescription = "Crossplane v2 removes support for external secret stores (alpha). Remove StoreConfig usage and migrate to regular Kubernetes Secrets."
	externalSecretStoresRemediation = "External secret stores are removed in Crossplane v2. Migrate to storing connection details in regular Kubernetes Secrets."
)

// Category returns the check category identifier.
func (c *ExternalSecretStores) Category() string { return externalSecretStoresCategory }

// Title returns the human-readable title.
func (c *ExternalSecretStores) Title() string { return externalSecretStoresTitle }

// Description returns a longer explanation of what this check looks for.
func (c *ExternalSecretStores) Description() string { return externalSecretStoresDescription }

// Run executes the check.
func (c *ExternalSecretStores) Run(ctx context.Context) ([]Finding, error) {
	var findings []Finding

	storeConfigs := &secretsv1alpha1.StoreConfigList{}
	if err := c.Client.List(ctx, storeConfigs); err != nil {
		return nil, errors.Wrap(err, "cannot list StoreConfigs")
	}
	for i := range storeConfigs.Items {
		sc := &storeConfigs.Items[i]
		findings = append(findings, Finding{
			Category: externalSecretStoresCategory,
			Severity: SeverityError,
			Resource: ResourceRef{
				APIVersion: secretsv1alpha1.SchemeGroupVersion.String(),
				Kind:       "StoreConfig",
				Name:       sc.Name,
			},
			Message:     fmt.Sprintf("StoreConfig %q exists; external secret stores are removed in v2.", sc.Name),
			Remediation: externalSecretStoresRemediation,
		})
	}

	comps := &apiextensionsv1.CompositionList{}
	if err := c.Client.List(ctx, comps); err != nil {
		return nil, errors.Wrap(err, "cannot list Compositions")
	}
	for i := range comps.Items {
		comp := &comps.Items[i]
		if comp.Spec.PublishConnectionDetailsWithStoreConfigRef == nil {
			continue
		}
		findings = append(findings, Finding{
			Category: externalSecretStoresCategory,
			Severity: SeverityError,
			Resource: ResourceRef{
				APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
				Kind:       "Composition",
				Name:       comp.Name,
			},
			FieldPath:   ".spec.publishConnectionDetailsWithStoreConfigRef",
			Message:     fmt.Sprintf("Composition %q sets publishConnectionDetailsWithStoreConfigRef; external secret stores are removed in v2.", comp.Name),
			Remediation: externalSecretStoresRemediation,
		})
	}

	types, err := DiscoverXRsAndClaims(ctx, c.Client)
	if err != nil {
		return nil, errors.Wrap(err, "cannot discover XR and Claim types")
	}
	for _, t := range types {
		instances, err := ListInstances(ctx, c.Client, t, c.Namespaces)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot list instances of %s", t.GVK)
		}
		for _, inst := range instances {
			m, found, err := unstructured.NestedMap(inst.Object, "spec", "publishConnectionDetailsTo")
			if err != nil || !found || m == nil {
				continue
			}
			findings = append(findings, Finding{
				Category:    externalSecretStoresCategory,
				Severity:    SeverityError,
				Resource:    ResourceRefFromUnstructured(inst),
				FieldPath:   ".spec.publishConnectionDetailsTo",
				Message:     fmt.Sprintf("%s %q sets publishConnectionDetailsTo; external secret stores are removed in v2.", inst.GetKind(), inst.GetName()),
				Remediation: externalSecretStoresRemediation,
			})
		}
	}

	return findings, nil
}
