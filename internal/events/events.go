// Package events implements the kscope events command's business logic:
// pulling Warning events, grouping them by involved object, and surfacing
// the most recently active groups first.
package events

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tylermac92/kscope/pkg/types"
)

// defaultCap limits how many event groups Analyze returns by default;
// Options.All disables it.
const defaultCap = 20

// Options configures the scope of Analyze.
type Options struct {
	// Namespace restricts the event pull to a single namespace. Ignored
	// when AllNamespaces is set.
	Namespace string
	// AllNamespaces pulls events across every namespace instead of just
	// Namespace.
	AllNamespaces bool
	// All disables the default cap on the number of groups returned.
	All bool
}

func (o Options) namespace() string {
	if o.AllNamespaces {
		return metav1.NamespaceAll
	}
	return o.Namespace
}

// group accumulates every Warning event seen for one involved object.
type group struct {
	kind      string
	namespace string
	name      string
	reason    string
	message   string
	count     int32
	last      time.Time
}

// Analyze pulls Warning events (cluster-wide or namespace-scoped per opts),
// groups them by involved object (kind/namespace/name), collapses repeats
// using each event's own Count field rather than re-counting duplicate
// event objects client-side, and returns groups sorted by most recent
// activity. The result is capped at defaultCap groups unless opts.All is
// set.
func Analyze(ctx context.Context, client kubernetes.Interface, opts Options) (types.Report, error) {
	eventList, err := client.CoreV1().Events(opts.namespace()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return types.Report{}, fmt.Errorf("listing events: %w", err)
	}

	groups := make(map[string]*group)
	var ordered []*group

	for _, event := range eventList.Items {
		if event.Type != corev1.EventTypeWarning {
			continue
		}

		key := event.InvolvedObject.Kind + "/" + event.InvolvedObject.Namespace + "/" + event.InvolvedObject.Name

		g, ok := groups[key]
		if !ok {
			g = &group{
				kind:      event.InvolvedObject.Kind,
				namespace: event.InvolvedObject.Namespace,
				name:      event.InvolvedObject.Name,
			}
			groups[key] = g
			ordered = append(ordered, g)
		}

		g.count += eventCount(event)

		if last := lastSeen(event); last.After(g.last) {
			g.last = last
			g.reason = event.Reason
			g.message = event.Message
		}
	}

	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].last.After(ordered[j].last)
	})

	if !opts.All && len(ordered) > defaultCap {
		ordered = ordered[:defaultCap]
	}

	findings := make([]types.Finding, 0, len(ordered))
	for _, g := range ordered {
		findings = append(findings, types.Finding{
			Severity:  types.SeverityWarning,
			Namespace: g.namespace,
			Resource:  g.kind,
			Name:      g.name,
			Message: fmt.Sprintf("%s (x%d, last seen %s): %s",
				g.reason, g.count, g.last.UTC().Format(time.RFC3339), g.message),
		})
	}

	return types.Report{Findings: findings}, nil
}

// eventCount returns how many times this event object represents an
// occurrence, using Kubernetes' own dedup counter rather than treating each
// stored event object as exactly one occurrence.
func eventCount(event corev1.Event) int32 {
	if event.Count > 0 {
		return event.Count
	}
	return 1
}

// lastSeen returns the best available timestamp for when this event last
// fired, falling back across the fields the Events API may populate.
func lastSeen(event corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	return event.CreationTimestamp.Time
}
