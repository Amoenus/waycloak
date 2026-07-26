// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}

func command(t *testing.T, environment []string, name string, arguments ...string) string {
	t.Helper()
	cmd := exec.Command(name, arguments...)
	if environment != nil {
		cmd.Env = environment
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, arguments, err, output)
	}
	return string(output)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func waitForPodReady(t *testing.T, c client.Client, pod *corev1.Pod) {
	t.Helper()
	waitFor(t, 60*time.Second, func() bool {
		var current corev1.Pod
		if c.Get(context.Background(), client.ObjectKeyFromObject(pod), &current) != nil {
			return false
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
}

func copyLocalFile(t *testing.T, local, namespace, pod, remote string, container ...string) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	must(t, err)
	relative, err := filepath.Rel(workingDirectory, local)
	must(t, err)
	copyArguments := []string{"cp", relative, namespace + "/" + pod + ":" + remote}
	execArguments := []string{"exec", "-n", namespace, pod}
	if len(container) > 1 {
		t.Fatalf("copyLocalFile accepts at most one container, got %d", len(container))
	}
	if len(container) == 1 {
		copyArguments = append(copyArguments, "-c", container[0])
		execArguments = append(execArguments, "-c", container[0])
	}
	command(t, nil, "kubectl", copyArguments...)
	execArguments = append(execArguments, "--", "chmod", "+x", remote)
	command(t, nil, "kubectl", execArguments...)
}

func commandSucceeds(namespace, pod, shellCommand string) bool {
	return execCommand("kubectl", "exec", "-n", namespace, pod, "--", "sh", "-c", shellCommand) == nil
}

func execCommand(name string, arguments ...string) error {
	cmd := exec.Command(name, arguments...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
