// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	waycni "github.com/Amoenus/waycloak/internal/cni"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const routeLabel = "networking.waycloak.io/egress-route"

func main() {
	var socketPath string
	var captureFile string
	var authKeyFile string
	flag.StringVar(&socketPath, "socket", waycni.DefaultAgentSocket, "host-mounted Unix socket path")
	flag.StringVar(&captureFile, "capture-file", "/run/waycloak/capture-count", "packet capture count output")
	flag.StringVar(&authKeyFile, "auth-key-file", waycni.DefaultAgentKeyFile, "root-only local protocol authentication key")
	flag.Parse()
	if err := run(socketPath, captureFile, authKeyFile); err != nil {
		log.Fatal(err)
	}
}

func run(socketPath, captureFile, authKeyFile string) error {
	if filepath.Dir(socketPath) != filepath.Dir(authKeyFile) {
		return errors.New("local protocol socket and authentication key must share one protected directory")
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := startPacketCapture(ctx, captureFile); err != nil {
		return fmt.Errorf("start direct-egress packet capture: %w", err)
	}
	key, err := waycni.RotateProtocolKey(authKeyFile)
	if err != nil {
		return fmt.Errorf("rotate local protocol authentication: %w", err)
	}
	authenticator, err := waycni.NewProtocolAuthenticator(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return err
	}
	server := &http.Server{
		Handler:           waycni.RootPeerOnlyHandler(waycni.AuthenticatedAgentHandler(authenticator, handler(client))),
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		ConnContext:       waycni.LocalPeerContext,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func handler(client kubernetes.Interface) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cni-node/v1/status", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeAgentError(response, http.StatusMethodNotAllowed, waycni.AgentErrorInvalidRequest, false, "operation method is invalid")
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	for _, path := range []string{"/cni-node/v1/resolve", "/cni-node/v1/binding", "/cni-node/v1/check", "/cni-node/v1/withdraw"} {
		path := path
		mux.HandleFunc(path, func(response http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodPost {
				writeAgentError(response, http.StatusMethodNotAllowed, waycni.AgentErrorInvalidRequest, false, "operation method is invalid")
				return
			}
			input, err := decodeRequest(response, request)
			if err != nil {
				writeAgentError(response, http.StatusBadRequest, waycni.AgentErrorInvalidRequest, false, "request schema is invalid")
				return
			}
			pod, err := client.CoreV1().Pods(input.Pod.Namespace).Get(request.Context(), input.Pod.Name, metav1.GetOptions{})
			if err != nil || string(pod.UID) != input.Pod.UID {
				writeAgentError(response, http.StatusNotFound, waycni.AgentErrorPodIdentityMismatch, false, "exact Pod identity is unavailable")
				return
			}
			enrolled := pod.Labels[routeLabel] != ""
			terminating := pod.DeletionTimestamp != nil
			switch path {
			case "/cni-node/v1/resolve":
				writeJSON(response, waycni.AgentResponse{APIVersion: waycni.AgentAPIVersion, Resolution: &waycni.Resolution{PodUID: string(pod.UID), Enrolled: enrolled, Terminating: terminating}})
			case "/cni-node/v1/binding":
				if !enrolled {
					writeAgentError(response, http.StatusNotFound, waycni.AgentErrorNotEnrolled, false, "Pod is not enrolled")
					return
				}
				probeDirectEgress(input.Pod.NetNS)
				writeAgentError(response, http.StatusServiceUnavailable, waycni.AgentErrorBindingNotReady, true, "UID binding is not ready")
			case "/cni-node/v1/check":
				if !enrolled {
					writeAgentError(response, http.StatusNotFound, waycni.AgentErrorNotEnrolled, false, "Pod is not enrolled")
					return
				}
				response.WriteHeader(http.StatusNoContent)
			case "/cni-node/v1/withdraw":
				response.WriteHeader(http.StatusNoContent)
			}
		})
	}
	return mux
}

func decodeRequest(response http.ResponseWriter, request *http.Request) (waycni.AgentRequest, error) {
	var input waycni.AgentRequest
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return input, errors.New("trailing JSON")
	}
	if input.APIVersion != waycni.AgentAPIVersion {
		return input, errors.New("unsupported protocol version")
	}
	if err := input.Pod.Validate(); err != nil {
		return input, err
	}
	return input, nil
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeAgentError(response http.ResponseWriter, status int, code string, retryable bool, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	writeJSON(response, waycni.AgentResponse{
		APIVersion: waycni.AgentAPIVersion,
		Error:      &waycni.AgentError{Code: code, Retryable: retryable, Message: message},
	})
}
