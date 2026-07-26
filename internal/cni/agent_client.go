// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	resolvePath  = "/cni-feasibility/v1/resolve"
	bindingPath  = "/cni-feasibility/v1/binding"
	checkPath    = "/cni-feasibility/v1/check"
	withdrawPath = "/cni-feasibility/v1/withdraw"
	statusPath   = "/cni-feasibility/v1/status"
)

type AgentRequest struct {
	APIVersion string      `json:"apiVersion"`
	Pod        PodIdentity `json:"pod"`
}

type AgentResponse struct {
	APIVersion string      `json:"apiVersion"`
	Resolution *Resolution `json:"resolution,omitempty"`
	Binding    *Binding    `json:"binding,omitempty"`
}

type UnixAgentClient struct {
	SocketPath     string
	RequestTimeout time.Duration
}

func (c UnixAgentClient) Resolve(ctx context.Context, pod PodIdentity) (Resolution, error) {
	var response AgentResponse
	if err := c.call(ctx, http.MethodPost, resolvePath, AgentRequest{APIVersion: AgentAPIVersion, Pod: pod}, &response); err != nil {
		return Resolution{}, err
	}
	if response.Resolution == nil {
		return Resolution{}, errors.New("local agent omitted enrollment resolution")
	}
	return *response.Resolution, nil
}

func (c UnixAgentClient) Binding(ctx context.Context, pod PodIdentity) (Binding, error) {
	var response AgentResponse
	if err := c.call(ctx, http.MethodPost, bindingPath, AgentRequest{APIVersion: AgentAPIVersion, Pod: pod}, &response); err != nil {
		var statusErr *agentStatusError
		if errors.As(err, &statusErr) && (statusErr.Code == http.StatusConflict || statusErr.Code == http.StatusServiceUnavailable || statusErr.Code == http.StatusNotFound) {
			return Binding{}, ErrBindingNotReady
		}
		return Binding{}, err
	}
	if response.Binding == nil {
		return Binding{}, ErrBindingNotReady
	}
	return *response.Binding, nil
}

func (c UnixAgentClient) Check(ctx context.Context, pod PodIdentity) error {
	return c.call(ctx, http.MethodPost, checkPath, AgentRequest{APIVersion: AgentAPIVersion, Pod: pod}, nil)
}

func (c UnixAgentClient) Withdraw(ctx context.Context, pod PodIdentity) error {
	return c.call(ctx, http.MethodPost, withdrawPath, AgentRequest{APIVersion: AgentAPIVersion, Pod: pod}, nil)
}

func (c UnixAgentClient) Status(ctx context.Context) error {
	return c.call(ctx, http.MethodGet, statusPath, nil, nil)
}

type agentStatusError struct{ Code int }

func (e *agentStatusError) Error() string { return fmt.Sprintf("local agent returned HTTP %d", e.Code) }

func (c UnixAgentClient) call(ctx context.Context, method, path string, body any, output any) error {
	if c.SocketPath == "" {
		return errors.New("local agent socket path is required")
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	var encoded io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		encoded = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://waycloak.local"+path, encoded)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", c.SocketPath)
		},
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call local agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return &agentStatusError{Code: response.StatusCode}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode local agent response: %w", err)
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("local agent response contains trailing JSON")
	}
	responseVersion := output.(*AgentResponse).APIVersion
	if responseVersion != AgentAPIVersion {
		return fmt.Errorf("local agent protocol version %q is unsupported", responseVersion)
	}
	return nil
}
