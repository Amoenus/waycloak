// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestPublishedRulesSeparateProtectionAndAvailability(t *testing.T) {
	data := readAsset(t, "prometheus-rules.yaml")
	var groups ruleFile
	if err := yaml.UnmarshalStrict(data, &groups); err != nil {
		t.Fatal(err)
	}
	domains := map[string]bool{}
	metrics := map[string]bool{}
	for _, group := range groups.Groups {
		if group.Name == "" || len(group.Rules) == 0 {
			t.Fatalf("invalid rule group %#v", group)
		}
		for _, rule := range group.Rules {
			if rule.Alert == "" || rule.Expr == "" || rule.For == "" {
				t.Fatalf("incomplete alert %#v", rule)
			}
			domain := rule.Labels["failure_domain"]
			posture := rule.Labels["traffic_posture"]
			if domain == "" || posture == "" || rule.Labels["severity"] == "" {
				t.Fatalf("alert lacks stable operational classification: %#v", rule)
			}
			if domain != "observability" && posture != "fail_closed" {
				t.Fatalf("data-plane alert does not state fail-closed posture: %#v", rule)
			}
			domains[domain] = true
			for _, metric := range []string{"waycloak_enrolled_pods", "waycloak_resource_condition_objects", "waycloak_workload_allocations", "waycloak_metrics_collection_success", "controller_runtime_reconcile_errors_total"} {
				if strings.Contains(rule.Expr, metric) {
					metrics[metric] = true
				}
			}
			if strings.Contains(rule.Expr, "waycloak_resource_condition_objects") && !strings.Contains(rule.Expr, "current") {
				t.Fatalf("condition alert can treat a stale positive condition as healthy: %s", rule.Expr)
			}
			for _, forbidden := range []string{"namespace=", "name=", "uid=", "node=", "address=", "port=", "endpoint=", "provider="} {
				if strings.Contains(strings.ToLower(rule.Expr), forbidden) {
					t.Fatalf("alert expression uses sensitive or unbounded label %q: %s", forbidden, rule.Expr)
				}
			}
			if strings.Contains(rule.Annotations["description"], "{{") {
				t.Fatalf("alert description interpolates an unreviewed label: %#v", rule)
			}
		}
	}
	for _, domain := range []string{"protection", "availability", "observability"} {
		if !domains[domain] {
			t.Fatalf("rules do not cover %q failures", domain)
		}
	}
	for _, metric := range []string{"waycloak_enrolled_pods", "waycloak_resource_condition_objects", "waycloak_workload_allocations", "waycloak_metrics_collection_success", "controller_runtime_reconcile_errors_total"} {
		if !metrics[metric] {
			t.Fatalf("rules do not cover %s", metric)
		}
	}
}

func TestPublishedDashboardUsesOnlyAggregateMetrics(t *testing.T) {
	data := readAsset(t, "grafana-dashboard.json")
	var dashboard map[string]any
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard["uid"] != "waycloak-stable-overview" {
		t.Fatalf("dashboard UID = %#v", dashboard["uid"])
	}
	text := string(data)
	for _, metric := range []string{"waycloak_enrolled_pods", "waycloak_resource_condition_objects", "waycloak_workload_allocations", "waycloak_metrics_collection_success", "controller_runtime_reconcile_errors_total"} {
		if !strings.Contains(text, metric) {
			t.Fatalf("dashboard omits %s", metric)
		}
	}
	for _, forbidden := range []string{"{{namespace}}", "{{pod}}", "{{uid}}", "{{node}}", "{{address}}", "{{port}}", "{{endpoint}}", "{{provider}}"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("dashboard exposes sensitive dimension %q", forbidden)
		}
	}
	if !strings.Contains(text, "current={{current}}") {
		t.Fatal("dashboard does not distinguish current from stale conditions")
	}
}

func TestPlainPrometheusScrapeExampleNeedsNoOperator(t *testing.T) {
	path := filepath.Join("..", "..", "config", "observability", "prometheus-scrape.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ScrapeConfigs []struct {
			JobName        string           `json:"job_name"`
			KubernetesSD   []map[string]any `json:"kubernetes_sd_configs"`
			RelabelConfigs []map[string]any `json:"relabel_configs"`
		} `json:"scrape_configs"`
	}
	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.ScrapeConfigs) != 1 || config.ScrapeConfigs[0].JobName != "waycloak-controller" || len(config.ScrapeConfigs[0].KubernetesSD) == 0 || len(config.ScrapeConfigs[0].RelabelConfigs) < 3 {
		t.Fatalf("plain scrape example = %#v", config)
	}
	if strings.Contains(string(data), "ServiceMonitor") || strings.Contains(string(data), "PodMonitor") {
		t.Fatal("plain scrape example introduced a Prometheus Operator dependency")
	}
}

func readAsset(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "charts", "waycloak", "files", "observability", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type ruleFile struct {
	Groups []ruleGroup `json:"groups"`
}

type ruleGroup struct {
	Name  string      `json:"name"`
	Rules []alertRule `json:"rules"`
}

type alertRule struct {
	Alert       string            `json:"alert"`
	Expr        string            `json:"expr"`
	For         string            `json:"for"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}
