# `kscope diff` — Implementation Spec

This document specifies `kscope diff` in enough detail to implement `internal/diff/diff.go`
and `internal/diff/diff_test.go` directly from it, following the Analyzer pattern and
package layout in `CLAUDE.md`. It resolves every ambiguity in the original one-paragraph
command spec through a design interview; those decisions are recorded here as the source
of truth.

## 1. What it does

`kscope diff` compares two live clusters — reached via two kubeconfig contexts — and
reports drift in Deployments and ConfigMaps: objects that exist on only one side, and
specific fields that differ between objects that exist on both. It is **not** directional:
neither context is a "baseline"; both are peers, labeled by their context names in every
finding.

## 2. Deviation from the standard Analyzer signature

Every other kscope command follows:

```go
func Analyze(ctx context.Context, client kubernetes.Interface, opts Options) (types.Report, error)
```

`diff` inherently compares two clusters, so it takes two clientsets:

```go
// Analyze compares Deployments and ConfigMaps between two live clusters
// (clientA, clientB — typically two kubeconfig contexts) and returns a
// Report of drift: objects unique to one side, and specific fields that
// differ between matching objects on both sides. Unlike every other kscope
// analyzer, Analyze takes two clientsets — a diff is inherently a
// two-cluster operation, and there is no single "the" cluster to default
// to. Neither side is a baseline: findings are labeled by context name via
// Options.ContextA/ContextB, not "old"/"new" or "added"/"removed" against
// a canonical order.
func Analyze(ctx context.Context, clientA, clientB kubernetes.Interface, opts Options) (types.Report, error)
```

The `internal/cli/diff.go` command builds two clientsets from `--context-a`/`--context-b`
(sharing the global `--kubeconfig`) and passes both to `Analyze`. This is the one command
where `RunE` legitimately constructs two clients instead of one — still no business logic,
just twice the plumbing.

## 3. Options

```go
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
```

Note `Options` has no single `namespace()` helper the way `health`/`resources`/`rbac` do:
those commands resolve `AllNamespaces` to `metav1.NamespaceAll` for one `List` call against
one cluster. `diff` instead resolves `AllNamespaces` to a *set of namespace names*, computed
from **both** clusters — see §4. Neither cluster's namespace list alone is authoritative.

## 4. Namespace resolution

```go
func resolveNamespaces(ctx context.Context, clientA, clientB kubernetes.Interface, opts Options) ([]string, error) {
	if !opts.AllNamespaces {
		return []string{opts.Namespace}, nil
	}

	nsA, err := clientA.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	// ... wrap err
	nsB, err := clientB.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	// ... wrap err

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
```

`isSystemNamespace` matches `kube-system`, `kube-public`, `kube-node-lease` — the same set
`internal/rbac` uses for system service accounts. Reuse or duplicate the check; do not
import across `internal/*` analyzer packages (they stay independent per `CLAUDE.md`).

**Default behavior** (no `-A`): diffs exactly `opts.Namespace` (global default `"default"`),
same as every other command. **Under `-A`**: diffs the union of namespaces that exist in
*either* cluster, system namespaces excluded unless `--include-system`. A namespace that
exists in only one cluster needs no special case: listing Deployments/ConfigMaps in a
namespace that doesn't exist on one side just returns an empty list on that side (client-go
does not error), so everything on the other side naturally comes out as "exists on one side
only" per §7.

## 5. Matching

Within each namespace being diffed, Deployments are matched to Deployments and ConfigMaps
to ConfigMaps by `name` (same Kind, same namespace, same name = same object across
clusters). No other resource kinds are compared (per `CLAUDE.md`'s non-goals — Deployments
and ConfigMaps only).

## 6. Value presentation principle

One rule governs every field comparison, stated once so the field tables in §8 don't need
to repeat it:

- **Scalar field** (a single string/int/quantity — replica count, container image,
  a single resource quantity, `serviceAccountName`, `restartPolicy`): show both values,
  `old → new` is not applicable since there's no direction — show
  `<field>: <ContextA>=<valueA>, <ContextB>=<valueB>`.
- **Open-ended collection field** (env vars, volumes, volume mounts, `nodeSelector`,
  toleration keys, ConfigMap `data`/`binaryData` keys): report only which **names** were
  added, removed, or changed — never their content. This keeps output short for
  large ConfigMaps/env lists and avoids ever printing a value that might be sensitive-
  adjacent, even though these are ConfigMaps and env vars, not Secrets.
- **Bounded nested-struct field** (`livenessProbe`/`readinessProbe`/`startupProbe`,
  container `securityContext`, pod-level `securityContext`, `affinity`): compared via
  `reflect.DeepEqual` (or an equivalent structural equality) as a single unit. If they
  differ, report a coarse "`<field>` differs" finding with no further breakdown. These are
  deliberately *not* diffed field-by-field — that would mean re-deriving the "curated
  broader list" recursively into every nested struct kscope doesn't have a specific reason
  to inspect. This is a firm boundary, not a placeholder for a future deeper diff.

This is a curated-field diff, not a generic structural diff of the whole object (that
option was explicitly rejected — see design log in §12). Fields not listed in §8 are never
compared, on either object kind.

## 7. Objects present on only one side

A Deployment or ConfigMap that exists in one context but not the other:

- **Severity:** `Warning` (both directions — a resource missing from context A is treated
  the same as one missing from context B; no asymmetry).
- **Message:** `"exists in <ContextA> only"` or `"exists in <ContextB> only"` — not
  "added"/"removed", since there's no baseline direction.
- No separate "namespace missing entirely" finding is emitted — if a namespace exists on
  only one side, every Deployment/ConfigMap in it naturally comes out as "exists in
  <ctx> only" per-object, which is sufficient signal without inventing a new finding shape
  for namespaces.

## 8. Field comparison tables

### 8.1 Deployment

| Field | Kind | Notes |
|---|---|---|
| `spec.replicas` | scalar | |
| Container **added/removed** | collection (by container `name`) | A container present in one side's pod spec but not the other's own finding, distinct from field-level diffs below. |
| `containers[].image` | scalar, per matched container | Matched containers = same `name` on both sides. |
| `containers[].resources.requests` (cpu, memory) | scalar, per matched container, per resource type | Report cpu and memory separately if both differ. |
| `containers[].resources.limits` (cpu, memory) | scalar, per matched container, per resource type | |
| `containers[].env` (+ `envFrom` names) | collection (by env var `name`), per matched container | Names only, no values (see §6). |
| `containers[].volumeMounts` | collection (by mount `name`), per matched container | Names only. |
| `containers[].livenessProbe` / `readinessProbe` / `startupProbe` | bounded struct, per matched container | Coarse "differs", per probe type. |
| `containers[].securityContext` | bounded struct, per matched container | Coarse "differs". |
| `spec.template.spec.volumes` | collection (by volume `name`) | Pod-template level, not per-container. Names only. |
| `spec.template.spec.nodeSelector` | collection (map keys) | Key names only, not values (see §6). |
| `spec.template.spec.affinity` | bounded struct | Coarse "differs". |
| `spec.template.spec.tolerations` | collection (by toleration `key`) | Key names only. |
| `spec.template.spec.serviceAccountName` | scalar | |
| `spec.template.spec.restartPolicy` | scalar | |
| `metadata.labels`, `metadata.annotations` | **excluded** | Never diffed — see §9. |

Everything else on `Deployment`/`PodSpec` (e.g. `strategy`, `lifecycle` hooks, `ports`,
`terminationGracePeriodSeconds`, image pull policy/secrets, DNS config, topology spread
constraints) is **out of scope** — not compared, not planned for a follow-up pass unless
explicitly requested.

### 8.2 ConfigMap

| Field | Kind | Notes |
|---|---|---|
| `data` | collection (map keys) | Key names only: added / removed / changed. No values shown, ever (see §6). |
| `binaryData` | collection (map keys) | Same treatment as `data` — key presence and whether the bytes differ, never content. |
| `metadata.labels`, `metadata.annotations` | **excluded** | Never diffed — see §9. |

A ConfigMap with any key-level drift produces **one** Finding per ConfigMap that lists all
key changes together (e.g. `"data keys changed: [foo, bar]; added: [baz]; removed: [qux]"`),
not one Finding per key — ConfigMap keys are an open-ended set and per-key findings would
be spammy for a config with many touched keys. This is the one place kscope aggregates
multiple sub-changes into a single Finding; every Deployment field in §8.1 gets its own
Finding (see §11 for why).

## 9. Labels and annotations are never diffed

Neither `Deployment` nor `ConfigMap` metadata (`labels`, `annotations`) is compared, on
either object, full stop. Rationale: annotations are frequently controller/webhook-managed
(`kubectl.kubernetes.io/last-applied-configuration`, `deployment.kubernetes.io/revision`,
timestamps injected by admission webhooks, etc.) and would dominate the output with noise
that has nothing to do with intentional configuration drift. Labels were considered for
inclusion (they're usually intentional) but excluded too, for simplicity and consistency —
one rule, no denylist to maintain.

## 10. System namespace exclusion

Same posture as `internal/rbac`'s `system:`-prefix exclusion: `kube-system`, `kube-public`,
and `kube-node-lease` are excluded from the namespace union computed under `-A` (§4) unless
`--include-system` is passed. An explicit `--namespace kube-system` is always honored — the
exclusion only prunes auto-discovery, it never blocks an explicit request.

## 11. Severity summary

| Finding | Severity |
|---|---|
| Deployment/ConfigMap exists on one side only | `Warning` |
| Any changed field (any row in §8, including container-added/removed) | `Warning` |

Uniformly `Warning` — no field or direction is elevated to `Critical`. This keeps the rule
simple and matches the decision that neither context is a "more correct" baseline whose
divergence is worse in one direction than the other.

**Granularity:** every differing field in §8.1 produces its own Finding (one Deployment
with a changed image *and* a changed replica count produces two Findings, each naming the
specific field — this is the literal reading of `CLAUDE.md`'s "Changed entries should show
the specific field that differs"). ConfigMap key-level changes are the sole exception,
aggregated per §8.2.

## 12. Design log (source of the above decisions)

Recorded from the pre-implementation interview, in case the rationale needs revisiting:

- **Symmetric, not directional.** Contexts are peers (`ContextA`/`ContextB`), not
  baseline/current. No "added"/"removed" language — "exists in `<ctx>` only" instead.
- **Namespace scope follows the existing global-flag convention**, not an all-namespaces-
  by-default override: `--namespace`/`-A` behave exactly like `health`/`events`/
  `resources`/`rbac audit`.
- **System namespaces excluded by default** under `-A`, mirroring `rbac audit`'s
  `system:`-prefix precedent; `--include-system` opts back in.
- **Exists-on-one-side-only is `Warning` in both directions** — no asymmetry between
  "missing" and "added".
- **Deployment field scope is a curated broader list, not a generic structural diff** —
  replicas, images, resource requests/limits, plus env vars, volumes/mounts, probes,
  security context, node selector/affinity/tolerations, service account, restart policy.
  Explicitly not a reflect-based walk of the entire object; a fixed, documented field list
  matching the rest of the codebase's explicit-typed-field style.
- **Labels and annotations excluded entirely** — no diffing, no denylist, on both object
  kinds.
- **ConfigMap diffs show changed key names only, never values** — avoids dumping
  potentially large or multi-line config content.
- **Scalar changes show both values** (`ContextA=x, ContextB=y`); no old→new arrow since
  there's no direction.
- **CLI takes `--context-a`/`--context-b`, sharing the global `--kubeconfig`** — the common
  case (two contexts in one kubeconfig file). Per-side kubeconfig override was considered
  and rejected for v1 as unneeded complexity; revisit if a real need for comparing across
  two separate kubeconfig files shows up.
- **Every changed field is its own Finding, `Warning` severity, uniformly** — no field is
  elevated to `Critical`.

## 13. CLI wiring (`internal/cli/diff.go`)

```go
func newDiffCmd() *cobra.Command {
	var contextA, contextB string
	var includeSystem bool

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Structured diff of deployments, configmaps, and resource configs between two contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientA, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: contextA})
			if err != nil {
				return fmt.Errorf("building clientset for context %q: %w", contextA, err)
			}
			clientB, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: contextB})
			if err != nil {
				return fmt.Errorf("building clientset for context %q: %w", contextB, err)
			}

			report, err := diff.Analyze(cmd.Context(), clientA, clientB, diff.Options{
				Namespace:     namespace,
				AllNamespaces: allNamespaces,
				IncludeSystem: includeSystem,
				ContextA:      contextA,
				ContextB:      contextB,
			})
			if err != nil {
				return err
			}

			return render.Render(cmd.OutOrStdout(), report, output)
		},
	}

	cmd.Flags().StringVar(&contextA, "context-a", "", "first kubeconfig context to compare (required)")
	cmd.Flags().StringVar(&contextB, "context-b", "", "second kubeconfig context to compare (required)")
	cmd.Flags().BoolVar(&includeSystem, "include-system", false, "include kube-system/kube-public/kube-node-lease when comparing all namespaces")
	cmd.MarkFlagRequired("context-a")
	cmd.MarkFlagRequired("context-b")

	return cmd
}
```

`--kubeconfig` and `--context` (the global, single-context flag) both still exist on the
root command for the other four commands; `diff` simply ignores the global `--context` and
uses its own `--context-a`/`--context-b` instead. `--kubeconfig` is reused as-is (both
sides read from the same file).

## 14. Package layout

```
internal/diff/diff.go        # Analyze(ctx, clientA, clientB, opts) (types.Report, error)
internal/diff/diff_test.go   # fake.NewSimpleClientset x2 per test case
```

Same shape as `internal/health`, `internal/resources`, `internal/rbac`: an `Options` type,
a top-level `Analyze`, private helpers per finding category, `fmt.Errorf("...: %w", err)`
wrapping on every API call, and a flat `[]types.Finding` built up and returned as
`types.Report{Findings: findings}`. Sort the final findings by
`(Namespace, Resource, Name)` before returning for deterministic output and easy testing.

## 15. Testing strategy

Two `fake.NewSimpleClientset` instances per test case (`clientA`, `clientB`). Cases to
cover, mirroring `internal/rbac`'s breadth:

- Identical Deployment and ConfigMap on both sides → no findings.
- Deployment present only in context A (and only in B) → one `Warning` "exists in ... only"
  finding per case, correct `Namespace`/`Name`.
- Same for ConfigMap.
- Replica count differs → one Finding, message shows both values.
- Container image differs → one Finding per differing container.
- Resource request differs but limit doesn't (and vice versa) → only the differing one
  produces a Finding.
- A container added on one side (name not present on the other) → its own Finding,
  distinct from any field-level Findings on containers present on both sides.
- Env var added/removed/changed on one side → one Finding naming the changed var names,
  no values in the message.
- Probe (`livenessProbe`) differs → one coarse "differs" Finding, no sub-detail.
- ConfigMap with multiple changed/added/removed keys → exactly one Finding for that
  ConfigMap, message lists all three categories.
- Label/annotation-only difference (spec otherwise identical) → no findings at all.
- `-A` with a namespace that exists only in context A → its Deployments/ConfigMaps report
  as "exists in ContextA only"; a namespace that's `kube-system` is excluded from that run
  unless `IncludeSystem: true`.
- Explicit `Namespace: "kube-system"` (no `AllNamespaces`) still diffs it — exclusion only
  applies to auto-discovery.

## 16. Non-goals (unchanged from `CLAUDE.md`)

- No resource kinds beyond Deployment and ConfigMap (no Services, StatefulSets, Secrets,
  CRDs, etc.).
- No mutation — `diff` is read-only like every other kscope command.
- No generic/reflection-based whole-object diff — the field list in §8 is the complete,
  intentional scope; nothing implicit is compared.
