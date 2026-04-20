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

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/pkg/v1"
	pkgv1alpha1 "github.com/crossplane/crossplane/apis/pkg/v1alpha1"
)

const (
	controllerConfigCategory    = "controller-config"
	controllerConfigTitle       = "ControllerConfig usage"
	controllerConfigDescription = "Crossplane v2 removes the ControllerConfig type. Use DeploymentRuntimeConfig instead."
	controllerConfigRemediation = "Migrate to DeploymentRuntimeConfig (pkg.crossplane.io/v1beta1). See https://docs.crossplane.io/latest/concepts/providers/#runtime-configuration."
	controllerConfigFieldPath   = ".spec.controllerConfigRef"
)

// ControllerConfigCheck finds usage of the removed ControllerConfig type:
// ControllerConfig CRs themselves and Providers/Functions that still reference
// one via spec.controllerConfigRef.
type ControllerConfigCheck struct {
	Client client.Client
}

// Category returns the check's stable identifier.
func (c *ControllerConfigCheck) Category() string { return controllerConfigCategory }

// Title returns the check's human-readable title.
func (c *ControllerConfigCheck) Title() string { return controllerConfigTitle }

// Description returns a short explanation of the check.
func (c *ControllerConfigCheck) Description() string { return controllerConfigDescription }

// Run lists ControllerConfig CRs and package types that reference a
// ControllerConfig, returning an Error-severity finding for each.
func (c *ControllerConfigCheck) Run(ctx context.Context) ([]Finding, error) {
	var findings []Finding

	ccs := &pkgv1alpha1.ControllerConfigList{}
	if err := c.Client.List(ctx, ccs); err != nil {
		return nil, errors.Wrap(err, "cannot list ControllerConfigs")
	}
	for i := range ccs.Items {
		cc := &ccs.Items[i]
		findings = append(findings, Finding{
			Category: controllerConfigCategory,
			Severity: SeverityError,
			Resource: ResourceRef{
				APIVersion: pkgv1alpha1.SchemeGroupVersion.String(),
				Kind:       pkgv1alpha1.ControllerConfigKind,
				Name:       cc.Name,
			},
			Message:     "ControllerConfig is removed in Crossplane v2.",
			Remediation: controllerConfigRemediation,
		})
	}

	providers := &pkgv1.ProviderList{}
	if err := c.Client.List(ctx, providers); err != nil {
		return nil, errors.Wrap(err, "cannot list Providers")
	}
	for i := range providers.Items {
		p := &providers.Items[i]
		ref := p.GetControllerConfigRef()
		if ref == nil {
			continue
		}
		findings = append(findings, Finding{
			Category: controllerConfigCategory,
			Severity: SeverityError,
			Resource: ResourceRef{
				APIVersion: pkgv1.SchemeGroupVersion.String(),
				Kind:       pkgv1.ProviderKind,
				Name:       p.Name,
			},
			FieldPath:   controllerConfigFieldPath,
			Message:     fmt.Sprintf("Provider %q references ControllerConfig %q, which is removed in Crossplane v2.", p.Name, ref.Name),
			Remediation: controllerConfigRemediation,
		})
	}

	functions := &pkgv1.FunctionList{}
	if err := c.Client.List(ctx, functions); err != nil {
		return nil, errors.Wrap(err, "cannot list Functions")
	}
	for i := range functions.Items {
		f := &functions.Items[i]
		ref := f.GetControllerConfigRef()
		if ref == nil {
			continue
		}
		findings = append(findings, Finding{
			Category: controllerConfigCategory,
			Severity: SeverityError,
			Resource: ResourceRef{
				APIVersion: pkgv1.SchemeGroupVersion.String(),
				Kind:       pkgv1.FunctionKind,
				Name:       f.Name,
			},
			FieldPath:   controllerConfigFieldPath,
			Message:     fmt.Sprintf("Function %q references ControllerConfig %q, which is removed in Crossplane v2.", f.Name, ref.Name),
			Remediation: controllerConfigRemediation,
		})
	}

	return findings, nil
}
