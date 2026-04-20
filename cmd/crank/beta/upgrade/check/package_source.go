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
	categoryUnqualifiedPackageSource = "unqualified-package-source"

	remediationUnqualifiedPackageSource = "Prefix the package with its fully qualified registry hostname (e.g. xpkg.crossplane.io/crossplane-contrib/provider-nop:v0.4.0)."
	docsUnqualifiedPackageSource        = ""
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
	return categoryUnqualifiedPackageSource
}

// Title returns the check's human-readable title.
func (c *UnqualifiedPackageSources) Title() string {
	return "Unqualified package sources"
}

// Description explains what this check looks for.
func (c *UnqualifiedPackageSources) Description() string {
	return "Crossplane v2 removes the default registry and the --registry flag. Package sources must be specified with a fully qualified registry hostname."
}

// Remediation returns the once-per-section advice for this check.
func (c *UnqualifiedPackageSources) Remediation() string {
	return remediationUnqualifiedPackageSource
}

// DocsURL returns the documentation link for this check.
func (c *UnqualifiedPackageSources) DocsURL() string { return docsUnqualifiedPackageSource }

// Run lists all package types and emits a Finding for each one whose
// spec.package is not a fully qualified registry reference.
func (c *UnqualifiedPackageSources) Run(ctx context.Context) ([]Finding, error) {
	apiVersion := pkgv1.SchemeGroupVersion.String()
	var findings []Finding

	providers := &pkgv1.ProviderList{}
	if err := c.Client.List(ctx, providers); err != nil {
		return nil, errors.Wrap(err, "cannot list Providers")
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
		return nil, errors.Wrap(err, "cannot list Configurations")
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
		return nil, errors.Wrap(err, "cannot list Functions")
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

// findingFor returns a Finding describing an unqualified package source, or
// ok=false if the package is empty or already qualified. Callers skip empty
// package strings rather than reporting them, since an empty package is an
// unrelated validation problem for a different check to surface.
func findingFor(pkg string, ref ResourceRef) (Finding, bool) {
	if pkg == "" {
		return Finding{}, false
	}
	if isQualified(pkg) {
		return Finding{}, false
	}
	return Finding{
		Category:  categoryUnqualifiedPackageSource,
		Resource:  ref,
		FieldPath: ".spec.package",
	}, true
}

// isQualified reports whether pkg includes a registry hostname.
//
// Parsing with an empty default registry leaves the registry component empty
// when the user didn't write one — go-containerregistry's repository parser
// treats the first path segment as a registry only when it contains a "." or
// ":". This matches the rule Crossplane v2 enforces (the implicit default
// registry is gone), and it's the same pattern internal/initializer uses to
// strip implicit registries from package sources.
//
// Unparseable references aren't flagged: malformed package sources are a
// validation problem for a different layer to surface, and we don't want
// noise here.
func isQualified(pkg string) bool {
	ref, err := name.ParseReference(pkg, name.WithDefaultRegistry(""))
	if err != nil {
		return true
	}
	return ref.Context().RegistryStr() != ""
}
