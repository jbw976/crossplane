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

	"github.com/crossplane/crossplane-runtime/pkg/errors"
)

const (
	featureFlagsCategory    = "feature-flags"
	featureFlagsTitle       = "Crossplane Deployment feature flags"
	featureFlagsDescription = "Crossplane v2 removes support for some alpha features that were gated by feature flags. This check inspects the Crossplane Deployment's container args."

	flagEnableExternalSecretStores = "--enable-external-secret-stores"

	messageExternalSecretStoresFlag     = "Crossplane is started with --enable-external-secret-stores, which enables a feature removed in v2."
	remediationExternalSecretStoresFlag = "Remove the --enable-external-secret-stores flag and migrate off external secret stores before upgrading to Crossplane v2."
)

// FeatureFlags inspects the running Crossplane Deployment(s) for container args
// that enable alpha features removed in Crossplane v2.
type FeatureFlags struct {
	Client    client.Client
	Namespace string
	Selector  string
}

// Category returns the check's stable identifier.
func (c *FeatureFlags) Category() string { return featureFlagsCategory }

// Title returns the check's human-readable title.
func (c *FeatureFlags) Title() string { return featureFlagsTitle }

// Description explains what this check looks for.
func (c *FeatureFlags) Description() string { return featureFlagsDescription }

// Run lists Deployments matching the configured selector in the Crossplane
// namespace and emits findings for removed feature flags, plus an informational
// finding reporting the detected Crossplane version per Deployment.
func (c *FeatureFlags) Run(ctx context.Context) ([]Finding, error) {
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
					Category:    featureFlagsCategory,
					Severity:    SeverityError,
					Resource:    ref,
					FieldPath:   fmt.Sprintf(".spec.template.spec.containers[%d].args", ci),
					Message:     messageExternalSecretStoresFlag,
					Remediation: remediationExternalSecretStoresFlag,
				})
			}
		}

		if len(containers) > 0 {
			version := versionFromImage(containers[0].Image)
			findings = append(findings, Finding{
				Category:  featureFlagsCategory,
				Severity:  SeverityInfo,
				Resource:  ref,
				FieldPath: ".spec.template.spec.containers[0].image",
				Message:   fmt.Sprintf("Detected Crossplane version: %s (deployment/%s).", version, d.Name),
			})
		}
	}

	return findings, nil
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

// versionFromImage returns the tag portion of a container image reference, or
// "<unknown>" if the reference has no tag (e.g. it is pinned by digest).
func versionFromImage(image string) string {
	i := strings.LastIndex(image, ":")
	if i < 0 {
		return "<unknown>"
	}
	// A `@sha256:...` digest reference has no tag.
	if strings.Contains(image, "@") {
		return "<unknown>"
	}
	tag := image[i+1:]
	if tag == "" {
		return "<unknown>"
	}
	return tag
}
