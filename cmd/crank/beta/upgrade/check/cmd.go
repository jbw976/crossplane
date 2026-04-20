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
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/logging"

	crossplaneapis "github.com/crossplane/crossplane/apis"
)

const (
	errLoadKubeconfig = "cannot load kubeconfig"
	errInitScheme     = "cannot register Crossplane types with scheme"
	errInitClient     = "cannot create Kubernetes client"
	errPrintReport    = "cannot print report"
	errIssuesFound    = "issues found"
)

// Cmd checks a Crossplane control plane for features that are removed or
// broken in Crossplane v2.
type Cmd struct {
	Kubeconfig           string `help:"Path to the kubeconfig file. Defaults to $KUBECONFIG or ~/.kube/config."             name:"kubeconfig"           type:"existingfile"`
	Context              string `help:"Kubernetes context to use from the kubeconfig."                                       name:"context"              predictor:"context"   short:"c"`
	Namespace            string `help:"Restrict namespaced checks to a single namespace. Defaults to all namespaces."        name:"namespace"            predictor:"namespace" short:"n"`
	CrossplaneNamespace  string `default:"crossplane-system"                                                                 help:"Namespace where the Crossplane Deployment runs." name:"crossplane-namespace"`
	CrossplaneSelector   string `default:"app=crossplane"                                                                    help:"Label selector for the Crossplane Deployment."   name:"crossplane-selector"`
	Output               string `default:"text"                                                                              enum:"text,json"                                       help:"Output format. One of: text, json." name:"output" short:"o"`
	SkipManagedResources bool   `default:"false"                                                                             help:"Skip scanning managed resources for external secret stores usage. Speeds up the check on clusters with many provider CRDs." name:"skip-managed-resources"`
	Concurrency          int    `default:"10"                                                                                help:"Maximum number of managed resource types to list in parallel."                                                              name:"concurrency"`
}

// Help returns help text for the check command.
func (c *Cmd) Help() string {
	return `
Check a Crossplane control plane for features that are removed or broken in
Crossplane v2. Run this against a v1.x control plane to surface any usage of
APIs that will not work after upgrading to v2.

By default the check sweeps the entire control plane: cluster-scoped
resources and all namespaces. Use --namespace to restrict namespaced checks
(currently just Claims) to a single namespace.

Exits non-zero if any error-severity findings are produced or any check
fails to run. Informational findings (flagged with [i]) do not trigger a
non-zero exit; they surface forward-looking work that isn't required for
the upgrade itself.

The command checks for:
  - Compositions using native patch-and-transform (removed in v2)
  - ControllerConfig usage (removed in v2; use DeploymentRuntimeConfig)
  - External secret stores usage (removed in v2): the Crossplane Deployment
    --enable-external-secret-stores flag, StoreConfig CRs, Compositions
    referencing a non-default StoreConfig, managed resources with
    spec.publishConnectionDetailsTo set, Claims with the same field set,
    and XRs with a user-explicit value for that field (auto-injected
    entries, where the entry name equals the XR's UID, are filtered out).
    Use --skip-managed-resources to skip the MR scan on large clusters.
  - Composite resource (XR and Claim) connection details (informational -
    legacy XRs and Claims keep working in v2; migration is only needed if
    you later convert these XRDs to v2-style namespaced XRs; managed
    resources are unaffected either way)
  - Package sources without a fully qualified registry, or that aren't
    parseable as OCI references at all (--registry removed in v2; either
    case fails the new rule)

Examples:
  # Check the current kubeconfig context, all namespaces
  crossplane beta upgrade check

  # Point at a specific kubeconfig and context
  crossplane beta upgrade check --kubeconfig ~/.kube/prod.yaml -c prod

  # Restrict Claim checks to a single namespace
  crossplane beta upgrade check -n team-a

  # Emit JSON for CI/automation
  crossplane beta upgrade check -o json
`
}

// Run executes the check command.
func (c *Cmd) Run(k *kong.Context, logger logging.Logger) error {
	// Cancel in-flight apiserver List calls on Ctrl-C or SIGTERM, so a slow
	// or hung apiserver doesn't leave the operator with a tool that only
	// responds to SIGKILL.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.Kubeconfig != "" {
		// ExplicitPath wins over $KUBECONFIG and ~/.kube/config.
		loadingRules.ExplicitPath = c.Kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: c.Context},
	).ClientConfig()
	if err != nil {
		return errors.Wrap(err, errLoadKubeconfig)
	}
	// controller-runtime no longer sets QPS/Burst on the rest config, so a
	// default client throttles to 5 QPS and the check crawls. Hardcoded rather
	// than exposed; this is a one-shot preflight, not a long-running tool.
	if cfg.QPS == 0 {
		cfg.QPS = 20
	}
	if cfg.Burst == 0 {
		cfg.Burst = 30
	}

	sch := runtime.NewScheme()
	if err := scheme.AddToScheme(sch); err != nil {
		return errors.Wrap(err, errInitScheme)
	}
	if err := extv1.AddToScheme(sch); err != nil {
		return errors.Wrap(err, errInitScheme)
	}
	if err := crossplaneapis.AddToScheme(sch); err != nil {
		return errors.Wrap(err, errInitScheme)
	}

	kube, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		return errors.Wrap(err, errInitClient)
	}

	checks := []Check{
		&NativePatchAndTransform{Client: kube},
		&ControllerConfigCheck{Client: kube},
		&ExternalSecretStores{
			Client:               kube,
			CrossplaneNamespace:  c.CrossplaneNamespace,
			Selector:             c.CrossplaneSelector,
			ClaimNamespace:       c.Namespace,
			SkipManagedResources: c.SkipManagedResources,
			Concurrency:          c.Concurrency,
		},
		&CompositeConnectionDetails{Client: kube, Namespace: c.Namespace},
		&UnqualifiedPackageSources{Client: kube},
	}

	runner := &Runner{Checks: checks, Logger: logger}
	report := runner.Run(ctx)

	p, err := NewPrinter(c.Output)
	if err != nil {
		return err
	}
	if err := p.Print(k.Stdout, report); err != nil {
		return errors.Wrap(err, errPrintReport)
	}

	// Returning a non-nil error here makes kong exit non-zero; the report
	// is already printed. The blank line on stderr separates the report
	// from kong's "crossplane: error: ..." line, which lands on stderr
	// immediately after this return.
	if report.HasErrors() {
		fmt.Fprintln(k.Stderr)
		return errors.New(errIssuesFound)
	}
	return nil
}
