// Package health implements the kscope health command's business logic: a
// cluster-wide rollup of node conditions, crashlooping pods, pending pods
// blocked on scheduling, and pending PVCs.
package health

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tylermac92/kscope/pkg/types"
)

// Options configures the scope of Analyze.
type Options struct {
	// Namespace restricts pod/PVC checks to a single namespace. Ignored
	// when AllNamespaces is set. Node conditions are always cluster-wide.
	Namespace string
	// AllNamespaces checks pods/PVCs across every namespace instead of
	// just Namespace.
	AllNamespaces bool
}

func (o Options) namespace() string {
	if o.AllNamespaces {
		return metav1.NamespaceAll
	}
	return o.Namespace
}

// Analyze rolls up cluster health into a Report: node Ready/MemoryPressure/
// DiskPressure/PIDPressure conditions, pods in CrashLoopBackOff, Pending
// pods with a matching FailedScheduling event, and PVCs stuck Pending.
func Analyze(ctx context.Context, client kubernetes.Interface, opts Options) (types.Report, error) {
	var findings []types.Finding

	nodeFindings, err := checkNodes(ctx, client)
	if err != nil {
		return types.Report{}, err
	}
	findings = append(findings, nodeFindings...)

	ns := opts.namespace()

	podFindings, err := checkPods(ctx, client, ns)
	if err != nil {
		return types.Report{}, err
	}
	findings = append(findings, podFindings...)

	pvcFindings, err := checkPVCs(ctx, client, ns)
	if err != nil {
		return types.Report{}, err
	}
	findings = append(findings, pvcFindings...)

	return types.Report{Findings: findings}, nil
}

func checkNodes(ctx context.Context, client kubernetes.Interface) ([]types.Finding, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	var findings []types.Finding
	for _, node := range nodes.Items {
		for _, cond := range node.Status.Conditions {
			switch cond.Type {
			case corev1.NodeReady:
				if cond.Status != corev1.ConditionTrue {
					findings = append(findings, types.Finding{
						Severity: types.SeverityCritical,
						Resource: "Node",
						Name:     node.Name,
						Message:  fmt.Sprintf("not Ready (status=%s): %s", cond.Status, cond.Reason),
					})
				}
			case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
				if cond.Status == corev1.ConditionTrue {
					findings = append(findings, types.Finding{
						Severity: types.SeverityWarning,
						Resource: "Node",
						Name:     node.Name,
						Message:  fmt.Sprintf("%s: %s", cond.Type, cond.Reason),
					})
				}
			}
		}
	}

	return findings, nil
}

func checkPods(ctx context.Context, client kubernetes.Interface, namespace string) ([]types.Finding, error) {
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	events, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}

	unschedulable := make(map[string]corev1.Event, len(events.Items))
	for _, event := range events.Items {
		if event.InvolvedObject.Kind == "Pod" && event.Reason == "FailedScheduling" {
			key := event.InvolvedObject.Namespace + "/" + event.InvolvedObject.Name
			unschedulable[key] = event
		}
	}

	var findings []types.Finding
	for _, pod := range pods.Items {
		findings = append(findings, crashLoopFindings(pod)...)

		if pod.Status.Phase != corev1.PodPending {
			continue
		}
		event, ok := unschedulable[pod.Namespace+"/"+pod.Name]
		if !ok {
			continue
		}
		findings = append(findings, types.Finding{
			Severity:  types.SeverityCritical,
			Namespace: pod.Namespace,
			Resource:  "Pod",
			Name:      pod.Name,
			Message:   fmt.Sprintf("Pending, unschedulable: %s", event.Message),
		})
	}

	return findings, nil
}

func crashLoopFindings(pod corev1.Pod) []types.Finding {
	var findings []types.Finding

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting == nil || cs.State.Waiting.Reason != "CrashLoopBackOff" {
			continue
		}

		msg := fmt.Sprintf("container %s crashlooping (restarts: %d)", cs.Name, cs.RestartCount)
		if term := cs.LastTerminationState.Terminated; term != nil {
			msg += fmt.Sprintf(", last exit code %d (%s)", term.ExitCode, term.Reason)
		}

		findings = append(findings, types.Finding{
			Severity:  types.SeverityCritical,
			Namespace: pod.Namespace,
			Resource:  "Pod",
			Name:      pod.Name,
			Message:   msg,
		})
	}

	return findings
}

func checkPVCs(ctx context.Context, client kubernetes.Interface, namespace string) ([]types.Finding, error) {
	pvcs, err := client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing persistentvolumeclaims: %w", err)
	}

	var findings []types.Finding
	for _, pvc := range pvcs.Items {
		if pvc.Status.Phase != corev1.ClaimPending {
			continue
		}
		findings = append(findings, types.Finding{
			Severity:  types.SeverityWarning,
			Namespace: pvc.Namespace,
			Resource:  "PersistentVolumeClaim",
			Name:      pvc.Name,
			Message:   "stuck Pending",
		})
	}

	return findings, nil
}
