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

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	secretsv1alpha1 "github.com/crossplane/crossplane/apis/secrets/v1alpha1"
)

// ExternalSecretStores surfaces use of the external secret stores alpha
// feature, which is removed in Crossplane v2. The check looks at three
// signals that reliably indicate user intent:
//
//   - The Crossplane Deployment is started with --enable-external-secret-stores.
//   - User-created or user-modified StoreConfig CRs exist. The init
//     controller always creates a "default" StoreConfig with no real config
//     (only DefaultScope set), so flagging that one is noise — we filter it
//     out via isAutoCreatedDefaultStoreConfig.
//   - Compositions whose spec.publishConnectionDetailsWithStoreConfigRef is
//     non-default — the kubebuilder default is {name: "default"}, so every
//     Composition appears to set this field. Only a non-default value is a
//     reliable signal of opted-in usage.
//
// XR and Claim spec.publishConnectionDetailsTo is intentionally NOT checked.
// When --enable-external-secret-stores is on, Crossplane's composite
// reconciler auto-injects this field on every XR using the XR's UID as the
// secret-store entry name, regardless of user intent. There's no reliable
// way to distinguish auto-injection from user configuration on the spec
// alone, so reading the field would flood the report with controller-managed
// noise. The deployment-flag signal already captures whether the user opted
// in at the cluster level.
type ExternalSecretStores struct {
	Client    client.Client
	Namespace string
	Selector  string
}

const (
	externalSecretStoresCategory    = "external-secret-stores"
	externalSecretStoresTitle       = "External secret stores"
	externalSecretStoresDescription = "Crossplane v2 removes external secret stores (alpha). Publish connection details as regular Kubernetes Secrets composed by your Compositions."
	externalSecretStoresRemediation = "Disable --enable-external-secret-stores on the Crossplane Deployment, replace StoreConfig usage with composed Secrets, and delete StoreConfig CRs."
	externalSecretStoresDocs        = ""

	// flagEnableExternalSecretStores is the Crossplane container arg that
	// turns on the alpha external-secret-stores feature.
	flagEnableExternalSecretStores = "--enable-external-secret-stores"

	// defaultStoreConfigName is the kubebuilder default for
	// Composition.spec.publishConnectionDetailsWithStoreConfigRef.name. The
	// apiserver injects this on every Composition whether or not the user
	// wrote the field, so we only flag values that differ from this default.
	defaultStoreConfigName = "default"
)

// Category returns the check category identifier.
func (c *ExternalSecretStores) Category() string { return externalSecretStoresCategory }

// Title returns the human-readable title.
func (c *ExternalSecretStores) Title() string { return externalSecretStoresTitle }

// Description returns a longer explanation of what this check looks for.
func (c *ExternalSecretStores) Description() string { return externalSecretStoresDescription }

// Remediation returns the once-per-section advice for this check.
func (c *ExternalSecretStores) Remediation() string { return externalSecretStoresRemediation }

// DocsURL returns the documentation link for this check.
func (c *ExternalSecretStores) DocsURL() string { return externalSecretStoresDocs }

// Run executes the check.
func (c *ExternalSecretStores) Run(ctx context.Context) ([]Finding, error) {
	var findings []Finding

	deployFindings, err := c.checkDeploymentFlag(ctx)
	if err != nil {
		return nil, err
	}
	findings = append(findings, deployFindings...)

	storeConfigs := &secretsv1alpha1.StoreConfigList{}
	if err := c.Client.List(ctx, storeConfigs); err != nil {
		return nil, errors.Wrap(err, "cannot list StoreConfigs")
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
		return nil, errors.Wrap(err, "cannot list Compositions")
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

	return findings, nil
}

// checkDeploymentFlag inspects the Crossplane Deployment(s) selected by
// Namespace+Selector for the --enable-external-secret-stores container arg.
func (c *ExternalSecretStores) checkDeploymentFlag(ctx context.Context) ([]Finding, error) {
	sel, err := labels.Parse(c.Selector)
	if err != nil {
		return nil, errors.Wrap(err, "cannot parse Crossplane label selector")
	}

	deploys := &appsv1.DeploymentList{}
	if err := c.Client.List(ctx, deploys,
		client.InNamespace(c.Namespace),
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
// "default" StoreConfig that Crossplane v1's init controller creates on every
// cluster (see internal/initializer/store_config.go). The auto-created shape
// has no provider config — only DefaultScope is set, and Type defaults to
// Kubernetes via the kubebuilder default. If the user customized any of the
// provider config fields, the StoreConfig is no longer the auto-created
// shape and we flag it as user-modified.
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
// considered enabled when it appears standalone (optionally followed by a
// non-flag value), or as `flag=<value>` where value is anything other than
// "false". An explicit `flag=false` is treated as disabled.
func containerHasEnabledFlag(args []string, flag string) bool {
	prefix := flag + "="
	for i, a := range args {
		switch {
		case a == flag:
			// A following non-flag token is the value; treat "false" as disabled.
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
