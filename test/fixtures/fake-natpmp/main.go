// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/binary"
	"flag"
	"log"
	"net"
	"net/netip"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:5351", "UDP listen address")
	externalAddressText := flag.String("external-address", "203.0.113.10", "external IPv4 address returned to clients")
	initialPort := flag.Uint("initial-port", 42000, "external port returned before rotation")
	rotatedPort := flag.Uint("rotated-port", 42001, "external port returned after rotation")
	rotateAfterTCP := flag.Int("rotate-after-tcp", 1, "rotate after this many TCP mapping requests")
	flag.Parse()
	externalAddress, err := netip.ParseAddr(*externalAddressText)
	if err != nil || !externalAddress.Is4() || *initialPort == 0 || *initialPort > 65535 || *rotatedPort == 0 || *rotatedPort > 65535 || *rotateAfterTCP < 1 {
		log.Fatal("invalid fake NAT-PMP configuration")
	}
	connection, err := net.ListenPacket("udp4", *listenAddress)
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()
	buffer := make([]byte, 128)
	cycle := 0
	for {
		length, peer, err := connection.ReadFrom(buffer)
		if err != nil {
			log.Fatal(err)
		}
		request := buffer[:length]
		if length >= 24 && request[0] == 2 {
			opcode := request[1] & 0x7f
			if opcode == 0 && length == 24 {
				response := make([]byte, 24)
				response[0] = 2
				response[1] = 0x80
				_, _ = connection.WriteTo(response, peer)
				log.Printf("PCP announce from %s", peer)
				continue
			}
			if opcode == 1 && length == 60 && (request[36] == 6 || request[36] == 17) {
				if request[36] == 6 {
					cycle++
				}
				port := uint16(*initialPort)
				if cycle > *rotateAfterTCP {
					port = uint16(*rotatedPort)
				}
				response := make([]byte, 60)
				response[0] = 2
				response[1] = 0x81
				copy(response[4:8], request[4:8])
				copy(response[24:42], request[24:42])
				binary.BigEndian.PutUint16(response[42:44], port)
				response[54] = 0xff
				response[55] = 0xff
				copy(response[56:60], externalAddress.AsSlice())
				_, _ = connection.WriteTo(response, peer)
				log.Printf("PCP mapping protocol=%d internal=%d suggested=%d external=%d lifetime=%d peer=%s", request[36], binary.BigEndian.Uint16(request[40:42]), binary.BigEndian.Uint16(request[42:44]), port, binary.BigEndian.Uint32(request[4:8]), peer)
				continue
			}
			continue
		}
		if length == 2 && request[0] == 0 && request[1] == 0 {
			response := make([]byte, 12)
			response[1] = 0x80
			copy(response[8:12], externalAddress.AsSlice())
			_, _ = connection.WriteTo(response, peer)
			log.Printf("external address request from %s", peer)
			continue
		}
		if length != 12 || request[0] != 0 || request[1] < 1 || request[1] > 2 {
			continue
		}
		if request[1] == 2 {
			cycle++
		}
		port := uint16(*initialPort)
		if cycle > *rotateAfterTCP {
			port = uint16(*rotatedPort)
		}
		response := make([]byte, 16)
		response[1] = request[1] | 0x80
		copy(response[8:10], request[4:6])
		binary.BigEndian.PutUint16(response[10:12], port)
		if binary.BigEndian.Uint32(request[8:12]) != 0 {
			binary.BigEndian.PutUint32(response[12:16], 4)
		}
		_, _ = connection.WriteTo(response, peer)
		log.Printf("mapping opcode=%d internal=%d suggested=%d external=%d lifetime=%d peer=%s", request[1], binary.BigEndian.Uint16(request[4:6]), binary.BigEndian.Uint16(request[6:8]), port, binary.BigEndian.Uint32(request[8:12]), peer)
	}
}
