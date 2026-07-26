// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	ProtocolKeySize       = 32
	ProtocolMaxMessage    = 1 << 20
	ProtocolMaxClockSkew  = 30 * time.Second
	ProtocolMaxReplayIDs  = 4096
	protocolHeaderVersion = "X-Waycloak-Protocol"
	protocolHeaderID      = "X-Waycloak-Request-Id"
	protocolHeaderTime    = "X-Waycloak-Issued-At"
	protocolHeaderAuth    = "X-Waycloak-Authentication"
)

// ProtocolAuthenticator signs local CNI/node-agent messages and rejects stale
// or replayed authenticated requests. One verifier is owned by one agent
// process and is discarded when its per-process key rotates.
type ProtocolAuthenticator struct {
	key        []byte
	now        func() time.Time
	random     io.Reader
	maxSkew    time.Duration
	maxReplay  int
	replayLock sync.Mutex
	seen       map[string]time.Time
}

func NewProtocolAuthenticator(key []byte) (*ProtocolAuthenticator, error) {
	if len(key) != ProtocolKeySize {
		return nil, fmt.Errorf("local protocol key must be exactly %d bytes", ProtocolKeySize)
	}
	return &ProtocolAuthenticator{
		key: append([]byte(nil), key...), now: time.Now, random: rand.Reader,
		maxSkew: ProtocolMaxClockSkew, maxReplay: ProtocolMaxReplayIDs,
		seen: make(map[string]time.Time),
	}, nil
}

func (a *ProtocolAuthenticator) SignRequest(method, requestPath string, body []byte) (http.Header, error) {
	identifierBytes := make([]byte, 16)
	if _, err := io.ReadFull(a.random, identifierBytes); err != nil {
		return nil, fmt.Errorf("generate local protocol request identity: %w", err)
	}
	identifier := base64.RawURLEncoding.EncodeToString(identifierBytes)
	issuedAt := a.now().UTC().Format(time.RFC3339Nano)
	header := make(http.Header)
	header.Set(protocolHeaderVersion, AgentAPIVersion)
	header.Set(protocolHeaderID, identifier)
	header.Set(protocolHeaderTime, issuedAt)
	header.Set(protocolHeaderAuth, encodeMAC(a.requestMAC(identifier, issuedAt, method, requestPath, body)))
	return header, nil
}

func (a *ProtocolAuthenticator) VerifyRequest(method, requestPath string, header http.Header, body []byte) (string, error) {
	version, err := oneHeader(header, protocolHeaderVersion)
	if err != nil || version != AgentAPIVersion {
		return "", errors.New("invalid local protocol version")
	}
	identifier, err := oneHeader(header, protocolHeaderID)
	if err != nil {
		return "", errors.New("invalid local protocol request identity")
	}
	decodedIdentifier, err := base64.RawURLEncoding.DecodeString(identifier)
	if err != nil || len(decodedIdentifier) != 16 {
		return "", errors.New("invalid local protocol request identity")
	}
	issuedAtText, err := oneHeader(header, protocolHeaderTime)
	if err != nil {
		return "", errors.New("invalid local protocol timestamp")
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, issuedAtText)
	if err != nil {
		return "", errors.New("invalid local protocol timestamp")
	}
	provided, err := decodeMACHeader(header)
	if err != nil || !hmac.Equal(provided, a.requestMAC(identifier, issuedAtText, method, requestPath, body)) {
		return "", errors.New("local protocol request authentication failed")
	}
	now := a.now().UTC()
	if issuedAt.Before(now.Add(-a.maxSkew)) || issuedAt.After(now.Add(a.maxSkew)) {
		return "", errors.New("local protocol request is outside the freshness window")
	}
	if err := a.consumeRequestID(identifier, issuedAt, now); err != nil {
		return "", err
	}
	return identifier, nil
}

func (a *ProtocolAuthenticator) SignResponse(requestID string, status int, body []byte) http.Header {
	header := make(http.Header)
	header.Set(protocolHeaderVersion, AgentAPIVersion)
	header.Set(protocolHeaderID, requestID)
	header.Set(protocolHeaderAuth, encodeMAC(a.responseMAC(requestID, status, body)))
	return header
}

func (a *ProtocolAuthenticator) VerifyResponse(requestID string, status int, header http.Header, body []byte) error {
	version, err := oneHeader(header, protocolHeaderVersion)
	if err != nil || version != AgentAPIVersion {
		return errors.New("invalid local protocol response version")
	}
	responseID, err := oneHeader(header, protocolHeaderID)
	if err != nil || responseID != requestID {
		return errors.New("local protocol response identity mismatch")
	}
	provided, err := decodeMACHeader(header)
	if err != nil || !hmac.Equal(provided, a.responseMAC(requestID, status, body)) {
		return errors.New("local protocol response authentication failed")
	}
	return nil
}

func (a *ProtocolAuthenticator) consumeRequestID(identifier string, issuedAt, now time.Time) error {
	a.replayLock.Lock()
	defer a.replayLock.Unlock()
	for candidate, timestamp := range a.seen {
		if timestamp.Before(now.Add(-a.maxSkew)) {
			delete(a.seen, candidate)
		}
	}
	if _, exists := a.seen[identifier]; exists {
		return errors.New("local protocol request was replayed")
	}
	if len(a.seen) >= a.maxReplay {
		return errors.New("local protocol replay window is full")
	}
	a.seen[identifier] = issuedAt
	return nil
}

func (a *ProtocolAuthenticator) requestMAC(identifier, issuedAt, method, requestPath string, body []byte) []byte {
	return a.mac("request", AgentAPIVersion, identifier, issuedAt, method, requestPath, digest(body))
}

func (a *ProtocolAuthenticator) responseMAC(identifier string, status int, body []byte) []byte {
	return a.mac("response", AgentAPIVersion, identifier, strconv.Itoa(status), digest(body))
}

func (a *ProtocolAuthenticator) mac(fields ...string) []byte {
	mac := hmac.New(sha256.New, a.key)
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(field))
	}
	return mac.Sum(nil)
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encodeMAC(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func decodeMACHeader(header http.Header) ([]byte, error) {
	value, err := oneHeader(header, protocolHeaderAuth)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("invalid local protocol authenticator")
	}
	return decoded, nil
}

func oneHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("header %s must appear exactly once", name)
	}
	return values[0], nil
}

type authenticatedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	large  bool
}

func (w *authenticatedResponse) Header() http.Header { return w.header }

func (w *authenticatedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *authenticatedResponse) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(value) > ProtocolMaxMessage {
		w.large = true
		return len(value), nil
	}
	return w.body.Write(value)
}

// AuthenticatedAgentHandler authenticates an exact request before dispatch and
// authenticates every response to an accepted request, including errors.
func AuthenticatedAgentHandler(authenticator *ProtocolAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(io.LimitReader(request.Body, ProtocolMaxMessage+1))
		if err != nil || len(body) > ProtocolMaxMessage {
			http.Error(response, "local protocol request rejected", http.StatusRequestEntityTooLarge)
			return
		}
		requestID, err := authenticator.VerifyRequest(request.Method, request.URL.Path, request.Header, body)
		if err != nil {
			http.Error(response, "local protocol request rejected", http.StatusUnauthorized)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		buffered := &authenticatedResponse{header: make(http.Header)}
		next.ServeHTTP(buffered, request)
		status := buffered.status
		if status == 0 {
			status = http.StatusOK
		}
		responseBody := buffered.body.Bytes()
		if buffered.large {
			status = http.StatusInternalServerError
			responseBody = []byte("local protocol response rejected\n")
		}
		for name, values := range buffered.header {
			if name == protocolHeaderVersion || name == protocolHeaderID || name == protocolHeaderTime || name == protocolHeaderAuth || name == "Content-Length" {
				continue
			}
			for _, value := range values {
				response.Header().Add(name, value)
			}
		}
		for name, values := range authenticator.SignResponse(requestID, status, responseBody) {
			response.Header()[name] = values
		}
		response.WriteHeader(status)
		_, _ = response.Write(responseBody)
	})
}
