package rbac

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/tylermac92/kscope/pkg/types"
)

func clusterRole(name string, rules ...rbacv1.PolicyRule) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules:      rules,
	}
}

func role(namespace, name string, rules ...rbacv1.PolicyRule) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Rules:      rules,
	}
}

func clusterRoleBinding(name, roleRefName string, subjects ...rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     roleRefName,
		},
		Subjects: subjects,
	}
}

func roleBinding(namespace, name, roleRefKind, roleRefName string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     roleRefKind,
			Name:     roleRefName,
		},
		Subjects: subjects,
	}
}

func saSubject(namespace, name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: namespace, Name: name}
}

func userSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.UserKind, Name: name}
}

func groupSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.GroupKind, Name: name}
}

func hasFinding(report types.Report, severity types.Severity, resourceKind, name string) bool {
	for _, f := range report.Findings {
		if f.Severity == severity && f.Resource == resourceKind && f.Name == name {
			return true
		}
	}
	return false
}

func TestAnalyze_SystemOnlyBindingsExcludedByDefault(t *testing.T) {
	schedulerRole := clusterRole("system:kube-scheduler", rbacv1.PolicyRule{
		Verbs:     []string{"*"},
		APIGroups: []string{""},
		Resources: []string{"pods"},
	})
	schedulerBinding := clusterRoleBinding("system:kube-scheduler", "system:kube-scheduler", userSubject("system:kube-scheduler"))
	adminBinding := clusterRoleBinding("cluster-admin", "cluster-admin", groupSubject("system:masters"))

	client := fake.NewSimpleClientset(schedulerRole, schedulerBinding, adminBinding)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings for system-only bindings, got %+v", report.Findings)
	}
}

func TestAnalyze_ClusterAdminGrantToNonSystemSA(t *testing.T) {
	binding := clusterRoleBinding("risky-admin", "cluster-admin", saSubject("default", "app-sa"))

	client := fake.NewSimpleClientset(binding)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityCritical, "ClusterRoleBinding", "risky-admin") {
		t.Fatalf("expected Critical finding for risky-admin granting cluster-admin, got %+v", report.Findings)
	}
}

func TestAnalyze_RoleWildcardVerb(t *testing.T) {
	appRole := role("default", "app-role", rbacv1.PolicyRule{
		Verbs:     []string{"*"},
		APIGroups: []string{""},
		Resources: []string{"pods"},
	})
	binding := roleBinding("default", "app-binding", "Role", "app-role", saSubject("default", "app-sa"))

	client := fake.NewSimpleClientset(appRole, binding)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "Role", "app-role") {
		t.Fatalf("expected Warning finding for app-role's wildcard verb, got %+v", report.Findings)
	}
}

func TestAnalyze_RoleWildcardResource(t *testing.T) {
	appRole := role("default", "app-role-2", rbacv1.PolicyRule{
		Verbs:     []string{"get", "list"},
		APIGroups: []string{""},
		Resources: []string{"*"},
	})
	binding := roleBinding("default", "app-binding-2", "Role", "app-role-2", saSubject("default", "app-sa"))

	client := fake.NewSimpleClientset(appRole, binding)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "Role", "app-role-2") {
		t.Fatalf("expected Warning finding for app-role-2's wildcard resource, got %+v", report.Findings)
	}
}

func TestAnalyze_ClusterRoleBindingToNamespaceScopedSA(t *testing.T) {
	viewRole := clusterRole("view", rbacv1.PolicyRule{
		Verbs:     []string{"get", "list", "watch"},
		APIGroups: []string{""},
		Resources: []string{"pods"},
	})
	binding := clusterRoleBinding("view-binding", "view", saSubject("default", "viewer-sa"))

	client := fake.NewSimpleClientset(viewRole, binding)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "ClusterRoleBinding", "view-binding") {
		t.Fatalf("expected Warning finding for view-binding attaching a namespace-scoped SA at cluster scope, got %+v", report.Findings)
	}
}

func TestAnalyze_IncludeSystemSurfacesExcludedFindings(t *testing.T) {
	schedulerRole := clusterRole("system:kube-scheduler", rbacv1.PolicyRule{
		Verbs:     []string{"*"},
		APIGroups: []string{""},
		Resources: []string{"pods"},
	})
	schedulerBinding := clusterRoleBinding("system:kube-scheduler", "system:kube-scheduler", userSubject("system:kube-scheduler"))
	adminBinding := clusterRoleBinding("cluster-admin", "cluster-admin", groupSubject("system:masters"))

	client := fake.NewSimpleClientset(schedulerRole, schedulerBinding, adminBinding)

	defaultReport, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(defaultReport.Findings) != 0 {
		t.Fatalf("expected no findings by default, got %+v", defaultReport.Findings)
	}

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true, IncludeSystem: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if !hasFinding(report, types.SeverityWarning, "ClusterRole", "system:kube-scheduler") {
		t.Fatalf("expected --include-system to surface the wildcard system:kube-scheduler role, got %+v", report.Findings)
	}
	if !hasFinding(report, types.SeverityCritical, "ClusterRoleBinding", "cluster-admin") {
		t.Fatalf("expected --include-system to surface the cluster-admin grant to system:masters, got %+v", report.Findings)
	}
}
