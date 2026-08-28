// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package portforward

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	adapterPathPrefix  = "/networking.waycloak.io/adapter/v1/leases/"
	DefaultAdapterPort = uint16(9444)
)

type AdapterFailureKind string

func (k AdapterFailureKind) String() string { return string(k) }

const (
	AdapterFailureConflict    AdapterFailureKind = "conflict"
	AdapterFailureUnavailable AdapterFailureKind = "unavailable"
)

type AdapterRequestError struct {
	Kind       AdapterFailureKind
	StatusCode int
}

func (e *AdapterRequestError) Error() string {
	return fmt.Sprintf("application adapter request failed: kind=%s status=%d", e.Kind, e.StatusCode)
}

type AdapterWithdrawalAcknowledgement struct {
	APIVersion        string              `json:"apiVersion"`
	LeaseNamespace    wayv1.NamespaceName `json:"leaseNamespace"`
	LeaseUID          wayv1.ObjectUID     `json:"leaseUID"`
	HandoffGeneration int64               `json:"handoffGeneration"`
	PodUID            wayv1.ObjectUID     `json:"podUID"`
	ObservedAt        time.Time           `json:"observedAt"`
	Withdrawn         bool                `json:"withdrawn"`
}

type AdapterHealthObservation struct {
	APIVersion string              `json:"apiVersion"`
	Namespace  wayv1.NamespaceName `json:"namespace"`
	Name       wayv1.ObjectName    `json:"name"`
	Image      string              `json:"image"`
	PodUID     wayv1.ObjectUID     `json:"podUID"`
	ObservedAt time.Time           `json:"observedAt"`
	Ready      bool                `json:"ready"`
}

type AdapterHealthChecker interface {
	Observe(context.Context, wayv1.NamespaceName, wayv1.ObjectName, string) (AdapterHealthObservation, error)
}

// HTTPAdapterClient uses only mTLS and the bounded adapter protocol. It does
// not load a service-account token and never follows environment proxies.
type HTTPAdapterClient struct {
	Client        *http.Client
	Port          uint16
	ClusterDomain string
	Now           func() time.Time
}

func NewHTTPAdapterClient(caFile, certFile, keyFile string, port uint16, clusterDomain string) (*HTTPAdapterClient, error) {
	clusterDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(clusterDomain)), ".")
	if port == 0 || len(utilvalidation.IsDNS1123Subdomain(clusterDomain)) != 0 {
		return nil, errors.New("adapter HTTPS port and cluster domain are required")
	}
	tlsConfig, err := clientTLSConfig(caFile, certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &HTTPAdapterClient{Port: port, ClusterDomain: clusterDomain, Client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: nil, TLSClientConfig: tlsConfig, DisableCompression: true, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
	}}}, nil
}

func (c *HTTPAdapterClient) Deliver(ctx context.Context, name wayv1.ObjectName, record AdapterLeaseRecord) (AdapterAcknowledgement, error) {
	var acknowledgement AdapterAcknowledgement
	if err := c.call(ctx, record.LeaseNamespace, name, http.MethodPut, adapterPath(record.LeaseUID, ""), record, &acknowledgement); err != nil {
		return AdapterAcknowledgement{}, err
	}
	return acknowledgement, nil
}

func (c *HTTPAdapterClient) Withdraw(ctx context.Context, name wayv1.ObjectName, intent AdapterWithdrawalIntent) (bool, error) {
	var acknowledgement AdapterWithdrawalAcknowledgement
	if err := c.call(ctx, intent.LeaseNamespace, name, http.MethodPost, adapterPath(intent.LeaseUID, "withdraw"), intent, &acknowledgement); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	exact := acknowledgement.APIVersion == AdapterAPIVersion && acknowledgement.LeaseNamespace == intent.LeaseNamespace && acknowledgement.LeaseUID == intent.LeaseUID &&
		acknowledgement.HandoffGeneration == intent.HandoffGeneration && acknowledgement.PodUID == intent.PodUID && !acknowledgement.ObservedAt.Before(now.Add(-DefaultObservationFreshness)) &&
		!acknowledgement.ObservedAt.After(now.Add(time.Minute)) && acknowledgement.Withdrawn
	return exact, nil
}

func (c *HTTPAdapterClient) Observe(ctx context.Context, namespace wayv1.NamespaceName, name wayv1.ObjectName, image string) (AdapterHealthObservation, error) {
	if c == nil || c.Client == nil || c.Port == 0 || c.ClusterDomain == "" || namespace == "" || name == "" || image == "" {
		return AdapterHealthObservation{}, errors.New("exact adapter health identity is required")
	}
	endpoint := adapterEndpoint(namespace, name, c.Port, c.ClusterDomain)
	endpoint.Path = "/networking.waycloak.io/adapter/v1/healthz"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return AdapterHealthObservation{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.Client.Do(request)
	if err != nil {
		return AdapterHealthObservation{}, fmt.Errorf("observe application adapter: %w", err)
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, runtimeBodyLimit)
	if err != nil {
		return AdapterHealthObservation{}, err
	}
	if response.StatusCode != http.StatusOK {
		return AdapterHealthObservation{}, fmt.Errorf("application adapter health returned status %d", response.StatusCode)
	}
	var observation AdapterHealthObservation
	if err := decodeStrict(body, &observation); err != nil {
		return AdapterHealthObservation{}, fmt.Errorf("decode application adapter health: %w", err)
	}
	if observation.APIVersion != AdapterAPIVersion || observation.Namespace != namespace || observation.Name != name || observation.Image != image {
		return AdapterHealthObservation{}, errors.New("application adapter health identity mismatch")
	}
	return observation, nil
}

func (c *HTTPAdapterClient) call(ctx context.Context, namespace wayv1.NamespaceName, name wayv1.ObjectName, method, path string, input, output any) error {
	if c == nil || c.Client == nil || c.Port == 0 || c.ClusterDomain == "" || namespace == "" || name == "" {
		return errors.New("exact adapter endpoint identity is required")
	}
	body, err := json.Marshal(input)
	if err != nil || int64(len(body)) > runtimeBodyLimit {
		return errors.New("adapter request is invalid")
	}
	endpoint := adapterEndpoint(namespace, name, c.Port, c.ClusterDomain)
	endpoint.Path = path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.Client.Do(request)
	if err != nil {
		return fmt.Errorf("call application adapter: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := readBounded(response.Body, runtimeBodyLimit)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		kind := AdapterFailureUnavailable
		if response.StatusCode == http.StatusConflict {
			kind = AdapterFailureConflict
		}
		return &AdapterRequestError{Kind: kind, StatusCode: response.StatusCode}
	}
	if err := decodeStrict(responseBody, output); err != nil {
		return fmt.Errorf("decode application adapter observation: %w", err)
	}
	return nil
}

func AdapterServiceName(namespace wayv1.NamespaceName, name wayv1.ObjectName) string {
	digest := sha256.Sum256([]byte(string(namespace) + "/" + string(name)))
	return fmt.Sprintf("waycloak-adapter-%x", digest[:8])
}

func adapterEndpoint(namespace wayv1.NamespaceName, name wayv1.ObjectName, port uint16, clusterDomain string) *url.URL {
	return &url.URL{Scheme: "https", Host: AdapterServiceName(namespace, name) + "." + string(namespace) + ".svc." + clusterDomain + ":" + strconv.Itoa(int(port))}
}

func adapterPath(uid wayv1.ObjectUID, operation string) string {
	path := adapterPathPrefix + url.PathEscape(string(uid))
	if operation != "" {
		path += "/" + operation
	}
	return path
}

var _ AdapterProtocol = (*HTTPAdapterClient)(nil)
var _ AdapterHealthChecker = (*HTTPAdapterClient)(nil)
