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

	"github.com/google/go-containerregistry/pkg/name"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/pkg/v1"
)

const (
	unqualifiedPackageSourceCategory = "unqualified-package-source"

	unqualifiedPackageSourceRemediation = "Prefix the package with its fully qualified registry hostname (e.g. xpkg.crossplane.io/crossplane-contrib/provider-nop:v0.4.0)."
	unqualifiedPackageSourceDocs        = "https://docs.crossplane.io/latest/guides/upgrade-to-crossplane-v2/#default-registry-flag"
)

// UnqualifiedPackageSources finds Providers, Configurations, and Functions
// whose spec.package is not fully qualified with a registry hostname.
// Crossplane v2 removes the default registry and the --registry flag, so every
// package reference must include its registry explicitly.
type UnqualifiedPackageSources struct {
	Client client.Client
}

// Category returns the check's stable identifier.
func (c *UnqualifiedPackageSources) Category() string {
	return unqualifiedPackageSourceCategory
}

// Title returns the check's human-readable title.
func (c *UnqualifiedPackageSources) Title() string {
	return "Unqualified package sources"
}

// Severity returns the severity of findings produced by this check.
func (c *UnqualifiedPackageSources) Severity() Severity { return SeverityError }

// Description explains what this check looks for.
func (c *UnqualifiedPackageSources) Description() string {
	return "Crossplane v2 removes the --registry flag and its implicit default registry. Every Provider, Configuration, and Function spec.package must be a fully qualified reference including its registry hostname. References that cannot be parsed as OCI references at all are flagged here too: they will not pull either way and need to be fixed."
}

// Remediation returns the once-per-section advice for this check.
func (c *UnqualifiedPackageSources) Remediation() string {
	return unqualifiedPackageSourceRemediation
}

// DocsURLs returns documentation links for this check.
func (c *UnqualifiedPackageSources) DocsURLs() []string {
	return []string{unqualifiedPackageSourceDocs}
}

// Run lists all package types and emits a Finding for each one whose
// spec.package is not a fully qualified registry reference.
func (c *UnqualifiedPackageSources) Run(ctx context.Context) ([]Finding, error) {
	apiVersion := pkgv1.SchemeGroupVersion.String()
	var findings []Finding

	providers := &pkgv1.ProviderList{}
	if err := c.Client.List(ctx, providers); err != nil {
		return findings, errors.Wrap(err, "cannot list Providers")
	}
	for i := range providers.Items {
		p := &providers.Items[i]
		if f, ok := findingFor(p.Spec.Package, ResourceRef{
			APIVersion: apiVersion,
			Kind:       pkgv1.ProviderKind,
			Name:       p.Name,
		}); ok {
			findings = append(findings, f)
		}
	}

	configurations := &pkgv1.ConfigurationList{}
	if err := c.Client.List(ctx, configurations); err != nil {
		return findings, errors.Wrap(err, "cannot list Configurations")
	}
	for i := range configurations.Items {
		cfg := &configurations.Items[i]
		if f, ok := findingFor(cfg.Spec.Package, ResourceRef{
			APIVersion: apiVersion,
			Kind:       pkgv1.ConfigurationKind,
			Name:       cfg.Name,
		}); ok {
			findings = append(findings, f)
		}
	}

	functions := &pkgv1.FunctionList{}
	if err := c.Client.List(ctx, functions); err != nil {
		return findings, errors.Wrap(err, "cannot list Functions")
	}
	for i := range functions.Items {
		fn := &functions.Items[i]
		if f, ok := findingFor(fn.Spec.Package, ResourceRef{
			APIVersion: apiVersion,
			Kind:       pkgv1.FunctionKind,
			Name:       fn.Name,
		}); ok {
			findings = append(findings, f)
		}
	}

	return findings, nil
}

// findingFor returns a Finding describing a problematic package source, or
// ok=false if pkg is empty or already fully qualified.
//
// Two cases produce a finding, distinguished by the FieldPath the finding
// reports: pkg is parseable but lacks a registry hostname (FieldPath
// ".spec.package"), or pkg is not a parseable OCI reference at all
// (FieldPath ".spec.package (unparseable)"). Both fail under v2's
// no-default-registry rule. Empty packages are skipped; that's a validation
// issue for a different layer.
func findingFor(pkg string, ref ResourceRef) (Finding, bool) {
	if pkg == "" {
		return Finding{}, false
	}
	// Parse with an empty default registry: go-containerregistry treats the
	// first path segment as a registry only when it contains "." or ":",
	// matching v2's rule, so an unqualified reference parses but leaves the
	// registry component empty.
	parsed, err := name.ParseReference(pkg, name.WithDefaultRegistry(""))
	if err == nil && parsed.Context().RegistryStr() != "" {
		return Finding{}, false
	}
	fieldPath := ".spec.package"
	if err != nil {
		fieldPath = ".spec.package (unparseable)"
	}
	return Finding{
		Category:  unqualifiedPackageSourceCategory,
		Resource:  ref,
		FieldPath: fieldPath,
	}, true
}
