// Package resources implements the kscope resources command's business
// logic: per-node request headroom against allocatable capacity, and pods
// with no resource requests set.
package resources

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tylermac92/kscope/pkg/types"
)

// Thresholds for node headroom findings, expressed as the percentage of
// allocatable capacity requested.
const (
	warningThresholdPercent  = 80.0
	criticalThresholdPercent = 100.0
)

// Options configures the scope of Analyze.
type Options struct {
	// Namespace restricts the missing-requests check to a single
	// namespace. Ignored when AllNamespaces is set. Node headroom always
	// accounts for every pod on the node regardless of this scope — see
	// Analyze.
	Namespace string
	// AllNamespaces checks pods for missing requests across every
	// namespace instead of just Namespace.
	AllNamespaces bool
}

func (o Options) namespace() string {
	if o.AllNamespaces {
		return metav1.NamespaceAll
	}
	return o.Namespace
}

// Analyze sums Running/Pending pod requests per node against
// Node.Status.Allocatable for CPU and memory, reporting headroom as a
// percentage, and separately flags pods with no requests set on any
// container.
func Analyze(ctx context.Context, client kubernetes.Interface, opts Options) (types.Report, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return types.Report{}, fmt.Errorf("listing nodes: %w", err)
	}

	// Headroom reflects the whole node's capacity, so it must account for
	// every pod scheduled onto it regardless of namespace scope —
	// otherwise headroom would look artificially high under a namespace
	// filter. The missing-requests check below is scoped separately.
	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return types.Report{}, fmt.Errorf("listing pods: %w", err)
	}

	podsByNode := make(map[string][]corev1.Pod)
	for _, pod := range pods.Items {
		if !schedulingRelevant(pod) || pod.Spec.NodeName == "" {
			continue
		}
		podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], pod)
	}

	var findings []types.Finding

	for _, node := range nodes.Items {
		findings = append(findings, headroomFindings(node, podsByNode[node.Name])...)
	}

	ns := opts.namespace()
	for _, pod := range pods.Items {
		if !schedulingRelevant(pod) {
			continue
		}
		if ns != metav1.NamespaceAll && pod.Namespace != ns {
			continue
		}
		if f := missingRequestsFinding(pod); f != nil {
			findings = append(findings, *f)
		}
	}

	return types.Report{Findings: findings}, nil
}

func schedulingRelevant(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending
}

func headroomFindings(node corev1.Node, pods []corev1.Pod) []types.Finding {
	var cpuRequested, memRequested resource.Quantity

	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				cpuRequested.Add(q)
			}
			if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				memRequested.Add(q)
			}
		}
	}

	var findings []types.Finding
	if f := headroomFinding(node.Name, "CPU", cpuRequested, node.Status.Allocatable[corev1.ResourceCPU]); f != nil {
		findings = append(findings, *f)
	}
	if f := headroomFinding(node.Name, "memory", memRequested, node.Status.Allocatable[corev1.ResourceMemory]); f != nil {
		findings = append(findings, *f)
	}
	return findings
}

// headroomFinding reports a Warning/Critical finding once requested crosses
// warningThresholdPercent/criticalThresholdPercent of allocatable, or nil
// when there's enough headroom (or allocatable isn't reported at all).
func headroomFinding(nodeName, label string, requested, allocatable resource.Quantity) *types.Finding {
	if allocatable.MilliValue() <= 0 {
		return nil
	}

	percentRequested := float64(requested.MilliValue()) / float64(allocatable.MilliValue()) * 100

	var severity types.Severity
	switch {
	case percentRequested >= criticalThresholdPercent:
		severity = types.SeverityCritical
	case percentRequested >= warningThresholdPercent:
		severity = types.SeverityWarning
	default:
		return nil
	}

	headroom := 100 - percentRequested
	if headroom < 0 {
		headroom = 0
	}

	return &types.Finding{
		Severity: severity,
		Resource: "Node",
		Name:     nodeName,
		Message: fmt.Sprintf("%s requested at %.0f%% of allocatable capacity (%.0f%% headroom remaining)",
			label, percentRequested, headroom),
	}
}

func missingRequestsFinding(pod corev1.Pod) *types.Finding {
	if len(pod.Spec.Containers) == 0 {
		return nil
	}

	for _, c := range pod.Spec.Containers {
		if len(c.Resources.Requests) > 0 {
			return nil
		}
	}

	return &types.Finding{
		Severity:  types.SeverityWarning,
		Namespace: pod.Namespace,
		Resource:  "Pod",
		Name:      pod.Name,
		Message:   "no resource requests set on any container; invisible to headroom accounting",
	}
}
