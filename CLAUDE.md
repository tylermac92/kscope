# CLAUDE.md

Guidance for Claude Code when working in this repository. Read this fully before making
architectural decisions or starting a new command implementation.

## Project Overview

`kscope` is a single-binary Go CLI that gives a fast, opinionated health snapshot of a
Kubernetes cluster. It is not a `kubectl` wrapper — it talks to the Kubernetes API directly
via `client-go` and encodes real operational judgment about what "healthy," "risky," and
"worth knowing" mean. The audience for the output is an SRE doing a first-pass triage, so
correctness and signal-to-noise ratio matter more than covering every edge case.

Five commands, roughly in order of implementation:

1. `kscope health` — cluster-wide rollup: node conditions, pod restarts, pending pods,
   crashlooping workloads, PVC binding status.
2. `kscope events` — deduplicated, grouped Warning events with a timeline.
3. `kscope resources` — requests/limits vs. node allocatable capacity; headroom, not just
   usage; flags pods with no requests set.
4. `kscope rbac audit` — service accounts and bindings with cluster-admin or wildcard
   (`*`) verbs/resources; flags overly broad bindings.
5. `kscope diff` — structured diff of deployments, configmaps, resource configs, and
   replica counts between two kubeconfig contexts.

Build in this order. Each command is a self-contained vertical slice (own package, own
tests) — don't try to design all five analyzers' shared abstractions up front. Let the
shape of `health` and `events` inform what `resources` and `rbac` actually need to share;
retrofit shared interfaces once the pattern is obvious rather than guessing at it now.

## Tech Stack

- **CLI framework:** Cobra, with Viper for config/flag binding (env vars + optional
  `~/.kscope.yaml` + flags, in that precedence order).
- **Kubernetes access:** `client-go` only. Never shell out to `kubectl` or parse its
  output — that defeats the point of the project.
- **Rendering:** `lipgloss` for tables and color-coded status; reach for `bubbletea` only
  if/when an interactive view is actually justified (e.g., a live-updating `health`
  watch mode) — don't add TUI complexity to commands that are naturally one-shot output.
- **Testing:** `k8s.io/client-go/kubernetes/fake` for unit-testing analyzer logic;
  `sigs.k8s.io/controller-runtime/pkg/envtest` for a smaller set of integration tests
  that exercise real API server behavior (RBAC evaluation, PVC binding, etc.).
- **Release:** GoReleaser + GitHub Actions, cross-compiling for
  linux/darwin/windows on amd64/arm64, publishing to GitHub Releases on tag push.

## Package Layout

```
cmd/kscope/            # main.go — entrypoint only, calls internal/cli.Execute()
internal/cli/          # Cobra command definitions. Thin: parse flags, call an
                        # analyzer, hand the result to a renderer. No business logic.
internal/k8s/           # Client construction: kubeconfig/context loading, clientset
                        # and dynamic client setup, shared helpers (e.g. list-all-
                        # namespaces pagination). Used by every analyzer.
internal/health/        # health command's business logic
internal/events/        # events command's business logic
internal/resources/     # resources command's business logic
internal/rbac/          # rbac audit command's business logic
internal/diff/          # diff command's business logic
internal/render/        # lipgloss table/report rendering, shared color/severity
                        # vocabulary, --output json serialization
pkg/types/              # Exported, stable types shared across analyzers and safe for
                        # external consumers to import: Severity, Finding, Report.
                        # Nothing else goes in pkg/ unless it's genuinely meant to be
                        # a public API — don't use pkg/ as a dumping ground.
```

Command wiring in `internal/cli` must stay thin. If you find yourself writing an `if`
statement about cluster state inside a `cobra.Command`'s `RunE`, that logic belongs in
the corresponding `internal/<command>` package instead.

## The Analyzer Pattern

Every command's business logic package exposes a function with this shape so it can be
unit tested against a fake clientset without touching Cobra or lipgloss at all:

```go
func Analyze(ctx context.Context, client kubernetes.Interface, opts Options) (Report, error)
```

`Report` (defined in `pkg/types`) is a flat, renderer-agnostic structure: a list of
`Finding`s, each with a `Severity` (`OK`, `Warning`, `Critical`), a subject (namespace/
resource/name), and a human-readable message. `internal/render` is the only package that
knows about colors, table widths, or JSON marshaling. This split is what makes the
analyzers testable with `fake.NewSimpleClientset(...)` — feed it objects, assert on the
`Report`, no live cluster required.

## Command Specifications

### `kscope health`
Rolls up: node `Ready`/`MemoryPressure`/`DiskPressure`/`PIDPressure` conditions, pods in
`CrashLoopBackOff` (check `ContainerStatuses[].RestartCount` + `LastTerminationState`),
`Pending` pods with unschedulable events, and PVCs stuck in `Pending`. Default output is
one summary line per category plus a short "worth investigating" list — not a full dump.

### `kscope events`
Pull `Warning` events cluster-wide (or namespace-scoped), group by involved object
(kind/namespace/name), collapse repeats using the event's own `count` field rather than
re-counting duplicates client-side, and sort by most recent. Cap default output length
and offer `--all` to disable the cap.

### `kscope resources`
For each node: sum pod requests/limits from `Pod.Spec.Containers[].Resources` against
`Node.Status.Allocatable`. Report headroom as a percentage, not just raw usage — the
point is "how close to saturated," not a `kubectl top` clone. Separately flag pods with
no `requests` set at all, since those are invisible to this whole calculation and are a
real footgun.

### `kscope rbac audit`
Walk `ClusterRoleBindings`/`RoleBindings` and their referenced `(Cluster)Role`s. Flag:
bindings that grant `cluster-admin`, roles containing a `*` in `verbs` or `resources`,
and bindings that attach a namespace-scoped-sounding service account to a
`ClusterRoleBinding`. Treat `system:` prefixed built-in accounts/roles as expected noise
and exclude them from the default report (offer `--include-system` to show everything).

### `kscope diff`
Takes two `--context` values (or context + kubeconfig pairs). For each: list
Deployments, ConfigMaps, and their resource configs/replica counts per namespace, then
produce a structured diff (added/removed/changed). Changed entries should show the
specific field that differs, not a raw object dump.

## Coding Conventions

- Every exported function that hits the API server takes `context.Context` as its first
  argument and respects cancellation.
- Wrap errors with `fmt.Errorf("...: %w", err)` so callers can `errors.Is`/`errors.As`;
  never swallow an API error silently.
- No global/package-level state for clients or config — construct and pass explicitly,
  which is also what makes fake-clientset testing straightforward.
- Global flags (`--kubeconfig`, `--context`, `--namespace`/`--all-namespaces`,
  `--output`) are defined once on the root command and bound via Viper, not
  redeclared per subcommand.
- `--output table` (default, lipgloss) and `--output json` (machine-readable) should be
  supported by every command from the start — retrofitting JSON output later tends to
  leak formatting concerns back into the analyzer.
- Severity-to-color mapping lives in exactly one place in `internal/render` — Critical
  = red, Warning = yellow, OK = green. Don't hardcode ANSI/hex elsewhere.

## Testing Strategy

- Unit tests for every analyzer live next to the code (`internal/health/health_test.go`,
  etc.) and build their fixtures with `fake.NewSimpleClientset(...)`. Cover both the
  "healthy cluster, no findings" case and each individual finding type — don't only test
  the unhappy path.
- A smaller `envtest`-based suite (build-tagged, e.g. `//go:build envtest`) covers
  behavior that a fake clientset can't reproduce faithfully, particularly RBAC
  evaluation for `rbac audit` and PVC binding lifecycle for `health`. These are slower
  and shouldn't run in the default `go test ./...` path — wire them into a separate
  `make test-integration` / CI job.
- Golden-file tests for `internal/render` output (table and JSON) so formatting
  regressions show up as a diff instead of a manual eyeball check.

## Release Pipeline

GoReleaser config (`.goreleaser.yaml`) builds linux/darwin/windows × amd64/arm64,
generates checksums, and publishes to GitHub Releases. GitHub Actions workflow triggers
on `v*` tag push, runs `go test ./...` first and fails the release if tests fail, then
invokes GoReleaser. Keep a separate, always-on CI workflow (lint + unit tests) that runs
on every PR regardless of tags.

## Non-Goals (for now)

- No cluster mutation of any kind — this is a read-only diagnostic tool. Don't add
  `--fix` flags or auto-remediation.
- No custom resource / operator-specific awareness in v1. Stick to core/v1, apps/v1,
  rbac/v1, and events — that's already enough surface area to do well.
- No persistent daemon/watch mode until the five one-shot commands are solid. If a watch
  mode gets added later, it's an additive flag on `health`, not a new architecture.
