// Command kubectl-sidecars is a kubectl plugin. This file provides the generic
// framework for a kubectl extension built on the upstream cli-runtime library;
// the plugin-specific logic lives in run.
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes"
)

// Build information. These are set at build time via -ldflags -X, both by the
// Makefile and by goreleaser. They default to values suitable for a plain
// `go build` or `go install`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionString returns a human-readable one-line version summary.
func versionString() string {
	return fmt.Sprintf("kubectl-sidecars %s (commit %s, built %s)", version, commit, date)
}

// options holds everything needed to run the plugin: the standard kubectl
// connection flags (via genericclioptions) plus this plugin's own flags and
// IO streams.
type options struct {
	configFlags *genericclioptions.ConfigFlags

	allNamespaces bool
	runningOnly   bool
	countOnly     bool
	showVersion   bool
	imagePatterns []string

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
	flags.BoolVarP(&o.allNamespaces, "all-namespaces", "A", o.allNamespaces,
		"List pods across all namespaces.")
	flags.BoolVar(&o.runningOnly, "running-only", o.runningOnly,
		"Only show pods in the Running phase (skip Pending, Succeeded, Failed, etc.).")
	flags.BoolVar(&o.countOnly, "count", o.countOnly,
		"Instead of listing pods, list each unique matching sidecar image and the number of pods running it.")
	flags.BoolVar(&o.showVersion, "version", o.showVersion,
		"Print version information and exit.")

	o.configFlags.AddFlags(flags)
}

// complete fills in any values derived from the parsed positional arguments.
// Each positional argument is a glob pattern to match sidecar images against.
func (o *options) complete(args []string) error {
	o.imagePatterns = args
	return nil
}

// validate checks that the options are internally consistent, including that
// every image pattern compiles.
func (o *options) validate(args []string) error {
	for _, p := range o.imagePatterns {
		if _, err := compileGlob(p); err != nil {
			return fmt.Errorf("invalid image pattern %q: %w", p, err)
		}
	}
	return nil
}

// podFilter reports whether a pod should be kept. Filters are composed by
// filterPods, so each one only needs to decide a single pod.
type podFilter func(*corev1.Pod) bool

// filterPods returns the pods that satisfy every provided filter. With no
// filters it returns all pods.
func filterPods(pods []corev1.Pod, filters ...podFilter) []corev1.Pod {
	var out []corev1.Pod
	for i := range pods {
		keep := true
		for _, f := range filters {
			if !f(&pods[i]) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, pods[i])
		}
	}
	return out
}

// compileGlob translates a shell-style glob into an anchored regexp. Unlike
// path.Match, '*' and '?' also match '/', so a pattern like "*istio*" matches
// images such as "docker.io/istio/proxyv2".
func compileGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// isNativeSidecar reports whether an init container is a native (sidecar) init
// container, i.e. one with a restartPolicy of Always.
func isNativeSidecar(c *corev1.Container) bool {
	return c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

// sidecarImages returns the images of a pod's sidecar containers: every
// .spec.containers[] entry plus native sidecars (.spec.initContainers[] whose
// restartPolicy is Always).
func sidecarImages(pod *corev1.Pod) []string {
	var imgs []string
	for i := range pod.Spec.Containers {
		imgs = append(imgs, pod.Spec.Containers[i].Image)
	}
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if isNativeSidecar(c) {
			imgs = append(imgs, c.Image)
		}
	}
	return imgs
}

// sidecarMatcher matches sidecar container images against a set of glob
// patterns. A matcher with no patterns matches every sidecar image.
type sidecarMatcher struct {
	patterns []*regexp.Regexp
}

func newSidecarMatcher(patterns ...string) (*sidecarMatcher, error) {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := compileGlob(p)
		if err != nil {
			return nil, fmt.Errorf("invalid image pattern %q: %w", p, err)
		}
		res = append(res, re)
	}
	return &sidecarMatcher{patterns: res}, nil
}

// matches reports whether image matches any pattern. With no patterns every
// image matches.
func (m *sidecarMatcher) matches(image string) bool {
	if len(m.patterns) == 0 {
		return true
	}
	for _, re := range m.patterns {
		if re.MatchString(image) {
			return true
		}
	}
	return false
}

// images returns the pod's sidecar images that match the patterns. When no
// patterns are set this is every sidecar image.
func (m *sidecarMatcher) images(pod *corev1.Pod) []string {
	var out []string
	for _, img := range sidecarImages(pod) {
		if m.matches(img) {
			out = append(out, img)
		}
	}
	return out
}

// filter returns a podFilter that keeps pods with at least one matching
// sidecar.
func (m *sidecarMatcher) filter() podFilter {
	return func(pod *corev1.Pod) bool {
		return len(m.images(pod)) > 0
	}
}

// isRunning reports whether a pod is in the Running phase. This skips pods that
// are Pending, Succeeded/Completed, or Failed. It does not require containers to
// be Ready: a container whose process is running but whose readiness probe is
// failing still counts, matching how kubectl reports the pod as Running.
func isRunning(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning
}

// getPods lists pods from the cluster. When allNamespaces is set it lists pods
// in every namespace; otherwise it uses the namespace resolved from the
// standard kubectl connection flags (falling back to the kubeconfig default).
func (o *options) getPods(ctx context.Context, clientset kubernetes.Interface) ([]corev1.Pod, error) {
	namespace := ""
	if !o.allNamespaces {
		var err error
		namespace, _, err = o.configFlags.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return nil, fmt.Errorf("resolving namespace: %w", err)
		}
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	return pods.Items, nil
}

// run contains the plugin logic: list pods, keep those with a matching
// sidecar, and print the results.
func (o *options) run(ctx context.Context) error {
	restConfig, err := o.configFlags.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("loading kube config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}

	pods, err := o.getPods(ctx, clientset)
	if err != nil {
		return err
	}

	sidecarFilter, err := newSidecarMatcher(o.imagePatterns...)
	if err != nil {
		return err
	}

	filters := []podFilter{sidecarFilter.filter()}
	if o.runningOnly {
		filters = append(filters, isRunning)
	}

	matched := filterPods(pods, filters...)
	if o.countOnly {
		return o.printCounts(matched, sidecarFilter)
	}
	return o.printPods(matched, sidecarFilter)
}

// printCounts writes each unique matching sidecar image and the number of pods
// running it, sorted by image name.
func (o *options) printCounts(pods []corev1.Pod, matcher *sidecarMatcher) error {
	counts := map[string]int{}
	for i := range pods {
		seen := map[string]bool{}
		for _, img := range matcher.images(&pods[i]) {
			if seen[img] {
				continue
			}
			seen[img] = true
			counts[img]++
		}
	}

	images := make([]string, 0, len(counts))
	for img := range counts {
		images = append(images, img)
	}
	sort.Strings(images)

	w := tabwriter.NewWriter(o.Out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "SIDECAR\tPODS")
	for _, img := range images {
		fmt.Fprintf(w, "%s\t%d\n", img, counts[img])
	}
	return w.Flush()
}

// printPods writes the matching pods and their matching sidecar images as a
// table.
func (o *options) printPods(pods []corev1.Pod, matcher *sidecarMatcher) error {
	w := tabwriter.NewWriter(o.Out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tPOD\tSIDECARS")
	for i := range pods {
		pod := &pods[i]
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			pod.Namespace, pod.Name, strings.Join(matcher.images(pod), ","))
	}
	return w.Flush()
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

	if o.showVersion {
		fmt.Fprintln(streams.Out, versionString())
		return
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
