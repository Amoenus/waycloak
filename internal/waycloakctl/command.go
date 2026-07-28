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
	case "version":
		_, err := fmt.Fprintln(dependencies.Stdout, Version)
		return err
	default:
		return usage(dependencies.Stderr)
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
	case "plan":
		flags := flag.NewFlagSet("install plan", flag.ContinueOnError)
		flags.SetOutput(dependencies.Stderr)
		kubeconfig, contextName, output := clusterFlags(flags)
		manifestPath := flags.String("release-manifest", "", "verified release manifest JSON")
		namespace := flags.String("namespace", "waycloak-system", "system namespace")
		release := flags.String("release", "waycloak", "Helm release name")
		overlay := flags.String("overlay-cidr", "100.96.0.0/16", "reviewed overlay CIDR")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *manifestPath == "" {
			return errors.New("--release-manifest is required")
		}
		manifest, _, err := LoadReleaseManifest(*manifestPath)
		if err != nil {
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
		plan, err := BuildInstallPlan(manifest, *namespace, *release, report)
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
	fmt.Fprintln(writer, "usage: waycloakctl <preflight|install plan|install apply|gateway init|doctor|verify|support-bundle|alpha-purge plan|alpha-purge apply|version>")
	return errors.New("invalid command")
}

var Version = "development"
