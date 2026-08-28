package diff

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/tylermac92/kscope/pkg/types"
)

func deployment(namespace, name string, replicas int32, containers ...corev1.Container) *appsv1.Deployment {
	r := replicas
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &r,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: containers,
				},
			},
		},
	}
}

func container(name, image string) corev1.Container {
	return corev1.Container{Name: name, Image: image}
}

func configMap(namespace, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       data,
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

func findingsFor(report types.Report, resourceKind, name string) []types.Finding {
	var out []types.Finding
	for _, f := range report.Findings {
		if f.Resource == resourceKind && f.Name == name {
			out = append(out, f)
		}
	}
	return out
}

func TestAnalyze_IdenticalObjects_NoFindings(t *testing.T) {
	dep := deployment("default", "web", 3, container("app", "nginx:1.25"))
	cm := configMap("default", "app-config", map[string]string{"foo": "bar"})

	clientA := fake.NewSimpleClientset(dep.DeepCopy(), cm.DeepCopy())
	clientB := fake.NewSimpleClientset(dep.DeepCopy(), cm.DeepCopy())

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings for identical objects, got %+v", report.Findings)
	}
}

func TestAnalyze_DeploymentExistsOnOneSideOnly(t *testing.T) {
	depA := deployment("default", "only-a", 1, container("app", "nginx:1.25"))
	depB := deployment("default", "only-b", 1, container("app", "nginx:1.25"))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "Deployment", "only-a") {
		t.Fatalf("expected finding for only-a, got %+v", report.Findings)
	}
	if !hasFinding(report, types.SeverityWarning, "Deployment", "only-b") {
		t.Fatalf("expected finding for only-b, got %+v", report.Findings)
	}
	for _, f := range findingsFor(report, "Deployment", "only-a") {
		if f.Message != "exists in ctx-a only" {
			t.Fatalf("unexpected message for only-a: %q", f.Message)
		}
	}
	for _, f := range findingsFor(report, "Deployment", "only-b") {
		if f.Message != "exists in ctx-b only" {
			t.Fatalf("unexpected message for only-b: %q", f.Message)
		}
	}
}

func TestAnalyze_ConfigMapExistsOnOneSideOnly(t *testing.T) {
	cmA := configMap("default", "only-a", map[string]string{"k": "v"})
	cmB := configMap("default", "only-b", map[string]string{"k": "v"})

	clientA := fake.NewSimpleClientset(cmA)
	clientB := fake.NewSimpleClientset(cmB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "ConfigMap", "only-a") {
		t.Fatalf("expected finding for only-a, got %+v", report.Findings)
	}
	if !hasFinding(report, types.SeverityWarning, "ConfigMap", "only-b") {
		t.Fatalf("expected finding for only-b, got %+v", report.Findings)
	}
}

func TestAnalyze_ReplicaCountDiffers(t *testing.T) {
	depA := deployment("default", "web", 3, container("app", "nginx:1.25"))
	depB := deployment("default", "web", 5, container("app", "nginx:1.25"))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	findings := findingsFor(report, "Deployment", "web")
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding for web, got %d: %+v", len(findings), findings)
	}
	if findings[0].Message != "replicas: ctx-a=3, ctx-b=5" {
		t.Fatalf("unexpected message: %q", findings[0].Message)
	}
}

func TestAnalyze_ContainerImageDiffers(t *testing.T) {
	depA := deployment("default", "web", 1, container("app", "nginx:1.25"))
	depB := deployment("default", "web", 1, container("app", "nginx:1.26"))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	findings := findingsFor(report, "Deployment", "web")
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding for web, got %d: %+v", len(findings), findings)
	}
	if findings[0].Message != "container app image: ctx-a=nginx:1.25, ctx-b=nginx:1.26" {
		t.Fatalf("unexpected message: %q", findings[0].Message)
	}
}

func TestAnalyze_ResourceRequestDiffersLimitDoesNot(t *testing.T) {
	containerWith := func(cpuRequest, cpuLimit string) corev1.Container {
		return corev1.Container{
			Name:  "app",
			Image: "nginx:1.25",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpuRequest)},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpuLimit)},
			},
		}
	}

	depA := deployment("default", "web", 1, containerWith("100m", "500m"))
	depB := deployment("default", "web", 1, containerWith("200m", "500m"))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	findings := findingsFor(report, "Deployment", "web")
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding (cpu request only), got %d: %+v", len(findings), findings)
	}
	if findings[0].Message != "container app cpu request: ctx-a=100m, ctx-b=200m" {
		t.Fatalf("unexpected message: %q", findings[0].Message)
	}
}

func TestAnalyze_ContainerAddedOnOneSide(t *testing.T) {
	depA := deployment("default", "web", 1, container("app", "nginx:1.25"))
	depB := deployment("default", "web", 1, container("app", "nginx:1.25"), container("sidecar", "envoy:1.0"))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	findings := findingsFor(report, "Deployment", "web")
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding (sidecar only in ctx-b), got %d: %+v", len(findings), findings)
	}
	if findings[0].Message != "container sidecar: only in ctx-b" {
		t.Fatalf("unexpected message: %q", findings[0].Message)
	}
}

func TestAnalyze_EnvVarChanges(t *testing.T) {
	containerWithEnv := func(env ...corev1.EnvVar) corev1.Container {
		return corev1.Container{Name: "app", Image: "nginx:1.25", Env: env}
	}

	depA := deployment("default", "web", 1, containerWithEnv(
		corev1.EnvVar{Name: "FOO", Value: "1"},
		corev1.EnvVar{Name: "REMOVED", Value: "x"},
	))
	depB := deployment("default", "web", 1, containerWithEnv(
		corev1.EnvVar{Name: "FOO", Value: "2"},
		corev1.EnvVar{Name: "ADDED", Value: "y"},
	))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	findings := findingsFor(report, "Deployment", "web")
	if len(findings) != 1 {
		t.Fatalf("expected exactly one env finding, got %d: %+v", len(findings), findings)
	}
	msg := findings[0].Message
	for _, want := range []string{"FOO", "ADDED", "REMOVED"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected message to name %q, got %q", want, msg)
		}
	}
}

func TestAnalyze_ProbeDiffers(t *testing.T) {
	withProbe := func(periodSeconds int32) corev1.Container {
		return corev1.Container{
			Name:          "app",
			Image:         "nginx:1.25",
			LivenessProbe: &corev1.Probe{PeriodSeconds: periodSeconds},
		}
	}

	depA := deployment("default", "web", 1, withProbe(10))
	depB := deployment("default", "web", 1, withProbe(30))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	findings := findingsFor(report, "Deployment", "web")
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding for the differing probe, got %d: %+v", len(findings), findings)
	}
	if findings[0].Message != "container app livenessProbe differs between ctx-a and ctx-b" {
		t.Fatalf("unexpected message: %q", findings[0].Message)
	}
}

func TestAnalyze_ConfigMapMultipleKeyChanges(t *testing.T) {
	cmA := configMap("default", "app-config", map[string]string{
		"unchanged": "same",
		"changed":   "old-value",
		"removed":   "gone-in-b",
	})
	cmB := configMap("default", "app-config", map[string]string{
		"unchanged": "same",
		"changed":   "new-value",
		"added":     "new-in-b",
	})

	clientA := fake.NewSimpleClientset(cmA)
	clientB := fake.NewSimpleClientset(cmB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	findings := findingsFor(report, "ConfigMap", "app-config")
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding aggregating all key changes, got %d: %+v", len(findings), findings)
	}
	msg := findings[0].Message
	for _, want := range []string{"changed", "removed", "added"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected message to mention key %q, got %q", want, msg)
		}
	}
	if strings.Contains(msg, "old-value") || strings.Contains(msg, "new-value") {
		t.Fatalf("expected no key values in the message, got %q", msg)
	}
}

func TestAnalyze_LabelAndAnnotationOnlyDifference_NoFindings(t *testing.T) {
	depA := deployment("default", "web", 1, container("app", "nginx:1.25"))
	depA.Labels = map[string]string{"team": "a"}
	depA.Annotations = map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "..."}

	depB := deployment("default", "web", 1, container("app", "nginx:1.25"))
	depB.Labels = map[string]string{"team": "b"}
	depB.Annotations = map[string]string{"deployment.kubernetes.io/revision": "3"}

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "default",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings for a label/annotation-only difference, got %+v", report.Findings)
	}
}

func TestAnalyze_AllNamespaces_UnionAndSystemExclusion(t *testing.T) {
	onlyInA := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "only-in-a"}}
	shared := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shared"}}
	kubeSystem := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}

	depOnlyInA := deployment("only-in-a", "orphan", 1, container("app", "nginx:1.25"))
	depSystemA := deployment("kube-system", "coredns", 1, container("coredns", "coredns:1.10"))
	depSystemB := deployment("kube-system", "coredns", 1, container("coredns", "coredns:1.11"))

	clientA := fake.NewSimpleClientset(onlyInA, shared, kubeSystem, depOnlyInA, depSystemA)
	clientB := fake.NewSimpleClientset(shared, kubeSystem, depSystemB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		AllNamespaces: true,
		ContextA:      "ctx-a",
		ContextB:      "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "Deployment", "orphan") {
		t.Fatalf("expected orphan (namespace only in ctx-a) to be flagged, got %+v", report.Findings)
	}
	if hasFinding(report, types.SeverityWarning, "Deployment", "coredns") {
		t.Fatalf("expected kube-system's coredns diff to be excluded by default, got %+v", report.Findings)
	}

	reportWithSystem, err := Analyze(context.Background(), clientA, clientB, Options{
		AllNamespaces: true,
		IncludeSystem: true,
		ContextA:      "ctx-a",
		ContextB:      "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if !hasFinding(reportWithSystem, types.SeverityWarning, "Deployment", "coredns") {
		t.Fatalf("expected --include-system to surface the kube-system coredns diff, got %+v", reportWithSystem.Findings)
	}
}

func TestAnalyze_ExplicitSystemNamespaceIsHonored(t *testing.T) {
	depA := deployment("kube-system", "coredns", 1, container("coredns", "coredns:1.10"))
	depB := deployment("kube-system", "coredns", 1, container("coredns", "coredns:1.11"))

	clientA := fake.NewSimpleClientset(depA)
	clientB := fake.NewSimpleClientset(depB)

	report, err := Analyze(context.Background(), clientA, clientB, Options{
		Namespace: "kube-system",
		ContextA:  "ctx-a",
		ContextB:  "ctx-b",
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "Deployment", "coredns") {
		t.Fatalf("expected an explicit --namespace kube-system to still be diffed, got %+v", report.Findings)
	}
}
