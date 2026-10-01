// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux && e2e

package cniinstall

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Amoenus/waycloak/internal/nodeagent"
	"golang.org/x/sys/unix"
)

// Exercise actual Linux bind mounts: a unit test which simply reopens a host
// pathname cannot detect the stale inode problem caused by a file hostPath.
func TestReadOnlyDirectoryMountObservesAtomicInstallationReplacement(t *testing.T) {
	if os.Getenv("WAYCLOAK_E2E_CNI_MOUNTS") != "1" {
		t.Skip("set WAYCLOAK_E2E_CNI_MOUNTS=1 with mount namespace privileges")
	}
	if os.Getenv("WAYCLOAK_CNI_MOUNT_CHILD") != "1" {
		cmd := exec.Command("unshare", "--mount", "--propagation", "private", os.Args[0], "-test.run=^TestReadOnlyDirectoryMountObservesAtomicInstallationReplacement$", "-test.v")
		cmd.Env = append(os.Environ(), "WAYCLOAK_CNI_MOUNT_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated bind-mount proof: %v\n%s", err, output)
		}
		return
	}
	directory := t.TempDir()
	host := filepath.Join(directory, "host")
	observer := filepath.Join(directory, "observer")
	for _, name := range []string{host, observer} {
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount(host, observer, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer unix.Unmount(observer, 0)
	if err := unix.Mount("", observer, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(observer, "forbidden"), nil, 0o600); !errors.Is(err, unix.EROFS) {
		t.Fatalf("observer mount is not read-only: %v", err)
	}
	source := filepath.Join(directory, "source")
	write(t, source, "binary-v1", 0o755)
	primary := filepath.Join(host, "10-primary.conflist")
	write(t, primary, `{"cniVersion":"1.1.0","name":"primary","plugins":[{"type":"bridge"}]}`, 0o644)
	options := fixture(host, source, filepath.Join(host, "05-waycloak.conflist"))
	options.SourceConfigPath = primary
	if err := Install(options); err != nil {
		t.Fatal(err)
	}
	validate := func(binary string) error {
		return nodeagent.ValidateCNIInstallation(filepath.Join(observer, "state", "install-receipt.json"), binary, filepath.Join(observer, "05-waycloak.conflist"), options.ReleaseIdentity)
	}
	observedBinary := filepath.Join(observer, "bin", "waycloak-cni")
	if err := validate(observedBinary); err != nil {
		t.Fatal(err)
	}
	// Negative control recreates the previous file-hostPath behavior.
	staleFile := filepath.Join(directory, "stale-binary")
	write(t, staleFile, "", 0o600)
	if err := unix.Mount(options.BinaryPath, staleFile, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer unix.Unmount(staleFile, 0)
	write(t, source, "binary-v2", 0o755)
	options.ReleaseIdentity.Version = "v1.0.0-beta.2"
	if err := Install(options); err != nil {
		t.Fatal(err)
	}
	if err := validate(observedBinary); err != nil {
		t.Fatalf("directory observer retained stale installation: %v", err)
	}
	if err := validate(staleFile); err == nil {
		t.Fatal("negative control did not reproduce stale file inode")
	}
	if err := atomicWrite(options.ConfigPath, []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validate(observedBinary); err == nil {
		t.Fatal("observer missed atomic config drift")
	}
	backup, err := os.ReadFile(options.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _, err := render(backup, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(options.ConfigPath, rendered, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validate(observedBinary); err != nil {
		t.Fatalf("observer did not recover after exact restoration: %v", err)
	}
}
