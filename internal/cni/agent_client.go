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
	resolvePath  = "/cni-node/v1/resolve"
	bindingPath  = "/cni-node/v1/binding"
	checkPath    = "/cni-node/v1/check"
	withdrawPath = "/cni-node/v1/withdraw"
	statusPath   = "/cni-node/v1/status"
)

type AgentRequest struct {
	APIVersion string      `json:"apiVersion"`
	Pod        PodIdentity `json:"pod"`
}

type AgentResponse struct {
	APIVersion string      `json:"apiVersion"`
	Resolution *Resolution `json:"resolution,omitempty"`
	Binding    *Binding    `json:"binding,omitempty"`
	Error      *AgentError `json:"error,omitempty"`
}

type AgentError struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}

const (
	AgentErrorInvalidRequest      = "InvalidRequest"
	AgentErrorPodIdentityMismatch = "PodIdentityMismatch"
	AgentErrorNotEnrolled         = "NotEnrolled"
	AgentErrorBindingNotReady     = "BindingNotReady"
)

type UnixAgentClient struct {
	SocketPath     string
	KeyFile        string
	Key            []byte
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
		if errors.As(err, &statusErr) && statusErr.Reason == AgentErrorBindingNotReady {
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

type agentStatusError struct {
	HTTPStatus int
	Reason     string
}

func (e *agentStatusError) Error() string {
	return fmt.Sprintf("local agent returned %s (HTTP %d)", e.Reason, e.HTTPStatus)
}

func (c UnixAgentClient) call(ctx context.Context, method, path string, body any, output any) error {
	if c.SocketPath == "" {
		return errors.New("local agent socket path is required")
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	authenticator, err := c.authenticator()
	if err != nil {
		return fmt.Errorf("load local protocol authentication: %w", err)
	}
	var bodyBytes []byte
	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://waycloak.local"+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	authentication, err := authenticator.SignRequest(method, path, bodyBytes)
	if err != nil {
		return err
	}
	for name, values := range authentication {
		request.Header[name] = values
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", c.SocketPath)
			if err != nil {
				return nil, err
			}
			if err := VerifyRootAgentPeer(connection); err != nil {
				_ = connection.Close()
				return nil, err
			}
			return connection, nil
		},
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call local agent: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, ProtocolMaxMessage+1))
	if err != nil || len(responseBody) > ProtocolMaxMessage {
		return errors.New("local protocol response exceeds the size limit")
	}
	requestID := request.Header.Get(protocolHeaderID)
	if err := authenticator.VerifyResponse(requestID, response.StatusCode, response.Header, responseBody); err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure AgentResponse
		if err := decodeAgentResponse(responseBody, &failure); err != nil {
			return fmt.Errorf("decode authenticated local protocol error: %w", err)
		}
		if failure.Error == nil || failure.Error.Code == "" || failure.Error.Message == "" {
			return errors.New("authenticated local protocol error omitted its stable code")
		}
		return &agentStatusError{HTTPStatus: response.StatusCode, Reason: failure.Error.Code}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := decodeAgentResponse(responseBody, output.(*AgentResponse)); err != nil {
		return fmt.Errorf("decode local agent response: %w", err)
	}
	return nil
}

func decodeAgentResponse(body []byte, output *AgentResponse) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("local agent response contains trailing JSON")
	}
	if output.APIVersion != AgentAPIVersion {
		return fmt.Errorf("local agent protocol version %q is unsupported", output.APIVersion)
	}
	return nil
}

func (c UnixAgentClient) authenticator() (*ProtocolAuthenticator, error) {
	key := c.Key
	if len(key) == 0 {
		var err error
		key, err = LoadProtocolKey(c.KeyFile)
		if err != nil {
			return nil, err
		}
	}
	return NewProtocolAuthenticator(key)
}
