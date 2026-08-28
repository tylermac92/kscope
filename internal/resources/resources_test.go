package resources

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/tylermac92/kscope/pkg/types"
)

func nodeWithAllocatable(name, cpu, memory string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
			},
		},
	}
}

func podWithRequests(namespace, name, nodeName, cpu, memory string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(cpu),
							corev1.ResourceMemory: resource.MustParse(memory),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func podWithoutRequests(namespace, name, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "app"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func hasFinding(report types.Report, severity types.Severity, resourceKind, name string) bool {
	for _, f := range report.Findings {
		if f.Severity == severity && f.Resource == resourceKind && f.Name == name {
			return true
		}
	}
	return false
}

func TestAnalyze_HealthyNodeHeadroom(t *testing.T) {
	node := nodeWithAllocatable("node-1", "4", "8Gi")
	pod := podWithRequests("default", "web-1", "node-1", "1", "2Gi")

	client := fake.NewSimpleClientset(node, pod)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings for a node with plenty of headroom, got %+v", report.Findings)
	}
}

func TestAnalyze_NodeNearSaturationWarning(t *testing.T) {
	node := nodeWithAllocatable("node-1", "4", "8Gi")
	// 3400m / 4000m = 85% requested.
	pod := podWithRequests("default", "web-1", "node-1", "3400m", "2Gi")

	client := fake.NewSimpleClientset(node, pod)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "Node", "node-1") {
		t.Fatalf("expected Warning finding for node-1 near saturation, got %+v", report.Findings)
	}
	if hasFinding(report, types.SeverityCritical, "Node", "node-1") {
		t.Fatalf("did not expect a Critical finding for node-1, got %+v", report.Findings)
	}
}

func TestAnalyze_NodeOverAllocatableCritical(t *testing.T) {
	node := nodeWithAllocatable("node-1", "4", "8Gi")
	// 4500m / 4000m = 112.5% requested.
	pod := podWithRequests("default", "web-1", "node-1", "4500m", "2Gi")

	client := fake.NewSimpleClientset(node, pod)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityCritical, "Node", "node-1") {
		t.Fatalf("expected Critical finding for node-1 over allocatable, got %+v", report.Findings)
	}
}

func TestAnalyze_PodMissingRequestsFlaggedRegardlessOfHeadroom(t *testing.T) {
	node := nodeWithAllocatable("node-1", "4", "8Gi")
	// A pod with no requests contributes 0 to the sum, so the node itself
	// stays well under threshold — the pod must still be flagged on its own.
	pod := podWithoutRequests("default", "web-1", "node-1")

	client := fake.NewSimpleClientset(node, pod)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if hasFinding(report, types.SeverityWarning, "Node", "node-1") || hasFinding(report, types.SeverityCritical, "Node", "node-1") {
		t.Fatalf("did not expect a node headroom finding, got %+v", report.Findings)
	}
	if !hasFinding(report, types.SeverityWarning, "Pod", "web-1") {
		t.Fatalf("expected Warning finding flagging web-1 for missing requests, got %+v", report.Findings)
	}
}

func TestAnalyze_PodWithRequestsNotFlagged(t *testing.T) {
	node := nodeWithAllocatable("node-1", "4", "8Gi")
	pod := podWithRequests("default", "web-1", "node-1", "1", "2Gi")

	client := fake.NewSimpleClientset(node, pod)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if hasFinding(report, types.SeverityWarning, "Pod", "web-1") {
		t.Fatalf("did not expect web-1 to be flagged for missing requests, got %+v", report.Findings)
	}
}
