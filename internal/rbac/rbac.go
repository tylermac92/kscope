// Package rbac implements the kscope rbac audit command's business logic:
// walking ClusterRoleBindings/RoleBindings to their referenced (Cluster)Role
// and flagging cluster-admin grants, wildcard rules, and namespace-scoped
// service accounts bound at cluster scope.
package rbac

import (
	"context"
	"fmt"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tylermac92/kscope/pkg/types"
)

// clusterAdminRoleName is the built-in ClusterRole every cluster ships with.
// Referencing it (from either binding kind) grants full cluster access.
const clusterAdminRoleName = "cluster-admin"

// systemNamespaces holds the well-known namespaces whose service accounts
// are treated the same as "system:"-prefixed users/groups/roles: expected
// built-in noise, excluded from the default report.
var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

// Options configures the scope of Analyze.
type Options struct {
	// Namespace restricts RoleBinding/Role checks to a single namespace.
	// Ignored when AllNamespaces is set. ClusterRoleBindings/ClusterRoles
	// are always considered, since they're inherently cluster-scoped.
	Namespace string
	// AllNamespaces checks RoleBindings/Roles across every namespace
	// instead of just Namespace.
	AllNamespaces bool
	// IncludeSystem shows system:-prefixed built-in roles/subjects and
	// system-namespace service accounts, which are excluded by default as
	// expected noise.
	IncludeSystem bool
}

func (o Options) namespace() string {
	if o.AllNamespaces {
		return metav1.NamespaceAll
	}
	return o.Namespace
}

// Analyze walks ClusterRoleBindings and RoleBindings, resolves each to its
// referenced ClusterRole or Role, and flags: bindings granting
// cluster-admin, roles containing a "*" in verbs or resources, and
// ClusterRoleBindings attaching a namespace-scoped-looking service account
// at cluster scope. system:-prefixed roles/subjects and service accounts in
// well-known system namespaces are excluded unless opts.IncludeSystem.
func Analyze(ctx context.Context, client kubernetes.Interface, opts Options) (types.Report, error) {
	clusterRoles, err := client.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
	if err != nil {
		return types.Report{}, fmt.Errorf("listing clusterroles: %w", err)
	}
	clusterRolesByName := make(map[string]rbacv1.ClusterRole, len(clusterRoles.Items))
	for _, cr := range clusterRoles.Items {
		clusterRolesByName[cr.Name] = cr
	}

	ns := opts.namespace()

	roles, err := client.RbacV1().Roles(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return types.Report{}, fmt.Errorf("listing roles: %w", err)
	}
	rolesByKey := make(map[string]rbacv1.Role, len(roles.Items))
	for _, r := range roles.Items {
		rolesByKey[r.Namespace+"/"+r.Name] = r
	}

	clusterRoleBindings, err := client.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return types.Report{}, fmt.Errorf("listing clusterrolebindings: %w", err)
	}

	roleBindings, err := client.RbacV1().RoleBindings(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return types.Report{}, fmt.Errorf("listing rolebindings: %w", err)
	}

	var findings []types.Finding
	seenRoles := make(map[string]bool)

	for _, crb := range clusterRoleBindings.Items {
		if f := clusterAdminFinding("ClusterRoleBinding", "", crb.Name, crb.RoleRef, crb.Subjects, opts.IncludeSystem); f != nil {
			findings = append(findings, *f)
		}

		if kind, roleNS, roleName, rules, ok := resolveRole(crb.RoleRef, "", clusterRolesByName, rolesByKey); ok {
			key := roleKey(kind, roleNS, roleName)
			if !seenRoles[key] {
				seenRoles[key] = true
				if f := wildcardFinding(kind, roleNS, roleName, rules, opts.IncludeSystem); f != nil {
					findings = append(findings, *f)
				}
			}
		}

		findings = append(findings, namespaceScopedSAFindings(crb, opts.IncludeSystem)...)
	}

	for _, rb := range roleBindings.Items {
		if f := clusterAdminFinding("RoleBinding", rb.Namespace, rb.Name, rb.RoleRef, rb.Subjects, opts.IncludeSystem); f != nil {
			findings = append(findings, *f)
		}

		if kind, roleNS, roleName, rules, ok := resolveRole(rb.RoleRef, rb.Namespace, clusterRolesByName, rolesByKey); ok {
			key := roleKey(kind, roleNS, roleName)
			if !seenRoles[key] {
				seenRoles[key] = true
				if f := wildcardFinding(kind, roleNS, roleName, rules, opts.IncludeSystem); f != nil {
					findings = append(findings, *f)
				}
			}
		}
	}

	return types.Report{Findings: findings}, nil
}

// resolveRole looks up the (Cluster)Role a binding's RoleRef points at.
// bindingNamespace scopes a "Role" kind lookup (Roles live in one
// namespace); it's ignored for "ClusterRole". ok is false when the
// reference doesn't resolve to a role kscope has seen.
func resolveRole(roleRef rbacv1.RoleRef, bindingNamespace string, clusterRolesByName map[string]rbacv1.ClusterRole, rolesByKey map[string]rbacv1.Role) (kind, namespace, name string, rules []rbacv1.PolicyRule, ok bool) {
	switch roleRef.Kind {
	case "ClusterRole":
		role, found := clusterRolesByName[roleRef.Name]
		if !found {
			return "", "", "", nil, false
		}
		return "ClusterRole", "", role.Name, role.Rules, true
	case "Role":
		role, found := rolesByKey[bindingNamespace+"/"+roleRef.Name]
		if !found {
			return "", "", "", nil, false
		}
		return "Role", role.Namespace, role.Name, role.Rules, true
	default:
		return "", "", "", nil, false
	}
}

func roleKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

// clusterAdminFinding flags a binding whose RoleRef is the built-in
// cluster-admin role, naming every subject it grants that to (excluding
// system:-prefixed ones unless includeSystem). Returns nil if the binding
// doesn't reference cluster-admin, or every subject was filtered out.
func clusterAdminFinding(bindingKind, bindingNamespace, bindingName string, roleRef rbacv1.RoleRef, subjects []rbacv1.Subject, includeSystem bool) *types.Finding {
	if roleRef.Name != clusterAdminRoleName {
		return nil
	}

	ids := subjectIdentities(subjects, includeSystem)
	if len(ids) == 0 {
		return nil
	}

	return &types.Finding{
		Severity:  types.SeverityCritical,
		Namespace: bindingNamespace,
		Resource:  bindingKind,
		Name:      bindingName,
		Message:   fmt.Sprintf("grants cluster-admin to %s", strings.Join(ids, ", ")),
	}
}

// wildcardFinding flags a role whose rules contain a "*" verb or resource.
// Returns nil for a system:-prefixed role name (unless includeSystem) or a
// role with no wildcard rules.
func wildcardFinding(roleKind, roleNamespace, roleName string, rules []rbacv1.PolicyRule, includeSystem bool) *types.Finding {
	if !includeSystem && isSystemName(roleName) {
		return nil
	}

	for _, rule := range rules {
		if containsWildcard(rule.Verbs) || containsWildcard(rule.Resources) {
			return &types.Finding{
				Severity:  types.SeverityWarning,
				Namespace: roleNamespace,
				Resource:  roleKind,
				Name:      roleName,
				Message:   "rule grants a wildcard (*) verb or resource",
			}
		}
	}

	return nil
}

func containsWildcard(items []string) bool {
	for _, item := range items {
		if item == "*" {
			return true
		}
	}
	return false
}

// namespaceScopedSAFindings flags each service-account subject on a
// ClusterRoleBinding that doesn't look like a built-in system account,
// since attaching a namespace-scoped SA at cluster scope widens its reach
// well beyond the namespace it otherwise lives in.
func namespaceScopedSAFindings(crb rbacv1.ClusterRoleBinding, includeSystem bool) []types.Finding {
	var findings []types.Finding

	for _, s := range crb.Subjects {
		if s.Kind != rbacv1.ServiceAccountKind {
			continue
		}
		if !includeSystem && isSystemSubject(s) {
			continue
		}

		findings = append(findings, types.Finding{
			Severity:  types.SeverityWarning,
			Namespace: s.Namespace,
			Resource:  "ClusterRoleBinding",
			Name:      crb.Name,
			Message:   fmt.Sprintf("binds namespace-scoped service account %s/%s at cluster scope", s.Namespace, s.Name),
		})
	}

	return findings
}

func subjectIdentities(subjects []rbacv1.Subject, includeSystem bool) []string {
	var ids []string
	for _, s := range subjects {
		if !includeSystem && isSystemSubject(s) {
			continue
		}
		ids = append(ids, subjectIdentity(s))
	}
	return ids
}

func subjectIdentity(s rbacv1.Subject) string {
	if s.Kind == rbacv1.ServiceAccountKind {
		return fmt.Sprintf("ServiceAccount %s/%s", s.Namespace, s.Name)
	}
	return fmt.Sprintf("%s %s", s.Kind, s.Name)
}

func isSystemSubject(s rbacv1.Subject) bool {
	if s.Kind == rbacv1.ServiceAccountKind {
		return systemNamespaces[s.Namespace]
	}
	return isSystemName(s.Name)
}

func isSystemName(name string) bool {
	return strings.HasPrefix(name, "system:")
}
