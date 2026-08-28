// Package diff implements the kscope diff command's business logic:
// structured drift detection for Deployments and ConfigMaps between two
// live clusters. See DIFF_SPEC.md at the repo root for the full design.
package diff

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tylermac92/kscope/pkg/types"
)

// systemNamespaces are excluded from the namespace union computed under
// Options.AllNamespaces, unless Options.IncludeSystem is set — mirroring
// internal/rbac's system: exclusion. An explicit Options.Namespace is
// always honored regardless of this set.
var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

func isSystemNamespace(name string) bool {
	return systemNamespaces[name]
}

// Options configures the scope of Analyze.
type Options struct {
	// Namespace restricts the diff to a single namespace. Ignored when
	// AllNamespaces is set. An explicit Namespace is always honored as-is,
	// even if it names a system namespace — IncludeSystem only affects
	// namespace auto-discovery under AllNamespaces.
	Namespace string
	// AllNamespaces diffs every namespace present in either context (the
	// union of both clusters' namespace lists), excluding well-known
	// system namespaces unless IncludeSystem is set.
	AllNamespaces bool
	// IncludeSystem includes kube-system, kube-public, and kube-node-lease
	// in the namespace union when AllNamespaces is set. No effect when
	// Namespace is set explicitly.
	IncludeSystem bool
	// ContextA and ContextB are human-readable labels for clientA/clientB
	// — typically the --context-a/--context-b flag values — used in
	// Finding messages to identify which side a value came from.
	ContextA string
	ContextB string
}

// Analyze compares Deployments and ConfigMaps between two live clusters
// (clientA, clientB — typically two kubeconfig contexts) and returns a
// Report of drift: objects unique to one side, and specific fields that
// differ between matching objects on both sides. Unlike every other kscope
// analyzer, Analyze takes two clientsets — a diff is inherently a
// two-cluster operation. Neither side is a baseline: findings are labeled
// by context name via Options.ContextA/ContextB, not "old"/"new".
func Analyze(ctx context.Context, clientA, clientB kubernetes.Interface, opts Options) (types.Report, error) {
	namespaces, err := resolveNamespaces(ctx, clientA, clientB, opts)
	if err != nil {
		return types.Report{}, err
	}

	var findings []types.Finding

	for _, ns := range namespaces {
		deployFindings, err := diffDeployments(ctx, clientA, clientB, ns, opts)
		if err != nil {
			return types.Report{}, err
		}
		findings = append(findings, deployFindings...)

		cmFindings, err := diffConfigMaps(ctx, clientA, clientB, ns, opts)
		if err != nil {
			return types.Report{}, err
		}
		findings = append(findings, cmFindings...)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Namespace != findings[j].Namespace {
			return findings[i].Namespace < findings[j].Namespace
		}
		if findings[i].Resource != findings[j].Resource {
			return findings[i].Resource < findings[j].Resource
		}
		return findings[i].Name < findings[j].Name
	})

	return types.Report{Findings: findings}, nil
}

// resolveNamespaces returns the namespaces to diff. Under AllNamespaces
// it's the union of both clusters' namespace lists (system namespaces
// pruned unless IncludeSystem); otherwise it's just Options.Namespace.
func resolveNamespaces(ctx context.Context, clientA, clientB kubernetes.Interface, opts Options) ([]string, error) {
	if !opts.AllNamespaces {
		return []string{opts.Namespace}, nil
	}

	nsA, err := clientA.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing namespaces in %s: %w", opts.ContextA, err)
	}
	nsB, err := clientB.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing namespaces in %s: %w", opts.ContextB, err)
	}

	union := make(map[string]bool)
	for _, ns := range nsA.Items {
		union[ns.Name] = true
	}
	for _, ns := range nsB.Items {
		union[ns.Name] = true
	}

	var names []string
	for name := range union {
		if !opts.IncludeSystem && isSystemNamespace(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	return names, nil
}

// existsOnlyFinding flags an object present in only one context. Severity
// is Warning regardless of which side, since neither context is a
// baseline.
func existsOnlyFinding(resourceKind, namespace, name, ctxName string) types.Finding {
	return types.Finding{
		Severity:  types.SeverityWarning,
		Namespace: namespace,
		Resource:  resourceKind,
		Name:      name,
		Message:   fmt.Sprintf("exists in %s only", ctxName),
	}
}

func diffDeployments(ctx context.Context, clientA, clientB kubernetes.Interface, namespace string, opts Options) ([]types.Finding, error) {
	listA, err := clientA.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing deployments in %s/%s: %w", opts.ContextA, namespace, err)
	}
	listB, err := clientB.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing deployments in %s/%s: %w", opts.ContextB, namespace, err)
	}

	byNameA := make(map[string]appsv1.Deployment, len(listA.Items))
	for _, d := range listA.Items {
		byNameA[d.Name] = d
	}
	byNameB := make(map[string]appsv1.Deployment, len(listB.Items))
	for _, d := range listB.Items {
		byNameB[d.Name] = d
	}

	var findings []types.Finding

	for name, a := range byNameA {
		b, ok := byNameB[name]
		if !ok {
			findings = append(findings, existsOnlyFinding("Deployment", namespace, name, opts.ContextA))
			continue
		}
		findings = append(findings, diffDeployment(a, b, opts)...)
	}
	for name := range byNameB {
		if _, ok := byNameA[name]; !ok {
			findings = append(findings, existsOnlyFinding("Deployment", namespace, name, opts.ContextB))
		}
	}

	return findings, nil
}

func diffDeployment(a, b appsv1.Deployment, opts Options) []types.Finding {
	namespace, name := a.Namespace, a.Name
	const kind = "Deployment"

	var findings []types.Finding

	if f := diffScalar(kind, namespace, name, "replicas", replicaCount(a), replicaCount(b), opts.ContextA, opts.ContextB); f != nil {
		findings = append(findings, *f)
	}

	specA, specB := a.Spec.Template.Spec, b.Spec.Template.Spec

	containersA := containersByName(specA.Containers)
	containersB := containersByName(specB.Containers)

	for cname, ca := range containersA {
		cb, ok := containersB[cname]
		if !ok {
			findings = append(findings, types.Finding{
				Severity:  types.SeverityWarning,
				Namespace: namespace,
				Resource:  kind,
				Name:      name,
				Message:   fmt.Sprintf("container %s: only in %s", cname, opts.ContextA),
			})
			continue
		}
		findings = append(findings, diffContainer(kind, namespace, name, cname, ca, cb, opts.ContextA, opts.ContextB)...)
	}
	for cname := range containersB {
		if _, ok := containersA[cname]; !ok {
			findings = append(findings, types.Finding{
				Severity:  types.SeverityWarning,
				Namespace: namespace,
				Resource:  kind,
				Name:      name,
				Message:   fmt.Sprintf("container %s: only in %s", cname, opts.ContextB),
			})
		}
	}

	volOnlyA, volOnlyB, volChanged := collectionDiff(volumesByName(specA.Volumes), volumesByName(specB.Volumes), deepEqual[corev1.Volume])
	if f := collectionFinding(kind, namespace, name, "volumes", volOnlyA, volOnlyB, volChanged, opts.ContextA, opts.ContextB); f != nil {
		findings = append(findings, *f)
	}

	nsOnlyA, nsOnlyB, nsChanged := collectionDiff(specA.NodeSelector, specB.NodeSelector, func(x, y string) bool { return x == y })
	if f := collectionFinding(kind, namespace, name, "nodeSelector", nsOnlyA, nsOnlyB, nsChanged, opts.ContextA, opts.ContextB); f != nil {
		findings = append(findings, *f)
	}

	if f := diffStruct(kind, namespace, name, "affinity", specA.Affinity, specB.Affinity, opts.ContextA, opts.ContextB); f != nil {
		findings = append(findings, *f)
	}

	tolOnlyA, tolOnlyB, tolChanged := collectionDiff(tolerationsByKey(specA.Tolerations), tolerationsByKey(specB.Tolerations), deepEqual[corev1.Toleration])
	if f := collectionFinding(kind, namespace, name, "tolerations", tolOnlyA, tolOnlyB, tolChanged, opts.ContextA, opts.ContextB); f != nil {
		findings = append(findings, *f)
	}

	if f := diffScalar(kind, namespace, name, "serviceAccountName", specA.ServiceAccountName, specB.ServiceAccountName, opts.ContextA, opts.ContextB); f != nil {
		findings = append(findings, *f)
	}
	if f := diffScalar(kind, namespace, name, "restartPolicy", string(specA.RestartPolicy), string(specB.RestartPolicy), opts.ContextA, opts.ContextB); f != nil {
		findings = append(findings, *f)
	}

	return findings
}

// replicaCount applies the same default Kubernetes itself uses: an unset
// Replicas defaults to 1.
func replicaCount(d appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}

func diffContainer(kind, namespace, deploymentName, containerName string, a, b corev1.Container, ctxA, ctxB string) []types.Finding {
	var findings []types.Finding

	field := func(f string) string { return containerField(containerName, f) }

	if f := diffScalar(kind, namespace, deploymentName, field("image"), a.Image, b.Image, ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}

	if f := diffQuantity(kind, namespace, deploymentName, field("cpu request"), a.Resources.Requests[corev1.ResourceCPU], b.Resources.Requests[corev1.ResourceCPU], ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}
	if f := diffQuantity(kind, namespace, deploymentName, field("memory request"), a.Resources.Requests[corev1.ResourceMemory], b.Resources.Requests[corev1.ResourceMemory], ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}
	if f := diffQuantity(kind, namespace, deploymentName, field("cpu limit"), a.Resources.Limits[corev1.ResourceCPU], b.Resources.Limits[corev1.ResourceCPU], ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}
	if f := diffQuantity(kind, namespace, deploymentName, field("memory limit"), a.Resources.Limits[corev1.ResourceMemory], b.Resources.Limits[corev1.ResourceMemory], ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}

	envOnlyA, envOnlyB, envChanged := collectionDiff(envSourcesByName(a.Env, a.EnvFrom), envSourcesByName(b.Env, b.EnvFrom), func(x, y any) bool { return reflect.DeepEqual(x, y) })
	if f := collectionFinding(kind, namespace, deploymentName, field("env"), envOnlyA, envOnlyB, envChanged, ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}

	vmOnlyA, vmOnlyB, vmChanged := collectionDiff(volumeMountsByName(a.VolumeMounts), volumeMountsByName(b.VolumeMounts), deepEqual[corev1.VolumeMount])
	if f := collectionFinding(kind, namespace, deploymentName, field("volumeMounts"), vmOnlyA, vmOnlyB, vmChanged, ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}

	if f := diffStruct(kind, namespace, deploymentName, field("livenessProbe"), a.LivenessProbe, b.LivenessProbe, ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}
	if f := diffStruct(kind, namespace, deploymentName, field("readinessProbe"), a.ReadinessProbe, b.ReadinessProbe, ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}
	if f := diffStruct(kind, namespace, deploymentName, field("startupProbe"), a.StartupProbe, b.StartupProbe, ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}
	if f := diffStruct(kind, namespace, deploymentName, field("securityContext"), a.SecurityContext, b.SecurityContext, ctxA, ctxB); f != nil {
		findings = append(findings, *f)
	}

	return findings
}

func containerField(containerName, field string) string {
	return fmt.Sprintf("container %s %s", containerName, field)
}

func diffConfigMaps(ctx context.Context, clientA, clientB kubernetes.Interface, namespace string, opts Options) ([]types.Finding, error) {
	listA, err := clientA.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing configmaps in %s/%s: %w", opts.ContextA, namespace, err)
	}
	listB, err := clientB.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing configmaps in %s/%s: %w", opts.ContextB, namespace, err)
	}

	byNameA := make(map[string]corev1.ConfigMap, len(listA.Items))
	for _, cm := range listA.Items {
		byNameA[cm.Name] = cm
	}
	byNameB := make(map[string]corev1.ConfigMap, len(listB.Items))
	for _, cm := range listB.Items {
		byNameB[cm.Name] = cm
	}

	var findings []types.Finding

	for name, a := range byNameA {
		b, ok := byNameB[name]
		if !ok {
			findings = append(findings, existsOnlyFinding("ConfigMap", namespace, name, opts.ContextA))
			continue
		}
		if f := diffConfigMap(a, b, opts); f != nil {
			findings = append(findings, *f)
		}
	}
	for name := range byNameB {
		if _, ok := byNameA[name]; !ok {
			findings = append(findings, existsOnlyFinding("ConfigMap", namespace, name, opts.ContextB))
		}
	}

	return findings, nil
}

// diffConfigMap aggregates every key-level change (across both .data and
// .binaryData) into a single Finding per ConfigMap, naming only the keys
// involved — never their values (see DIFF_SPEC.md §6/§8.2).
func diffConfigMap(a, b corev1.ConfigMap, opts Options) *types.Finding {
	dataOnlyA, dataOnlyB, dataChanged := collectionDiff(a.Data, b.Data, func(x, y string) bool { return x == y })
	binOnlyA, binOnlyB, binChanged := collectionDiff(a.BinaryData, b.BinaryData, func(x, y []byte) bool { return bytes.Equal(x, y) })

	onlyA := mergeSorted(dataOnlyA, binOnlyA)
	onlyB := mergeSorted(dataOnlyB, binOnlyB)
	changed := mergeSorted(dataChanged, binChanged)

	return collectionFinding("ConfigMap", a.Namespace, a.Name, "data keys", onlyA, onlyB, changed, opts.ContextA, opts.ContextB)
}

func mergeSorted(a, b []string) []string {
	merged := append(append([]string{}, a...), b...)
	sort.Strings(merged)
	return merged
}

// diffScalar compares a single comparable value (a string, an int32, ...)
// on both sides and reports both values when they differ.
func diffScalar[T comparable](resourceKind, namespace, name, field string, a, b T, ctxA, ctxB string) *types.Finding {
	if a == b {
		return nil
	}
	return &types.Finding{
		Severity:  types.SeverityWarning,
		Namespace: namespace,
		Resource:  resourceKind,
		Name:      name,
		Message:   fmt.Sprintf("%s: %s=%v, %s=%v", field, ctxA, a, ctxB, b),
	}
}

// diffQuantity compares a resource.Quantity by value (never by ==, whose
// pointer-holding internals make it unsafe for equality) and reports both
// values when they differ.
func diffQuantity(resourceKind, namespace, name, field string, a, b resource.Quantity, ctxA, ctxB string) *types.Finding {
	if a.Cmp(b) == 0 {
		return nil
	}
	return &types.Finding{
		Severity:  types.SeverityWarning,
		Namespace: namespace,
		Resource:  resourceKind,
		Name:      name,
		Message:   fmt.Sprintf("%s: %s=%s, %s=%s", field, ctxA, a.String(), ctxB, b.String()),
	}
}

// diffStruct reports a coarse "differs" finding for a bounded nested-struct
// field (a probe, a security context, affinity) with no sub-field
// breakdown — see DIFF_SPEC.md §6 for why these stop at "differs".
func diffStruct(resourceKind, namespace, name, field string, a, b any, ctxA, ctxB string) *types.Finding {
	if reflect.DeepEqual(a, b) {
		return nil
	}
	return &types.Finding{
		Severity:  types.SeverityWarning,
		Namespace: namespace,
		Resource:  resourceKind,
		Name:      name,
		Message:   fmt.Sprintf("%s differs between %s and %s", field, ctxA, ctxB),
	}
}

// collectionDiff compares two name-keyed collections (env vars, volumes,
// ConfigMap keys, ...) and reports which names exist only in a, only in b,
// or exist in both but aren't equal per the given equality function.
func collectionDiff[V any](a, b map[string]V, equal func(V, V) bool) (onlyA, onlyB, changed []string) {
	for name, av := range a {
		bv, ok := b[name]
		if !ok {
			onlyA = append(onlyA, name)
			continue
		}
		if !equal(av, bv) {
			changed = append(changed, name)
		}
	}
	for name := range b {
		if _, ok := a[name]; !ok {
			onlyB = append(onlyB, name)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	sort.Strings(changed)
	return onlyA, onlyB, changed
}

// collectionFinding turns a collectionDiff result into a Finding naming
// only the affected keys — never their values (see DIFF_SPEC.md §6).
func collectionFinding(resourceKind, namespace, name, label string, onlyA, onlyB, changed []string, ctxA, ctxB string) *types.Finding {
	if len(onlyA) == 0 && len(onlyB) == 0 && len(changed) == 0 {
		return nil
	}
	return &types.Finding{
		Severity:  types.SeverityWarning,
		Namespace: namespace,
		Resource:  resourceKind,
		Name:      name,
		Message:   collectionMessage(label, ctxA, ctxB, onlyA, onlyB, changed),
	}
}

func collectionMessage(label, ctxA, ctxB string, onlyA, onlyB, changed []string) string {
	var parts []string
	if len(onlyA) > 0 {
		parts = append(parts, fmt.Sprintf("only in %s: %s", ctxA, strings.Join(onlyA, ", ")))
	}
	if len(onlyB) > 0 {
		parts = append(parts, fmt.Sprintf("only in %s: %s", ctxB, strings.Join(onlyB, ", ")))
	}
	if len(changed) > 0 {
		parts = append(parts, fmt.Sprintf("changed: %s", strings.Join(changed, ", ")))
	}
	return fmt.Sprintf("%s: %s", label, strings.Join(parts, "; "))
}

func deepEqual[T any](a, b T) bool {
	return reflect.DeepEqual(a, b)
}

func containersByName(containers []corev1.Container) map[string]corev1.Container {
	m := make(map[string]corev1.Container, len(containers))
	for _, c := range containers {
		m[c.Name] = c
	}
	return m
}

func volumesByName(volumes []corev1.Volume) map[string]corev1.Volume {
	m := make(map[string]corev1.Volume, len(volumes))
	for _, v := range volumes {
		m[v.Name] = v
	}
	return m
}

func volumeMountsByName(mounts []corev1.VolumeMount) map[string]corev1.VolumeMount {
	m := make(map[string]corev1.VolumeMount, len(mounts))
	for _, vm := range mounts {
		m[vm.Name] = vm
	}
	return m
}

func tolerationsByKey(tolerations []corev1.Toleration) map[string]corev1.Toleration {
	m := make(map[string]corev1.Toleration, len(tolerations))
	for _, t := range tolerations {
		m[t.Key] = t
	}
	return m
}

// envSourcesByName combines env and envFrom into one name-keyed collection
// so both contribute to a single "env" Finding per container (per
// DIFF_SPEC.md §8.1's "containers[].env (+ envFrom names)" row).
func envSourcesByName(env []corev1.EnvVar, envFrom []corev1.EnvFromSource) map[string]any {
	m := make(map[string]any, len(env)+len(envFrom))
	for _, e := range env {
		m[e.Name] = e
	}
	for _, ef := range envFrom {
		m[envFromKey(ef)] = ef
	}
	return m
}

func envFromKey(ef corev1.EnvFromSource) string {
	switch {
	case ef.ConfigMapRef != nil:
		return "envFrom:configMap:" + ef.ConfigMapRef.Name
	case ef.SecretRef != nil:
		return "envFrom:secret:" + ef.SecretRef.Name
	default:
		return "envFrom:unknown"
	}
}
