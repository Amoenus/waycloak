// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("expected serve-tracker, dht-nodes, or listen-port")
	}
	switch os.Args[1] {
	case "serve-tracker":
		serveTracker(os.Args[2:])
	case "dht-nodes":
		value, err := qbitValue(os.Args[2:], "dht_nodes")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(value)
	case "listen-port":
		value, err := qbitValue(os.Args[2:], "listen_port")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(value)
	default:
		log.Fatal("unknown command")
	}
}

func serveTracker(args []string) {
	flags := flag.NewFlagSet("serve-tracker", flag.ExitOnError)
	listen := flags.String("listen", "127.0.0.1:18081", "tracker listen address")
	output := flags.String("output", "/tmp/tracker-port", "atomic observed-port output")
	_ = flags.Parse(args)
	server := &http.Server{Addr: *listen, Handler: trackerHandler(*output), ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func trackerHandler(output string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/announce", func(response http.ResponseWriter, request *http.Request) {
		port, err := strconv.ParseUint(request.URL.Query().Get("port"), 10, 16)
		if err != nil || port == 0 {
			http.Error(response, "invalid announced port", http.StatusBadRequest)
			return
		}
		address, err := netip.ParseAddr(request.URL.Query().Get("ip"))
		if err != nil || !address.Is4() || !address.IsGlobalUnicast() {
			http.Error(response, "invalid announced address", http.StatusBadRequest)
			return
		}
		addressHash := sha256.Sum256([]byte(address.String()))
		temporary := output + ".tmp"
		if err := os.WriteFile(temporary, []byte(fmt.Sprintf("%d\n%x\n", port, addressHash)), 0o600); err != nil {
			http.Error(response, "store announced port", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(temporary, output); err != nil {
			http.Error(response, "publish announced port", http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = io.WriteString(response, "d8:intervali60e5:peers0:e")
	})
	return mux
}

func qbitValue(args []string, field string) (int, error) {
	flags := flag.NewFlagSet(field, flag.ContinueOnError)
	apiKeyFile := flags.String("api-key-file", "/secrets/api-key", "qBitTorrent API key file")
	endpoint := flags.String("endpoint", "http://127.0.0.1:8080", "qBitTorrent Web API endpoint")
	if err := flags.Parse(args); err != nil {
		return 0, err
	}
	key, err := os.ReadFile(filepath.Clean(*apiKeyFile))
	if err != nil {
		return 0, errors.New("read qBitTorrent API key")
	}
	path := "/api/v2/transfer/info"
	if field == "listen_port" {
		path = "/api/v2/app/preferences"
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(*endpoint, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(key)))
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("qBitTorrent API returned HTTP %d", response.StatusCode)
	}
	var document map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return 0, errors.New("decode qBitTorrent API response")
	}
	raw, exists := document[field]
	if !exists {
		return 0, fmt.Errorf("qBitTorrent response omitted %s", field)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 || field == "listen_port" && value > 65535 {
		return 0, fmt.Errorf("qBitTorrent %s is invalid", field)
	}
	return value, nil
}
