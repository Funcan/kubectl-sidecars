package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// runningUnreadyPod models the real-world pod that surfaced the --running-only
// bug (anonymised from a captured `kubectl get pod -o json`): the pod is in the
// Running phase, but its main container's readiness probe is failing so it
// reports Ready=false while its process is still running. A ready sidecar shares
// the pod. Such a pod must be treated as running.
func runningUnreadyPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-pod",
			Namespace: "example-ns",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:    "app",
					Image:   "registry.example.com/app:latest",
					Ready:   false,
					Started: boolPtr(true),
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
				{
					Name:    "sidecar",
					Image:   "registry.example.com/sidecar:latest",
					Ready:   true,
					Started: boolPtr(true),
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

func TestIsRunning(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "running phase with an unready but running container",
			pod:  runningUnreadyPod(),
			want: true,
		},
		{
			name: "running phase with all containers ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					},
				},
			},
			want: true,
		},
		{
			name: "pending phase",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			want: false,
		},
		{
			name: "succeeded phase",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			want: false,
		},
		{
			name: "failed phase",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodFailed},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRunning(tc.pod); got != tc.want {
				t.Errorf("isRunning() = %v, want %v", got, tc.want)
			}
		})
	}
}
