package health

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/tylermac92/kscope/pkg/types"
)

func healthyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse},
			},
		},
	}
}

func healthyPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 0,
					State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}
}

func hasFinding(report types.Report, severity types.Severity, resource, name string) bool {
	for _, f := range report.Findings {
		if f.Severity == severity && f.Resource == resource && f.Name == name {
			return true
		}
	}
	return false
}

func TestAnalyze_HealthyCluster(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "data"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}

	client := fake.NewSimpleClientset(healthyNode("node-1"), healthyPod("default", "web-1"), pvc)

	report, err := Analyze(context.Background(), client, Options{Namespace: "default"})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings for a healthy cluster, got %+v", report.Findings)
	}
}

func TestAnalyze_NotReadyNode(t *testing.T) {
	node := healthyNode("node-1")
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	node.Status.Conditions[0].Reason = "KubeletNotReady"

	client := fake.NewSimpleClientset(node)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityCritical, "Node", "node-1") {
		t.Fatalf("expected Critical finding for node-1, got %+v", report.Findings)
	}
}

func TestAnalyze_CrashLoopingPod(t *testing.T) {
	pod := healthyPod("default", "web-1")
	pod.Status.ContainerStatuses[0].RestartCount = 5
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
	}
	pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"},
	}

	client := fake.NewSimpleClientset(healthyNode("node-1"), pod)

	report, err := Analyze(context.Background(), client, Options{Namespace: "default"})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityCritical, "Pod", "web-1") {
		t.Fatalf("expected Critical finding for web-1, got %+v", report.Findings)
	}
}

func TestAnalyze_PendingPodUnschedulable(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web-2"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web-2.failedscheduling"},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: "default",
			Name:      "web-2",
		},
		Reason:  "FailedScheduling",
		Message: "0/3 nodes are available: insufficient cpu",
	}

	client := fake.NewSimpleClientset(healthyNode("node-1"), pod, event)

	report, err := Analyze(context.Background(), client, Options{Namespace: "default"})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityCritical, "Pod", "web-2") {
		t.Fatalf("expected Critical finding for web-2, got %+v", report.Findings)
	}
}

func TestAnalyze_PendingPVC(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "data"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}

	client := fake.NewSimpleClientset(healthyNode("node-1"), pvc)

	report, err := Analyze(context.Background(), client, Options{Namespace: "default"})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "PersistentVolumeClaim", "data") {
		t.Fatalf("expected Warning finding for PVC data, got %+v", report.Findings)
	}
}
