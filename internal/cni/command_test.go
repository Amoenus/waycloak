// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"strings"
	"testing"
	"time"
)

const primaryResult = `{
  "cniVersion":"1.0.0",
  "name":"kindnet",
  "type":"waycloak",
  "prevResult":{
    "cniVersion":"1.0.0",
    "interfaces":[{"name":"eth0","sandbox":"/var/run/netns/example"}],
    "ips":[{"interface":0,"address":"10.244.0.2/24"}],
    "routes":[{"dst":"0.0.0.0/0"}]
  },
  "agentSocket":"/run/waycloak/test.sock",
  "stateDir":"/var/lib/cni/waycloak-test",
  "resolveTimeout":"1s",
  "bindingTimeout":"4s",
  "retryInterval":"50ms"
}`

func TestParseRequiresExactKubernetesIdentityAndPrevResult(t *testing.T) {
	args := "IgnoreUnknown=1;K8S_POD_NAMESPACE=apps;K8S_POD_NAME=protected;K8S_POD_UID=uid-1;K8S_POD_INFRA_CONTAINER_ID=sandbox-1"
	parsed, err := Parse([]byte(primaryResult), "sandbox-1", "/netns/pod", "eth0", args, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Request.Pod.UID != "uid-1" || parsed.Conf.PrevResult == nil || parsed.BindingTimeout != 4*time.Second {
		t.Fatalf("parsed configuration = %#v", parsed)
	}
	if parsed.Conf.AgentKeyFile != DefaultAgentKeyFile {
		t.Fatalf("default local protocol key path = %q", parsed.Conf.AgentKeyFile)
	}
	if _, err := Parse([]byte(primaryResult), "different", "/netns/pod", "eth0", args, true, true); err == nil {
		t.Fatal("mismatched sandbox identity was accepted")
	}
	withoutPrevious := []byte(`{"cniVersion":"1.0.0","name":"kindnet","type":"waycloak"}`)
	if _, err := Parse(withoutPrevious, "sandbox-1", "/netns/pod", "eth0", args, true, true); err == nil {
		t.Fatal("missing prevResult was accepted")
	}
}

func TestParseAcceptsLegacyKindnetPrevResult(t *testing.T) {
	legacy := strings.ReplaceAll(primaryResult, "1.0.0", "0.3.1")
	args := "IgnoreUnknown=1;K8S_POD_NAMESPACE=apps;K8S_POD_NAME=protected;K8S_POD_UID=uid-1;K8S_POD_INFRA_CONTAINER_ID=sandbox-1"
	parsed, err := Parse([]byte(legacy), "sandbox-1", "/netns/pod", "eth0", args, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Conf.CNIVersion != "0.3.1" || parsed.Conf.PrevResult == nil {
		t.Fatalf("legacy kindnet configuration was not retained: %#v", parsed.Conf)
	}
}

func TestParseRejectsUnboundedWaits(t *testing.T) {
	config := []byte(`{"cniVersion":"1.0.0","name":"kindnet","type":"waycloak","resolveTimeout":"6s"}`)
	if _, err := Parse(config, "", "", "", "", false, false); err == nil {
		t.Fatal("unbounded resolve timeout was accepted")
	}
	config = []byte(`{"cniVersion":"1.0.0","name":"kindnet","type":"waycloak","bindingTimeout":"31s"}`)
	if _, err := Parse(config, "", "", "", "", false, false); err == nil {
		t.Fatal("unbounded binding timeout was accepted")
	}
}

func TestParseRejectsRelativeLocalProtocolPaths(t *testing.T) {
	config := []byte(`{"cniVersion":"1.0.0","name":"kindnet","type":"waycloak","agentKeyFile":"relative.key"}`)
	if _, err := Parse(config, "", "", "", "", false, false); err == nil {
		t.Fatal("relative local protocol key path was accepted")
	}
	config = []byte(`{"cniVersion":"1.0.0","name":"kindnet","type":"waycloak","agentSocket":"/run/waycloak/agent.sock","agentKeyFile":"/run/other/agent.key"}`)
	if _, err := Parse(config, "", "", "", "", false, false); err == nil {
		t.Fatal("split local protocol directories were accepted")
	}
}

func TestNewPluginSeparatesAgentRequestTimeoutFromRetryCadence(t *testing.T) {
	parsed := Parsed{
		Conf:           NetConf{AgentSocket: "/run/waycloak/test.sock", StateDir: t.TempDir()},
		ResolveTimeout: 2 * time.Second,
		BindingTimeout: 10 * time.Second,
		RetryInterval:  10 * time.Millisecond,
	}
	plugin := NewPlugin(parsed, &fakeEnforcer{})
	client, ok := plugin.Agent.(UnixAgentClient)
	if !ok {
		t.Fatalf("agent = %T", plugin.Agent)
	}
	if client.RequestTimeout != time.Second || client.RequestTimeout == parsed.RetryInterval {
		t.Fatalf("agent request timeout = %s, retry interval = %s", client.RequestTimeout, parsed.RetryInterval)
	}
}
