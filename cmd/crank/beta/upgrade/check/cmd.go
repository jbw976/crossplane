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
	"os"

	"github.com/alecthomas/kong"
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
	errInitClient    = "cannot create Kubernetes client"
	errRunChecks     = "cannot run upgrade checks"
	errPrintReport   = "cannot print report"
)

// Cmd checks a Crossplane control plane for features that are removed or
// broken in Crossplane v2.
type Cmd struct {
	Context             string   `help:"Kubernetes context."                                                                                  name:"context"              predictor:"context"   short:"c"`
	Namespaces          []string `help:"Restrict Claim checks to the given namespaces. Defaults to all namespaces."                           name:"namespace"            predictor:"namespace" short:"n"`
	CrossplaneNamespace string   `default:"crossplane-system"                                                                                 help:"Namespace where the Crossplane Deployment runs." name:"crossplane-namespace"`
	CrossplaneSelector  string   `default:"app=crossplane"                                                                                    help:"Label selector for the Crossplane Deployment."   name:"crossplane-selector"`
	Output              string   `default:"text"                                                                                              enum:"text,json"                                       help:"Output format. One of: text, json." name:"output" short:"o"`
}

// Help returns help text for the check command.
func (c *Cmd) Help() string {
	return `
Check a Crossplane control plane for features that are removed or broken in
Crossplane v2. Run this against a v1.x control plane to surface any usage of
APIs that will not work after upgrading to v2.

Exits non-zero if findings with Error severity are reported.

The command checks for:
  - Compositions using native patch-and-transform (removed in v2)
  - ControllerConfig usage (removed in v2; use DeploymentRuntimeConfig)
  - External secret stores usage (removed in v2)
  - Composite resource connection details (removed in v2)
  - Package sources without a fully-qualified registry (--registry removed)
  - Crossplane Deployment flags enabling removed alpha features

Examples:
  # Check the current kubeconfig context across all namespaces
  crossplane beta upgrade check

  # Use a different context and limit Claim checks to specific namespaces
  crossplane beta upgrade check -c prod -n team-a -n team-b

  # Emit JSON for CI/automation
  crossplane beta upgrade check -o json
`
}

// Run executes the check command.
func (c *Cmd) Run(k *kong.Context, logger logging.Logger) error {
	ctx := context.Background()

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: c.Context},
	).ClientConfig()
	if err != nil {
		return errors.Wrap(err, errLoadKubeconfig)
	}
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
		&ExternalSecretStores{Client: kube, Namespaces: c.Namespaces},
		&CompositeConnectionDetails{Client: kube, Namespaces: c.Namespaces},
		&UnqualifiedPackageSources{Client: kube},
		&FeatureFlags{Client: kube, Namespace: c.CrossplaneNamespace, Selector: c.CrossplaneSelector},
	}

	runner := &Runner{Checks: checks, Logger: logger}
	report, err := runner.Run(ctx)
	if err != nil {
		return errors.Wrap(err, errRunChecks)
	}

	p, err := NewPrinter(c.Output)
	if err != nil {
		return err
	}
	if err := p.Print(k.Stdout, report); err != nil {
		return errors.Wrap(err, errPrintReport)
	}

	if report.HasErrors() {
		os.Exit(1)
	}
	return nil
}
