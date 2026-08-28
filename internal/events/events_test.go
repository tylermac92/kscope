package events

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func warningEvent(name, namespace, involvedName string, count int32, last time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: namespace,
			Name:      involvedName,
		},
		Type:          corev1.EventTypeWarning,
		Reason:        "BackOff",
		Message:       "back-off restarting failed container",
		Count:         count,
		LastTimestamp: metav1.NewTime(last),
	}
}

func normalEvent(name, namespace, involvedName string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: namespace,
			Name:      involvedName,
		},
		Type:          corev1.EventTypeNormal,
		Reason:        "Scheduled",
		Message:       "successfully assigned",
		Count:         1,
		LastTimestamp: metav1.NewTime(time.Now()),
	}
}

func TestAnalyze_NoWarningEvents(t *testing.T) {
	client := fake.NewSimpleClientset(normalEvent("e1", "default", "web-1"))

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings with only Normal events, got %+v", report.Findings)
	}
}

func TestAnalyze_CollapsesRepeatsUsingCountField(t *testing.T) {
	base := time.Now()

	client := fake.NewSimpleClientset(
		warningEvent("e1", "default", "web-1", 3, base),
		warningEvent("e2", "default", "web-1", 2, base.Add(time.Minute)),
	)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if len(report.Findings) != 1 {
		t.Fatalf("expected one collapsed group for web-1, got %d: %+v", len(report.Findings), report.Findings)
	}

	f := report.Findings[0]
	if f.Name != "web-1" || f.Resource != "Pod" || f.Namespace != "default" {
		t.Fatalf("unexpected finding subject: %+v", f)
	}
	if !strings.Contains(f.Message, "x5") {
		t.Fatalf("expected message to report the summed count (3+2=5), got %q", f.Message)
	}
}

func TestAnalyze_SeparateObjectsSortedByRecency(t *testing.T) {
	base := time.Now()

	client := fake.NewSimpleClientset(
		warningEvent("e1", "default", "older", 1, base),
		warningEvent("e2", "default", "newer", 1, base.Add(time.Hour)),
	)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if len(report.Findings) != 2 {
		t.Fatalf("expected two groups, got %d: %+v", len(report.Findings), report.Findings)
	}
	if report.Findings[0].Name != "newer" {
		t.Fatalf("expected most recent group (newer) first, got %+v", report.Findings)
	}
	if report.Findings[1].Name != "older" {
		t.Fatalf("expected least recent group (older) last, got %+v", report.Findings)
	}
}

func TestAnalyze_DefaultCapAndAll(t *testing.T) {
	base := time.Now()
	numObjects := defaultCap + 5

	objs := make([]runtime.Object, 0, numObjects)
	for i := 0; i < numObjects; i++ {
		objs = append(objs, warningEvent(
			fmt.Sprintf("e%d", i), "default", fmt.Sprintf("pod-%d", i),
			1, base.Add(time.Duration(i)*time.Minute),
		))
	}
	client := fake.NewSimpleClientset(objs...)

	report, err := Analyze(context.Background(), client, Options{AllNamespaces: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(report.Findings) != defaultCap {
		t.Fatalf("expected default cap of %d groups, got %d", defaultCap, len(report.Findings))
	}
	// pod-(numObjects-1) has the latest timestamp, so it must survive the cap
	// and sort first.
	wantMostRecent := fmt.Sprintf("pod-%d", numObjects-1)
	if report.Findings[0].Name != wantMostRecent {
		t.Fatalf("expected %s first under the cap, got %+v", wantMostRecent, report.Findings[0])
	}

	allReport, err := Analyze(context.Background(), client, Options{AllNamespaces: true, All: true})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(allReport.Findings) != numObjects {
		t.Fatalf("expected --all to return all %d groups, got %d", numObjects, len(allReport.Findings))
	}
}
