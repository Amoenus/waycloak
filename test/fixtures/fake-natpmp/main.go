// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/binary"
	"flag"
	"log"
	"net"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:5351", "UDP listen address")
	flag.Parse()
	connection, err := net.ListenPacket("udp4", *listenAddress)
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()
	buffer := make([]byte, 32)
	cycle := 0
	for {
		length, peer, err := connection.ReadFrom(buffer)
		if err != nil {
			log.Fatal(err)
		}
		request := buffer[:length]
		if length == 2 && request[0] == 0 && request[1] == 0 {
			response := make([]byte, 12)
			response[1] = 0x80
			copy(response[8:12], net.IPv4(203, 0, 113, 10).To4())
			_, _ = connection.WriteTo(response, peer)
			continue
		}
		if length != 12 || request[0] != 0 || request[1] < 1 || request[1] > 2 {
			continue
		}
		if request[1] == 2 {
			cycle++
		}
		port := uint16(42000)
		if cycle > 1 {
			port = 42001
		}
		if suggested := binary.BigEndian.Uint16(request[6:8]); suggested != 0 && cycle == 1 {
			port = suggested
		}
		response := make([]byte, 16)
		response[1] = request[1] | 0x80
		copy(response[8:10], request[4:6])
		binary.BigEndian.PutUint16(response[10:12], port)
		if binary.BigEndian.Uint32(request[8:12]) != 0 {
			binary.BigEndian.PutUint32(response[12:16], 4)
		}
		_, _ = connection.WriteTo(response, peer)
	}
}
