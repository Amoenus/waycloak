// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	cnitypes "github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/version"
)

const (
	DefaultAgentSocket  = "/run/waycloak/cni-agent.sock"
	DefaultAgentKeyFile = "/run/waycloak/cni-auth.key"
	DefaultStateDir     = "/var/lib/cni/waycloak/attachments"
	AgentRequestTimeout = 5 * time.Second
)

type NetConf struct {
	cnitypes.PluginConf
	AgentSocket    string `json:"agentSocket,omitempty"`
	AgentKeyFile   string `json:"agentKeyFile,omitempty"`
	StateDir       string `json:"stateDir,omitempty"`
	ResolveTimeout string `json:"resolveTimeout,omitempty"`
	BindingTimeout string `json:"bindingTimeout,omitempty"`
	RetryInterval  string `json:"retryInterval,omitempty"`
}

type KubernetesArgs struct {
	cnitypes.CommonArgs
	K8S_POD_NAMESPACE          cnitypes.UnmarshallableString
	K8S_POD_NAME               cnitypes.UnmarshallableString
	K8S_POD_UID                cnitypes.UnmarshallableString
	K8S_POD_INFRA_CONTAINER_ID cnitypes.UnmarshallableString
}

type Parsed struct {
	Conf           NetConf
	Request        Request
	ResolveTimeout time.Duration
	BindingTimeout time.Duration
	RetryInterval  time.Duration
}

func Parse(stdin []byte, containerID, netns, ifName, args string, requireIdentity, requirePrevResult bool) (Parsed, error) {
	var conf NetConf
	if err := json.Unmarshal(stdin, &conf); err != nil {
		return Parsed{}, fmt.Errorf("decode CNI configuration: %w", err)
	}
	if conf.Name == "" || conf.CNIVersion == "" {
		return Parsed{}, errors.New("CNI network name and version are required")
	}
	if requirePrevResult && conf.RawPrevResult == nil {
		return Parsed{}, errors.New("waycloak must be chained after the primary CNI and requires prevResult")
	}
	if conf.RawPrevResult != nil {
		if err := version.ParsePrevResult(&conf.PluginConf); err != nil {
			return Parsed{}, fmt.Errorf("parse prevResult: %w", err)
		}
	}
	if conf.AgentSocket == "" {
		conf.AgentSocket = DefaultAgentSocket
	}
	if conf.StateDir == "" {
		conf.StateDir = DefaultStateDir
	}
	if conf.AgentKeyFile == "" {
		conf.AgentKeyFile = DefaultAgentKeyFile
	}
	if !path.IsAbs(conf.AgentSocket) || !path.IsAbs(conf.AgentKeyFile) || strings.ContainsAny(conf.AgentSocket+conf.AgentKeyFile, "\\\x00") {
		return Parsed{}, errors.New("local agent socket and authentication key paths must be absolute Linux paths")
	}
	if path.Dir(conf.AgentSocket) != path.Dir(conf.AgentKeyFile) {
		return Parsed{}, errors.New("local agent socket and authentication key must share one protected directory")
	}
	resolveTimeout, err := boundedDuration(conf.ResolveTimeout, 2*time.Second, 100*time.Millisecond, 5*time.Second, "resolveTimeout")
	if err != nil {
		return Parsed{}, err
	}
	bindingTimeout, err := boundedDuration(conf.BindingTimeout, 10*time.Second, time.Second, 30*time.Second, "bindingTimeout")
	if err != nil {
		return Parsed{}, err
	}
	retryInterval, err := boundedDuration(conf.RetryInterval, 100*time.Millisecond, 10*time.Millisecond, time.Second, "retryInterval")
	if err != nil {
		return Parsed{}, err
	}
	parsed := Parsed{Conf: conf, ResolveTimeout: resolveTimeout, BindingTimeout: bindingTimeout, RetryInterval: retryInterval}
	if !requireIdentity {
		return parsed, nil
	}
	var k8sArgs KubernetesArgs
	if err := cnitypes.LoadArgs(args, &k8sArgs); err != nil {
		return Parsed{}, fmt.Errorf("parse Kubernetes CNI arguments: %w", err)
	}
	pod := PodIdentity{
		Namespace: string(k8sArgs.K8S_POD_NAMESPACE), Name: string(k8sArgs.K8S_POD_NAME),
		UID: string(k8sArgs.K8S_POD_UID), ContainerID: containerID, IfName: ifName, NetNS: netns,
	}
	if err := pod.Validate(); err != nil {
		return Parsed{}, err
	}
	if infraID := string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID); infraID == "" || infraID != containerID {
		return Parsed{}, fmt.Errorf("K8S_POD_INFRA_CONTAINER_ID %q does not match CNI_CONTAINERID", infraID)
	}
	parsed.Request = Request{Network: conf.Name, Pod: pod}
	return parsed, nil
}

func NewPlugin(parsed Parsed, enforcer Enforcer) Plugin {
	keyFile := parsed.Conf.AgentKeyFile
	if keyFile == "" {
		keyFile = DefaultAgentKeyFile
	}
	return Plugin{
		Agent:    UnixAgentClient{SocketPath: parsed.Conf.AgentSocket, KeyFile: keyFile, RequestTimeout: AgentRequestTimeout},
		Enforcer: enforcer, Store: FileStore{Directory: parsed.Conf.StateDir},
		ResolveTimeout: parsed.ResolveTimeout, BindingTimeout: parsed.BindingTimeout, RetryInterval: parsed.RetryInterval,
	}
}

func boundedDuration(value string, fallback, minimum, maximum time.Duration, field string) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", field, err)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", field, minimum, maximum)
	}
	return parsed, nil
}
