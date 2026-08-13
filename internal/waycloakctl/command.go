// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package waycloakctl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func Run(ctx context.Context, arguments []string, dependencies Dependencies) error {
	if dependencies.Stdout == nil {
		dependencies.Stdout = io.Discard
	}
	if dependencies.Stderr == nil {
		dependencies.Stderr = io.Discard
	}
	if dependencies.Clients == nil {
		dependencies.Clients = DefaultClientFactory
	}
	if len(arguments) == 0 {
		return usage(dependencies.Stderr)
	}
	switch arguments[0] {
	case "preflight":
		return runPreflight(ctx, arguments[1:], dependencies)
	case "install":
		return runInstall(ctx, arguments[1:], dependencies)
	case "gateway":
		return runGateway(arguments[1:], dependencies)
	case "doctor":
		return runDoctor(ctx, arguments[1:], dependencies)
	case "verify":
		return runVerify(ctx, arguments[1:], dependencies)
	case "support-bundle":
		return runSupportBundle(ctx, arguments[1:], dependencies)
	case "alpha-purge":
		return runAlphaPurge(ctx, arguments[1:], dependencies)
	case "state":
		return runState(ctx, arguments[1:], dependencies)
	case "certificate":
		return runCertificate(ctx, arguments[1:], dependencies)
	case "version":
		_, err := fmt.Fprintln(dependencies.Stdout, Version)
		return err
	default:
		return usage(dependencies.Stderr)
	}
}

func runCertificate(ctx context.Context, arguments []string, dependencies Dependencies) error {
	if len(arguments) < 2 || arguments[0] != "rotation" {
		return errors.New("certificate requires rotation plan or rotation apply")
	}
	switch arguments[1] {
	case "plan":
		flags := flag.NewFlagSet("certificate rotation plan", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, output := clusterFlags(flags)
		namespace := flags.String("namespace", "waycloak-system", "system namespace")
		release := flags.String("release", "waycloak", "Helm release name")
		overlay := flags.String("overlay-cidr", "100.96.0.0/16", "reviewed protected overlay CIDR")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		if *output != "json" {
			return errors.New("certificate rotation plan output must be json")
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		if err := ensureNoInstallTransition(ctx, clients, *namespace, *release); err != nil {
			return err
		}
		report, err := Preflight(ctx, clients, *overlay)
		if err != nil {
			return err
		}
		if active, found, err := recoverCertificateRotationPlan(ctx, clients, *namespace, *release, report); err != nil {
			return err
		} else if found {
			return writeOutput(dependencies.Stdout, "json", active)
		}
		source, err := ObserveInstalledRelease(ctx, clients, *namespace, *release)
		if err != nil {
			return err
		}
		plan, err := BuildCertificateRotationPlan(report, source, *namespace, *release, *overlay)
		if err != nil {
			return err
		}
		if err := validateNoOrMatchingStagedCertificate(ctx, clients, plan); err != nil {
			return err
		}
		return writeOutput(dependencies.Stdout, "json", plan)
	case "apply":
		flags := flag.NewFlagSet("certificate rotation apply", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, _ := clusterFlags(flags)
		planPath := flags.String("plan", "", "reviewed certificate rotation plan JSON")
		confirmation := flags.String("confirm", "", "exact planID confirmation")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		plan, err := LoadCertificateRotationPlan(*planPath)
		if err != nil {
			return err
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		return ApplyCertificateRotationPlan(ctx, clients, plan, *confirmation)
	default:
		return errors.New("certificate rotation requires plan or apply")
	}
}

func runPreflight(ctx context.Context, arguments []string, dependencies Dependencies) error {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	flags.SetOutput(dependencies.Stderr)
	kubeconfig, contextName, output := clusterFlags(flags)
	overlay := flags.String("overlay-cidr", "100.96.0.0/16", "reviewed protected overlay CIDR")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
	if err != nil {
		return err
	}
	report, err := Preflight(ctx, clients, *overlay)
	if err != nil {
		return err
	}
	if err := writeOutput(dependencies.Stdout, *output, report); err != nil {
		return err
	}
	if !report.Compatible {
		return errors.New("cluster is incompatible; no mutation was performed")
	}
	return nil
}

func runInstall(ctx context.Context, arguments []string, dependencies Dependencies) error {
	if len(arguments) == 0 {
		return errors.New("install requires plan or apply")
	}
	switch arguments[0] {
	case "repair":
		return runInstallRepair(ctx, arguments[1:], dependencies)
	case "plan":
		flags := flag.NewFlagSet("install plan", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, output := clusterFlags(flags)
		manifestPath := flags.String("release-manifest", "", "verified release manifest JSON")
		namespace := flags.String("namespace", "waycloak-system", "system namespace")
		release := flags.String("release", "waycloak", "Helm release name")
		overlay := flags.String("overlay-cidr", "100.96.0.0/16", "reviewed overlay CIDR")
		nodeArchitecture := flags.String("node-architecture", "", "reviewed amd64 or arm64 support row; required on mixed-architecture clusters")
		enablePortForwarding := flags.Bool("enable-port-forwarding", false, "explicitly enable the release-attested port-forward runtime")
		portForwardTLSSecret := flags.String("port-forward-controller-tls-secret", "", "pre-created immutable controller mTLS Secret for port forwarding")
		enableAdapterProtocol := flags.Bool("enable-adapter-protocol", false, "enable controller trust for explicitly selected out-of-process WorkloadAdapters")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *manifestPath == "" {
			return errors.New("--release-manifest is required")
		}
		if *enableAdapterProtocol && !*enablePortForwarding {
			return errors.New("--enable-adapter-protocol requires --enable-port-forwarding")
		}
		if *enablePortForwarding != (*portForwardTLSSecret != "") {
			return errors.New("--enable-port-forwarding and --port-forward-controller-tls-secret must be supplied together")
		}
		manifest, _, err := LoadReleaseManifest(*manifestPath)
		if err != nil {
			return err
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		if err := ensureNoCertificateRotation(ctx, clients, *namespace, *release); err != nil {
			return err
		}
		if err := ensureNoInstallRepair(ctx, clients, *namespace, *release); err != nil {
			return err
		}
		report, err := Preflight(ctx, clients, *overlay)
		if err != nil {
			return err
		}
		targetCRDs, err := ChartCRDIdentities(ctx, dependencies.RunCommand, manifest.Chart)
		if err != nil {
			return err
		}
		if active, found, err := recoverInstallTransitionPlan(ctx, clients, *namespace, *release, report, manifest, targetCRDs); err != nil {
			return err
		} else if found {
			return writeOutput(dependencies.Stdout, *output, active)
		}
		source, err := ObserveInstalledRelease(ctx, clients, *namespace, *release)
		if err != nil {
			return err
		}
		var portForwarding *PortForwardInstallIdentity
		if *enablePortForwarding {
			identity, identityErr := observePortForwardInstallIdentity(ctx, clients, *namespace, *portForwardTLSSecret, *enableAdapterProtocol)
			if identityErr != nil {
				return fmt.Errorf("observe port-forward controller TLS identity: %w", identityErr)
			}
			portForwarding = &identity
		}
		plan, err := BuildInstallPlan(manifest, *namespace, *release, *nodeArchitecture, report, source, targetCRDs, portForwarding)
		if err != nil {
			return err
		}
		return writeOutput(dependencies.Stdout, *output, plan)
	case "apply":
		flags := flag.NewFlagSet("install apply", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, _ := clusterFlags(flags)
		planPath := flags.String("plan", "", "reviewed install plan JSON")
		confirmation := flags.String("confirm", "", "exact planID confirmation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		plan, err := LoadInstallPlan(*planPath)
		if err != nil {
			return err
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		return ApplyInstallPlan(ctx, clients, dependencies.RunCommand, plan, *confirmation)
	default:
		return errors.New("install requires plan or apply")
	}
}

func runInstallRepair(ctx context.Context, arguments []string, dependencies Dependencies) error {
	if len(arguments) == 0 {
		return errors.New("install repair requires plan or apply")
	}
	switch arguments[0] {
	case "plan":
		flags := flag.NewFlagSet("install repair plan", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, output := clusterFlags(flags)
		namespace := flags.String("namespace", "waycloak-system", "system namespace")
		release := flags.String("release", "waycloak", "Helm release name")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *output != "json" {
			return errors.New("install repair plan output must be json")
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		if err := ensureNoCertificateRotation(ctx, clients, *namespace, *release); err != nil {
			return err
		}
		repair, _, repairFound, err := loadInstallRepairJournal(ctx, clients, *namespace, *release)
		if err != nil {
			return err
		}
		overlay := ""
		if repairFound {
			overlay = repair.Transition.OverlayCIDR
		} else {
			transition, _, found, transitionErr := loadInstallTransitionJournal(ctx, clients, *namespace, *release)
			if transitionErr != nil {
				return transitionErr
			}
			if !found {
				return errors.New("install repair requires an active exact-transition journal")
			}
			overlay = transition.OverlayCIDR
		}
		report, err := Preflight(ctx, clients, overlay)
		if err != nil {
			return err
		}
		if active, found, err := recoverInstallRepairPlan(ctx, clients, *namespace, *release, report); err != nil {
			return err
		} else if found {
			return writeOutput(dependencies.Stdout, "json", active)
		}
		plan, err := BuildInstallRepairPlan(ctx, clients, report, *namespace, *release)
		if err != nil {
			return err
		}
		return writeOutput(dependencies.Stdout, "json", plan)
	case "apply":
		flags := flag.NewFlagSet("install repair apply", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, _ := clusterFlags(flags)
		planPath := flags.String("plan", "", "reviewed Helm transition repair plan JSON")
		confirmation := flags.String("confirm", "", "exact repair planID confirmation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		plan, err := LoadInstallRepairPlan(*planPath)
		if err != nil {
			return err
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		return ApplyInstallRepairPlan(ctx, clients, dependencies.RunCommand, plan, *confirmation)
	default:
		return errors.New("install repair requires plan or apply")
	}
}

func runGateway(arguments []string, dependencies Dependencies) error {
	if len(arguments) == 0 || arguments[0] != "init" {
		return errors.New("gateway requires init")
	}
	flags := flag.NewFlagSet("gateway init", flag.ContinueOnError)
	flags.SetOutput(dependencies.Stderr)
	namespace := flags.String("namespace", "", "gateway namespace")
	name := flags.String("name", "", "gateway name")
	className := flags.String("class", "gluetun.waycloak.io", "gateway class")
	configName := flags.String("config-map", "", "native non-secret ConfigMap name")
	secretName := flags.String("secret", "", "existing credentials Secret name")
	provider := flags.String("provider", "protonvpn", "provider recipe")
	protocol := flags.String("protocol", "openvpn", "engine protocol")
	overlay := flags.String("overlay-cidr", "100.96.0.0/16", "reviewed install overlay CIDR")
	allowVerify := flags.Bool("allow-disruptive-verify", false, "label this gateway as dedicated for confirmation-gated tunnel-loss smoke tests")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	value, err := RenderGatewayRecipe(GatewayRecipe{Namespace: *namespace, Name: *name, ClassName: *className, ConfigMapName: *configName, SecretName: *secretName, Provider: *provider, Protocol: *protocol, OverlayCIDR: *overlay, AllowDisruptiveVerify: *allowVerify})
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(dependencies.Stdout, value)
	return err
}

func runState(ctx context.Context, arguments []string, dependencies Dependencies) error {
	if len(arguments) == 0 {
		return errors.New("state requires backup or restore")
	}
	switch arguments[0] {
	case "backup":
		flags := flag.NewFlagSet("state backup", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, output := clusterFlags(flags)
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *output != "json" {
			return errors.New("state backup output must be json")
		}
		clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
		if err != nil {
			return err
		}
		backup, err := BuildStateBackup(ctx, clients)
		if err != nil {
			return err
		}
		return writeOutput(dependencies.Stdout, "json", backup)
	case "restore":
		if len(arguments) < 2 {
			return errors.New("state restore requires plan or apply")
		}
		switch arguments[1] {
		case "plan":
			flags := flag.NewFlagSet("state restore plan", flag.ContinueOnError)
			flags.SetOutput(dependencies.Stderr)
			kubeconfig, contextName, output := clusterFlags(flags)
			backupPath := flags.String("backup", "", "reviewed portable state backup JSON")
			overlay := flags.String("overlay-cidr", "100.96.0.0/16", "reviewed protected overlay CIDR")
			if err := flags.Parse(arguments[2:]); err != nil {
				return err
			}
			if *output != "json" {
				return errors.New("state restore plan output must be json")
			}
			backup, err := LoadStateBackup(*backupPath)
			if err != nil {
				return err
			}
			clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
			if err != nil {
				return err
			}
			plan, err := BuildStateRestorePlan(ctx, clients, backup, *overlay)
			if err != nil {
				return err
			}
			return writeOutput(dependencies.Stdout, "json", plan)
		case "apply":
			flags := flag.NewFlagSet("state restore apply", flag.ContinueOnError)
			flags.SetOutput(dependencies.Stderr)
			kubeconfig, contextName, _ := clusterFlags(flags)
			planPath := flags.String("plan", "", "reviewed state restore plan JSON")
			confirmation := flags.String("confirm", "", "exact planID confirmation")
			if err := flags.Parse(arguments[2:]); err != nil {
				return err
			}
			plan, err := LoadStateRestorePlan(*planPath)
			if err != nil {
				return err
			}
			clients, err := dependencies.Clients(ctx, *kubeconfig, *contextName)
			if err != nil {
				return err
			}
			return ApplyStateRestorePlan(ctx, clients, plan, *confirmation)
		default:
			return errors.New("state restore requires plan or apply")
		}
	default:
		return errors.New("state requires backup or restore")
	}
}

func clusterFlags(flags *flag.FlagSet) (*string, *string, *string) {
	kubeconfig := flags.String("kubeconfig", os.Getenv("KUBECONFIG"), "Kubernetes client configuration")
	contextName := flags.String("context", "", "Kubernetes context")
	output := flags.String("output", "json", "json or human")
	return kubeconfig, contextName, output
}

func writeOutput(writer io.Writer, format string, value any) error {
	if format != "json" && format != "human" {
		return errors.New("output must be json or human")
	}
	if format == "human" {
		switch typed := value.(type) {
		case PreflightReport:
			_, err := fmt.Fprintf(writer, "Compatible: %t\nKubernetes: %s\nNodes: %d\nCNI: %s\n", typed.Compatible, typed.Cluster.KubernetesVersion, typed.Cluster.NodeCount, typed.CNI.Name)
			if err != nil {
				return err
			}
			for _, check := range typed.Checks {
				if _, err = fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Status, check.Name, check.Summary); err != nil {
					return err
				}
			}
			return nil
		}
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage(writer io.Writer) error {
	fmt.Fprintln(writer, "usage: waycloakctl <preflight|install plan|install apply|install repair plan|install repair apply|certificate rotation plan|certificate rotation apply|gateway init|doctor|verify|support-bundle|alpha-purge plan|alpha-purge apply|state backup|state restore plan|state restore apply|version>")
	return errors.New("invalid command")
}

var Version = "development"
