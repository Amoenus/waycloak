// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package gatewaydataplane

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Amoenus/waycloak/internal/telemetry"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	maximumDNSMessage       = 65535
	dnsExchangeTimeout      = 3 * time.Second
	dnsProbeAttemptTimeout  = time.Second
	dnsProbeMaximumAttempts = 3
)

type DNSProber interface {
	Probe(context.Context) error
}

// DNSProbe independently verifies the semantic DNS behavior exposed by the
// gateway sidecar. The sidecar process being alive is deliberately insufficient
// for readiness: all required cluster and VPN-routed paths must answer.
type DNSProbe struct {
	config     Config
	ids        atomic.Uint32
	probeCheck func(context.Context, string, string, dnsmessage.Type) error
	telemetry  telemetry.Recorder
}

type dnsIdentity struct {
	id       uint16
	question dnsmessage.Question
}

func NewDNSProber(config Config) DNSProber {
	return &DNSProbe{config: config}
}

func NewDNSProberWithTelemetry(config Config, recorder telemetry.Recorder) DNSProber {
	return &DNSProbe{config: config, telemetry: recorder}
}

func parseDNSMessage(message []byte) (dnsmessage.Header, []dnsmessage.Question, error) {
	if len(message) < 12 || len(message) > maximumDNSMessage {
		return dnsmessage.Header{}, nil, errors.New("DNS message size is invalid")
	}
	var parsed dnsmessage.Message
	if err := parsed.Unpack(message); err != nil {
		return dnsmessage.Header{}, nil, err
	}
	return parsed.Header, parsed.Questions, nil
}

func validateDNSResponse(response []byte, identity dnsIdentity) (dnsmessage.Header, error) {
	header, questions, err := parseDNSMessage(response)
	if err != nil || !header.Response || header.ID != identity.id || len(questions) != 1 || questions[0] != identity.question {
		return dnsmessage.Header{}, errors.New("DNS response does not match the exact query")
	}
	return header, nil
}

func readDNSFrame(reader io.Reader) ([]byte, error) {
	var length [2]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(length[:]))
	if size < 12 || size > maximumDNSMessage {
		return nil, errors.New("DNS TCP frame size is invalid")
	}
	message := make([]byte, size)
	_, err := io.ReadFull(reader, message)
	return message, err
}

func writeDNSFrame(writer io.Writer, message []byte) error {
	if len(message) < 12 || len(message) > maximumDNSMessage {
		return errors.New("DNS TCP frame size is invalid")
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(message)))
	if _, err := writer.Write(length[:]); err != nil {
		return err
	}
	_, err := writer.Write(message)
	return err
}

func (probe *DNSProbe) Probe(ctx context.Context) error {
	clusterName := "kubernetes.default.svc." + probe.config.ClusterDomain + "."
	checks := []struct {
		network, name string
		typeCode      dnsmessage.Type
	}{
		{"udp4", clusterName, dnsmessage.TypeA},
		{"tcp4", clusterName, dnsmessage.TypeA},
		{"udp4", "example.com.", dnsmessage.TypeA},
		{"tcp4", "example.com.", dnsmessage.TypeA},
		{"udp4", "example.com.", dnsmessage.TypeAAAA},
		{"tcp4", "example.com.", dnsmessage.TypeAAAA},
	}
	check := probe.probe
	if probe.probeCheck != nil {
		check = probe.probeCheck
	}
	for _, candidate := range checks {
		started := time.Now()
		err := check(ctx, candidate.network, candidate.name, candidate.typeCode)
		phase := "external"
		if candidate.name == clusterName {
			phase = "cluster"
		}
		transport := "tcp"
		if candidate.network == "udp4" {
			transport = "udp"
		}
		telemetry.Emit(probe.telemetry, ctx, telemetry.Event{Component: "gateway_agent", Operation: "dns_probe", Result: telemetry.Result(err), Phase: phase, Transport: transport,
			QueryType: strings.ToLower(dnsTypeName(candidate.typeCode)), FailureClass: telemetry.FailureClass(err), Duration: time.Since(started)})
		if err != nil {
			return err
		}
	}
	return nil
}

func (probe *DNSProbe) probe(ctx context.Context, network, name string, typeCode dnsmessage.Type) error {
	started := time.Now()
	request, identity, err := probe.probeRequest(name, typeCode)
	if err != nil {
		return dnsProbeFailure(network, name, typeCode, "request_build", started, "", err)
	}
	response, attempts, err := exchangeDNSProbe(ctx, network, dnsListenAddress(probe.config), request)
	if err != nil {
		return dnsProbeFailure(network, name, typeCode, "exchange", started, "", fmt.Errorf("attempts=%d: %w", attempts, err))
	}
	header, err := validateDNSResponse(response, identity)
	if err != nil {
		return dnsProbeFailure(network, name, typeCode, "response_validation", started, "", err)
	}
	if header.RCode != dnsmessage.RCodeSuccess {
		return dnsProbeFailure(network, name, typeCode, "rcode", started, dnsRCodeName(header.RCode), nil)
	}
	if network == "udp4" && header.Truncated {
		response, attempts, err = exchangeDNSProbe(ctx, "tcp4", dnsListenAddress(probe.config), request)
		if err != nil {
			return dnsProbeFailure("tcp4", name, typeCode, "truncated_retry_exchange", started, "", fmt.Errorf("attempts=%d: %w", attempts, err))
		}
		header, err = validateDNSResponse(response, identity)
		if err != nil {
			return dnsProbeFailure("tcp4", name, typeCode, "truncated_retry_validation", started, "", err)
		}
		if header.Truncated {
			return dnsProbeFailure("tcp4", name, typeCode, "truncated_retry_validation", started, "", errors.New("response remained truncated"))
		}
		if header.RCode != dnsmessage.RCodeSuccess {
			return dnsProbeFailure("tcp4", name, typeCode, "truncated_retry_rcode", started, dnsRCodeName(header.RCode), nil)
		}
	}
	return nil
}

func exchangeDNSProbe(ctx context.Context, network, address string, request []byte) ([]byte, int, error) {
	probeCtx, cancelProbe := context.WithTimeout(ctx, dnsExchangeTimeout)
	defer cancelProbe()
	var lastErr error
	for attempt := 1; attempt <= dnsProbeMaximumAttempts; attempt++ {
		attemptCtx, cancelAttempt := context.WithTimeout(probeCtx, dnsProbeAttemptTimeout)
		var response []byte
		if network == "udp4" {
			response, lastErr = exchangeDNSUDP(attemptCtx, address, request)
		} else {
			response, lastErr = exchangeDNSTCP(attemptCtx, address, request)
		}
		cancelAttempt()
		if lastErr == nil {
			return response, attempt, nil
		}
		if probeCtx.Err() != nil {
			return nil, attempt, lastErr
		}
	}
	return nil, dnsProbeMaximumAttempts, lastErr
}

func dnsProbeFailure(network, name string, typeCode dnsmessage.Type, phase string, started time.Time, rcode string, err error) error {
	message := fmt.Sprintf("dns_probe network=%s qname=%q qtype=%s phase=%s latency=%s", network, name, dnsTypeName(typeCode), phase, time.Since(started).Round(time.Microsecond))
	if rcode != "" {
		message += " rcode=" + rcode
	}
	if err != nil {
		return fmt.Errorf("%s: %w", message, err)
	}
	return errors.New(message)
}

func dnsTypeName(typeCode dnsmessage.Type) string {
	switch typeCode {
	case dnsmessage.TypeA:
		return "A"
	case dnsmessage.TypeAAAA:
		return "AAAA"
	default:
		return fmt.Sprintf("TYPE%d", typeCode)
	}
}

func dnsRCodeName(rcode dnsmessage.RCode) string {
	switch rcode {
	case dnsmessage.RCodeSuccess:
		return "NoError"
	case dnsmessage.RCodeFormatError:
		return "FormErr"
	case dnsmessage.RCodeServerFailure:
		return "ServFail"
	case dnsmessage.RCodeNameError:
		return "NXDomain"
	case dnsmessage.RCodeNotImplemented:
		return "NotImp"
	case dnsmessage.RCodeRefused:
		return "Refused"
	default:
		return fmt.Sprintf("RCODE%d", rcode)
	}
}

func (probe *DNSProbe) probeRequest(name string, typeCode dnsmessage.Type) ([]byte, dnsIdentity, error) {
	dnsName, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, dnsIdentity{}, err
	}
	id := uint16(probe.ids.Add(1))
	question := dnsmessage.Question{Name: dnsName, Type: typeCode, Class: dnsmessage.ClassINET}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, dnsIdentity{}, err
	}
	if err := builder.Question(question); err != nil {
		return nil, dnsIdentity{}, err
	}
	if err := builder.StartAdditionals(); err != nil {
		return nil, dnsIdentity{}, err
	}
	root, _ := dnsmessage.NewName(".")
	if err := builder.OPTResource(dnsmessage.ResourceHeader{Name: root, Type: dnsmessage.TypeOPT, Class: 1232}, dnsmessage.OPTResource{}); err != nil {
		return nil, dnsIdentity{}, err
	}
	request, err := builder.Finish()
	return request, dnsIdentity{id: id, question: question}, err
}

func exchangeDNSUDP(ctx context.Context, address string, request []byte) ([]byte, error) {
	connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "udp4", address)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	setDNSDeadline(connection, ctx, dnsExchangeTimeout)
	if _, err := connection.Write(request); err != nil {
		return nil, err
	}
	response := make([]byte, maximumDNSMessage)
	count, err := connection.Read(response)
	return response[:count], err
}

func exchangeDNSTCP(ctx context.Context, address string, request []byte) ([]byte, error) {
	connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	setDNSDeadline(connection, ctx, dnsExchangeTimeout)
	if err := writeDNSFrame(connection, request); err != nil {
		return nil, err
	}
	return readDNSFrame(connection)
}

func setDNSDeadline(connection net.Conn, ctx context.Context, maximum time.Duration) {
	deadline := time.Now().Add(maximum)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
}
