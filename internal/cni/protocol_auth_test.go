// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package cni

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProtocolAuthenticationRejectsTamperingStalenessAndReplay(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, ProtocolKeySize)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	signer := protocolTestAuthenticator(t, key, now, 0x11)
	verifier := protocolTestAuthenticator(t, key, now, 0x22)
	body := []byte(`{"apiVersion":"networking.waycloak.io/cni-node/v1"}`)
	header, err := signer.SignRequest(http.MethodPost, resolvePath, body)
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := verifier.VerifyRequest(http.MethodPost, resolvePath, header, body)
	if err != nil || requestID == "" {
		t.Fatalf("valid request rejected: id=%q err=%v", requestID, err)
	}
	if _, err := verifier.VerifyRequest(http.MethodPost, resolvePath, header, body); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("replayed request error = %v", err)
	}

	tamperedVerifier := protocolTestAuthenticator(t, key, now, 0x23)
	if _, err := tamperedVerifier.VerifyRequest(http.MethodPost, resolvePath, header, append(body, ' ')); err == nil {
		t.Fatal("tampered request body was authenticated")
	}
	if _, err := tamperedVerifier.VerifyRequest(http.MethodPost, bindingPath, header, body); err == nil {
		t.Fatal("request replayed against a different operation was authenticated")
	}

	wrongKey := protocolTestAuthenticator(t, bytes.Repeat([]byte{0x24}, ProtocolKeySize), now, 0x24)
	if _, err := wrongKey.VerifyRequest(http.MethodPost, resolvePath, header, body); err == nil {
		t.Fatal("request signed by a foreign key was authenticated")
	}
	duplicateHeader := header.Clone()
	duplicateHeader.Add(protocolHeaderID, "duplicate")
	if _, err := tamperedVerifier.VerifyRequest(http.MethodPost, resolvePath, duplicateHeader, body); err == nil {
		t.Fatal("duplicate authentication header was accepted")
	}

	staleSigner := protocolTestAuthenticator(t, key, now.Add(-ProtocolMaxClockSkew-time.Second), 0x25)
	staleHeader, err := staleSigner.SignRequest(http.MethodPost, resolvePath, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamperedVerifier.VerifyRequest(http.MethodPost, resolvePath, staleHeader, body); err == nil || !strings.Contains(err.Error(), "freshness") {
		t.Fatalf("stale request error = %v", err)
	}
	futureSigner := protocolTestAuthenticator(t, key, now.Add(ProtocolMaxClockSkew+time.Second), 0x26)
	futureHeader, err := futureSigner.SignRequest(http.MethodPost, resolvePath, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamperedVerifier.VerifyRequest(http.MethodPost, resolvePath, futureHeader, body); err == nil || !strings.Contains(err.Error(), "freshness") {
		t.Fatalf("future request error = %v", err)
	}
}

func TestProtocolAuthenticationBindsResponseIdentityStatusAndBody(t *testing.T) {
	key := bytes.Repeat([]byte{0x51}, ProtocolKeySize)
	authenticator := protocolTestAuthenticator(t, key, time.Now().UTC(), 0x52)
	body := []byte(`{"apiVersion":"networking.waycloak.io/cni-node/v1"}`)
	header := authenticator.SignResponse("request-identity", http.StatusServiceUnavailable, body)
	if err := authenticator.VerifyResponse("request-identity", http.StatusServiceUnavailable, header, body); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() error{
		"request identity": func() error {
			return authenticator.VerifyResponse("other", http.StatusServiceUnavailable, header, body)
		},
		"status": func() error { return authenticator.VerifyResponse("request-identity", http.StatusOK, header, body) },
		"body": func() error {
			return authenticator.VerifyResponse("request-identity", http.StatusServiceUnavailable, header, append(body, ' '))
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); err == nil {
				t.Fatal("tampered response was authenticated")
			}
		})
	}
}

func TestAuthenticatedAgentHandlerSignsAcceptedErrorsAndRejectsReplay(t *testing.T) {
	key := bytes.Repeat([]byte{0x61}, ProtocolKeySize)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	clientAuth := protocolTestAuthenticator(t, key, now, 0x62)
	serverAuth := protocolTestAuthenticator(t, key, now, 0x63)
	body := []byte(`{"apiVersion":"networking.waycloak.io/cni-node/v1"}`)
	header, err := clientAuth.SignRequest(http.MethodPost, bindingPath, body)
	if err != nil {
		t.Fatal(err)
	}
	handler := AuthenticatedAgentHandler(serverAuth, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Error(response, "binding unavailable", http.StatusServiceUnavailable)
	}))
	request := httptest.NewRequest(http.MethodPost, bindingPath, bytes.NewReader(body))
	request.Header = header.Clone()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	requestID := header.Get(protocolHeaderID)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	if err := clientAuth.VerifyResponse(requestID, response.Code, response.Header(), response.Body.Bytes()); err != nil {
		t.Fatalf("accepted error response was not authenticated: %v", err)
	}

	replay := httptest.NewRequest(http.MethodPost, bindingPath, bytes.NewReader(body))
	replay.Header = header.Clone()
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", replayResponse.Code)
	}
	if replayResponse.Header().Get(protocolHeaderAuth) != "" {
		t.Fatal("unauthenticated request received an authenticated response oracle")
	}
}

func TestAuthenticatedAgentHandlerRejectsOversizedInputBeforeDispatch(t *testing.T) {
	dispatched := false
	authenticator := protocolTestAuthenticator(t, bytes.Repeat([]byte{0x71}, ProtocolKeySize), time.Now().UTC(), 0x72)
	handler := AuthenticatedAgentHandler(authenticator, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched = true }))
	request := httptest.NewRequest(http.MethodPost, resolvePath, bytes.NewReader(make([]byte, ProtocolMaxMessage+1)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || dispatched {
		t.Fatalf("oversized request status=%d dispatched=%t", response.Code, dispatched)
	}
}

func TestProtocolReplayWindowIsBounded(t *testing.T) {
	key := bytes.Repeat([]byte{0x73}, ProtocolKeySize)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	verifier := protocolTestAuthenticator(t, key, now, 0x74)
	verifier.maxReplay = 1
	body := []byte(`{}`)
	first, err := protocolTestAuthenticator(t, key, now, 0x75).SignRequest(http.MethodGet, statusPath, body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := protocolTestAuthenticator(t, key, now, 0x76).SignRequest(http.MethodGet, statusPath, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyRequest(http.MethodGet, statusPath, first, body); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyRequest(http.MethodGet, statusPath, second, body); err == nil || !strings.Contains(err.Error(), "window is full") {
		t.Fatalf("bounded replay error = %v", err)
	}
}

func TestDecodeAgentResponseRequiresVersionAndStrictSingleDocument(t *testing.T) {
	valid := []byte(`{"apiVersion":"networking.waycloak.io/cni-node/v1","error":{"code":"BindingNotReady","retryable":true,"message":"UID binding is not ready"}}`)
	var response AgentResponse
	if err := decodeAgentResponse(valid, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != AgentErrorBindingNotReady || !response.Error.Retryable {
		t.Fatalf("decoded stable error = %#v", response.Error)
	}

	for name, body := range map[string][]byte{
		"wrong version": []byte(`{"apiVersion":"networking.waycloak.io/cni-node/v2"}`),
		"unknown field": []byte(`{"apiVersion":"networking.waycloak.io/cni-node/v1","foreign":true}`),
		"trailing JSON": append(append([]byte(nil), valid...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			var decoded AgentResponse
			if err := decodeAgentResponse(body, &decoded); err == nil {
				t.Fatal("malformed local protocol response was accepted")
			}
		})
	}

	var trailing AgentResponse
	if err := decodeAgentResponse(append(append([]byte(nil), valid...), '\n'), &trailing); err != nil {
		t.Fatalf("trailing whitespace should remain valid JSON: %v", err)
	}
}

func TestReadAgentResponseBodyDistinguishesIOFailureAndSizeLimit(t *testing.T) {
	sentinel := errors.New("connection reset")
	if _, err := readAgentResponseBody(io.MultiReader(bytes.NewReader([]byte("partial")), failingProtocolReader{err: sentinel})); !errors.Is(err, sentinel) {
		t.Fatalf("read error = %v", err)
	}
	if _, err := readAgentResponseBody(bytes.NewReader(make([]byte, ProtocolMaxMessage+1))); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestAgentStatusErrorRetainsAuthenticatedMessage(t *testing.T) {
	err := (&agentStatusError{HTTPStatus: http.StatusNotFound, Reason: AgentErrorNotEnrolled, Message: "Pod is not enrolled"}).Error()
	if !strings.Contains(err, AgentErrorNotEnrolled) || !strings.Contains(err, "Pod is not enrolled") {
		t.Fatalf("status error = %q", err)
	}
}

type failingProtocolReader struct{ err error }

func (r failingProtocolReader) Read([]byte) (int, error) { return 0, r.err }

func protocolTestAuthenticator(t *testing.T, key []byte, now time.Time, randomByte byte) *ProtocolAuthenticator {
	t.Helper()
	authenticator, err := NewProtocolAuthenticator(key)
	if err != nil {
		t.Fatal(err)
	}
	authenticator.now = func() time.Time { return now }
	authenticator.random = bytes.NewReader(bytes.Repeat([]byte{randomByte}, 16*16))
	return authenticator
}
