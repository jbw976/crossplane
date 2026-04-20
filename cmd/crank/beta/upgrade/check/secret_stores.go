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
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	secretsv1alpha1 "github.com/crossplane/crossplane/apis/secrets/v1alpha1"
)

// ExternalSecretStores surfaces use of the external secret stores feature,
// removed in Crossplane v2. The check flags:
//
//   - Crossplane Deployments running with --enable-external-secret-stores.
//   - StoreConfig CRs other than the unmodified "default" that v1's init
//     controller creates on every cluster.
//   - Compositions whose spec.publishConnectionDetailsWithStoreConfigRef.name
//     differs from "default". The apiserver injects that name as a kubebuilder
//     default on every Composition, so only other values reflect user intent.
//   - Managed resources and Claims with a non-empty spec.publishConnectionDetailsTo.
//   - XRs with a non-empty spec.publishConnectionDetailsTo whose name field
//     is not the XR's own metadata.uid. The composite reconciler auto-injects
//     this field on every XR using the UID as the entry name when the feature
//     is on, so name == UID is the controller's fingerprint and is filtered
//     out; anything else is a user-explicit override.
type ExternalSecretStores struct {
	Client              client.Client
	CrossplaneNamespace string
	Selector            string
	// ClaimNamespace restricts Claim instance lookups; empty lists across
	// all namespaces. XRs and managed resources are cluster-scoped in v1.
	ClaimNamespace string

	// SkipManagedResources disables the MR scan, which is the expensive part
	// of this check on clusters with many provider CRDs.
	SkipManagedResources bool

	// Concurrency bounds parallel List calls during the MR scan; non-positive
	// values mean unbounded.
	Concurrency int
}

const (
	externalSecretStoresCategory    = "external-secret-stores"
	externalSecretStoresTitle       = "External secret stores"
	externalSecretStoresDescription = "Crossplane v2 removes external secret stores. Publish connection details as Kubernetes Secrets composed by your Compositions, or adopt External Secrets Operator if you need an external store."
	externalSecretStoresRemediation = "Disable --enable-external-secret-stores on the Crossplane Deployment, replace StoreConfig-based publishing with composed Kubernetes Secrets (or adopt External Secrets Operator), then delete StoreConfig resources. No automated converter exists."
	externalSecretStoresDocsUpgrade = "https://docs.crossplane.io/latest/guides/upgrade-to-crossplane-v2/#external-secret-stores"
	externalSecretStoresDocsCompose = "https://docs.crossplane.io/latest/guides/connection-details-composition"
	externalSecretStoresDocsESO     = "https://github.com/external-secrets/external-secrets"

	flagEnableExternalSecretStores = "--enable-external-secret-stores"

	// defaultStoreConfigName is the kubebuilder default the apiserver injects
	// on Composition.spec.publishConnectionDetailsWithStoreConfigRef.name; only
	// values other than this signal user intent.
	defaultStoreConfigName = "default"
)

// Category returns the check category identifier.
func (c *ExternalSecretStores) Category() string { return externalSecretStoresCategory }

// Title returns the human-readable title.
func (c *ExternalSecretStores) Title() string { return externalSecretStoresTitle }

// Severity returns the severity of findings produced by this check.
func (c *ExternalSecretStores) Severity() Severity { return SeverityError }

// Description explains what this check looks for.
func (c *ExternalSecretStores) Description() string { return externalSecretStoresDescription }

// Remediation returns the once-per-section advice for this check.
func (c *ExternalSecretStores) Remediation() string { return externalSecretStoresRemediation }

// DocsURLs returns documentation links for this check.
func (c *ExternalSecretStores) DocsURLs() []string {
	return []string{
		externalSecretStoresDocsUpgrade,
		externalSecretStoresDocsCompose,
		externalSecretStoresDocsESO,
	}
}

// Run emits a Finding for each of the signals described on ExternalSecretStores.
func (c *ExternalSecretStores) Run(ctx context.Context) ([]Finding, error) {
	var findings []Finding

	deployFindings, err := c.checkDeploymentFlag(ctx)
	if err != nil {
		return findings, err
	}
	findings = append(findings, deployFindings...)

	storeConfigs := &secretsv1alpha1.StoreConfigList{}
	if err := c.Client.List(ctx, storeConfigs); err != nil {
		return findings, errors.Wrap(err, "cannot list StoreConfigs")
	}
	for i := range storeConfigs.Items {
		sc := &storeConfigs.Items[i]
		if isAutoCreatedDefaultStoreConfig(sc) {
			continue
		}
		findings = append(findings, Finding{
			Category: externalSecretStoresCategory,
			Resource: ResourceRef{
				APIVersion: secretsv1alpha1.SchemeGroupVersion.String(),
				Kind:       "StoreConfig",
				Name:       sc.Name,
			},
		})
	}

	comps := &apiextensionsv1.CompositionList{}
	if err := c.Client.List(ctx, comps); err != nil {
		return findings, errors.Wrap(err, "cannot list Compositions")
	}
	for i := range comps.Items {
		comp := &comps.Items[i]
		ref := comp.Spec.PublishConnectionDetailsWithStoreConfigRef
		if ref == nil || ref.Name == defaultStoreConfigName {
			continue
		}
		findings = append(findings, Finding{
			Category: externalSecretStoresCategory,
			Resource: ResourceRef{
				APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
				Kind:       "Composition",
				Name:       comp.Name,
			},
			FieldPath: ".spec.publishConnectionDetailsWithStoreConfigRef",
		})
	}

	xrClaimFindings, err := c.checkXRsAndClaims(ctx)
	findings = append(findings, xrClaimFindings...)
	if err != nil {
		return findings, err
	}

	if !c.SkipManagedResources {
		mrFindings, err := c.checkManagedResources(ctx)
		findings = append(findings, mrFindings...)
		if err != nil {
			return findings, err
		}
	}

	return findings, nil
}

// checkXRsAndClaims flags XR and Claim instances with a user-set
// spec.publishConnectionDetailsTo. See the package godoc for the XR UID
// filter rationale.
func (c *ExternalSecretStores) checkXRsAndClaims(ctx context.Context) ([]Finding, error) {
	types, err := DiscoverXRsAndClaims(ctx, c.Client)
	if err != nil {
		return nil, errors.Wrap(err, "cannot discover XR and Claim types")
	}

	var findings []Finding
	for _, t := range types {
		ns := ""
		if t.Namespaced {
			ns = c.ClaimNamespace
		}
		instances, err := ListInstances(ctx, c.Client, t, ns)
		if err != nil {
			return findings, errors.Wrapf(err, "cannot list instances of %s", t.GVK)
		}
		for i := range instances {
			inst := &instances[i]
			v, found, err := unstructured.NestedMap(inst.Object, "spec", "publishConnectionDetailsTo")
			if err != nil {
				return findings, errors.Wrapf(err, "cannot read .spec.publishConnectionDetailsTo on %s/%s", inst.GetKind(), inst.GetName())
			}
			if !found || len(v) == 0 {
				continue
			}
			// Skip the composite reconciler's auto-injected entries on XRs.
			if !t.Namespaced {
				name, _, _ := unstructured.NestedString(v, "name")
				if name == string(inst.GetUID()) {
					continue
				}
			}
			findings = append(findings, Finding{
				Category:  externalSecretStoresCategory,
				Resource:  ResourceRefFromUnstructured(*inst),
				FieldPath: ".spec.publishConnectionDetailsTo",
			})
		}
	}
	return findings, nil
}

// checkManagedResources flags MRs with a user-set spec.publishConnectionDetailsTo.
// One List per MR type runs concurrently. List failures are aggregated rather
// than returned, so one flaky CRD doesn't drop findings from healthy types.
func (c *ExternalSecretStores) checkManagedResources(ctx context.Context) ([]Finding, error) {
	types, err := DiscoverManagedResources(ctx, c.Client)
	if err != nil {
		return nil, errors.Wrap(err, "cannot discover managed resource types")
	}

	var g errgroup.Group
	if c.Concurrency > 0 {
		g.SetLimit(c.Concurrency)
	}

	var (
		mu       sync.Mutex
		findings []Finding
		listErrs []error
	)

	for i := range types {
		t := types[i]
		g.Go(func() error {
			instances, err := ListInstances(ctx, c.Client, t, "")
			if err != nil {
				mu.Lock()
				listErrs = append(listErrs, errors.Wrapf(err, "cannot list %s", t.GVK))
				mu.Unlock()
				return nil
			}
			var local []Finding
			for j := range instances {
				inst := &instances[j]
				v, found, err := unstructured.NestedMap(inst.Object, "spec", "publishConnectionDetailsTo")
				if err != nil {
					mu.Lock()
					listErrs = append(listErrs, errors.Wrapf(err, "cannot read .spec.publishConnectionDetailsTo on %s/%s", inst.GetKind(), inst.GetName()))
					mu.Unlock()
					continue
				}
				if !found || len(v) == 0 {
					continue
				}
				local = append(local, Finding{
					Category:  externalSecretStoresCategory,
					Resource:  ResourceRefFromUnstructured(*inst),
					FieldPath: ".spec.publishConnectionDetailsTo",
				})
			}
			if len(local) > 0 {
				mu.Lock()
				findings = append(findings, local...)
				mu.Unlock()
			}
			// Always nil; errors go to listErrs so errgroup never cancels.
			return nil
		})
	}
	_ = g.Wait()

	if len(listErrs) > 0 {
		return findings, errors.Join(listErrs...)
	}
	return findings, nil
}

// checkDeploymentFlag flags Crossplane Deployments started with
// --enable-external-secret-stores.
func (c *ExternalSecretStores) checkDeploymentFlag(ctx context.Context) ([]Finding, error) {
	sel, err := labels.Parse(c.Selector)
	if err != nil {
		return nil, errors.Wrap(err, "cannot parse Crossplane label selector")
	}

	deploys := &appsv1.DeploymentList{}
	if err := c.Client.List(ctx, deploys,
		client.InNamespace(c.CrossplaneNamespace),
		client.MatchingLabelsSelector{Selector: sel},
	); err != nil {
		return nil, errors.Wrap(err, "cannot list Crossplane Deployments")
	}

	apiVersion := appsv1.SchemeGroupVersion.String()

	var findings []Finding
	for i := range deploys.Items {
		d := &deploys.Items[i]
		ref := ResourceRef{
			APIVersion: apiVersion,
			Kind:       "Deployment",
			Namespace:  d.Namespace,
			Name:       d.Name,
		}
		containers := d.Spec.Template.Spec.Containers
		for ci := range containers {
			ctr := &containers[ci]
			if containerHasEnabledFlag(ctr.Args, flagEnableExternalSecretStores) {
				findings = append(findings, Finding{
					Category:  externalSecretStoresCategory,
					Resource:  ref,
					FieldPath: fmt.Sprintf(".spec.template.spec.containers[%d].args", ci),
				})
			}
		}
	}
	return findings, nil
}

// isAutoCreatedDefaultStoreConfig reports whether sc is the unmodified
// "default" StoreConfig that v1's init controller creates on every cluster.
// The auto-created shape has only DefaultScope set; any user-customized
// provider config disqualifies it.
func isAutoCreatedDefaultStoreConfig(sc *secretsv1alpha1.StoreConfig) bool {
	if sc.Name != defaultStoreConfigName {
		return false
	}
	cfg := sc.Spec.SecretStoreConfig
	if cfg.Type != nil && *cfg.Type != xpv1.SecretStoreKubernetes {
		return false
	}
	if cfg.Kubernetes != nil {
		return false
	}
	if cfg.Plugin != nil {
		return false
	}
	return true
}

// containerHasEnabledFlag reports whether args enables flag. The flag is
// enabled when it appears standalone, as `flag=<anything-but-false>`, or
// followed by a non-flag value other than "false".
func containerHasEnabledFlag(args []string, flag string) bool {
	prefix := flag + "="
	for i, a := range args {
		switch {
		case a == flag:
			if i+1 < len(args) {
				next := args[i+1]
				if !strings.HasPrefix(next, "-") && strings.EqualFold(next, "false") {
					return false
				}
			}
			return true
		case strings.HasPrefix(a, prefix):
			value := strings.TrimPrefix(a, prefix)
			if strings.EqualFold(value, "false") {
				return false
			}
			return true
		}
	}
	return false
}
