// Command kubectl-sidecars is a kubectl plugin. This file provides the generic
// framework for a kubectl extension built on the upstream cli-runtime library;
// the plugin-specific logic lives in run.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes"
)

// options holds everything needed to run the plugin: the standard kubectl
// connection flags (via genericclioptions) plus this plugin's own flags and
// IO streams.
type options struct {
	configFlags *genericclioptions.ConfigFlags

	genericiooptions.IOStreams
}

func newOptions(streams genericiooptions.IOStreams) *options {
	return &options{
		configFlags: genericclioptions.NewConfigFlags(true),
		IOStreams:   streams,
	}
}

// bindFlags registers this plugin's flags followed by the standard kubectl
// connection flags (-n/--namespace, --context, --kubeconfig, etc.).
func (o *options) bindFlags(flags *pflag.FlagSet) {
	o.configFlags.AddFlags(flags)
}

// complete fills in any values derived from the parsed positional arguments.
func (o *options) complete(args []string) error {
	return nil
}

// validate checks that the options are internally consistent.
func (o *options) validate(args []string) error {
	return nil
}

// run contains the plugin logic. It builds a Kubernetes client from the
// resolved connection flags; the actual work is not implemented yet.
func (o *options) run(ctx context.Context) error {
	restConfig, err := o.configFlags.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("loading kube config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}
	_ = clientset

	return fmt.Errorf("not implemented")
}

func main() {
	streams := genericiooptions.IOStreams{
		In:     os.Stdin,
		Out:    os.Stdout,
		ErrOut: os.Stderr,
	}

	o := newOptions(streams)

	flags := pflag.NewFlagSet("kubectl-sidecars", pflag.ExitOnError)
	pflag.CommandLine = flags
	o.bindFlags(flags)

	if err := flags.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(streams.ErrOut, err)
		os.Exit(1)
	}

	args := flags.Args()
	if err := o.complete(args); err != nil {
		fmt.Fprintln(streams.ErrOut, err)
		os.Exit(1)
	}
	if err := o.validate(args); err != nil {
		fmt.Fprintln(streams.ErrOut, err)
		os.Exit(1)
	}
	if err := o.run(context.Background()); err != nil {
		fmt.Fprintln(streams.ErrOut, err)
		os.Exit(1)
	}
}
