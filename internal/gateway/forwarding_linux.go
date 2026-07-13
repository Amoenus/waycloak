// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
)

type linuxForwarding struct{}

func NewForwarding() Forwarding { return linuxForwarding{} }

func (linuxForwarding) InstallLockdown(_ context.Context, desired DesiredState) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	return ensureForwardingLockdown(desired.GatewayName)
}

func (linuxForwarding) Reconcile(_ context.Context, desired DesiredState) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if err := ensureIPv4Forwarding(); err != nil {
		return err
	}
	overlay, err := readyLink(OverlayInterfaceName(desired.GatewayName))
	if err != nil {
		return fmt.Errorf("resolve gateway overlay for forwarding: %w", err)
	}
	tunnel, err := readyLink(desired.TunnelInterface)
	if err != nil {
		return fmt.Errorf("resolve VPN tunnel for forwarding: %w", err)
	}
	if overlay.Attrs().Index == tunnel.Attrs().Index {
		return errors.New("gateway overlay and VPN tunnel interfaces must differ")
	}
	return replaceForwardingPolicy(desired, overlay.Attrs().Name, tunnel.Attrs().Name)
}

func ensureIPv4Forwarding() error {
	const path = "/proc/sys/net/ipv4/ip_forward"
	value, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read IPv4 forwarding state: %w", err)
	}
	if strings.TrimSpace(string(value)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1\n"), 0); err != nil {
		return fmt.Errorf("enable IPv4 forwarding: %w", err)
	}
	return nil
}

func readyLink(name string) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, err
	}
	if link.Attrs().Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("interface %q is down", name)
	}
	return link, nil
}

func replaceForwardingPolicy(desired DesiredState, overlayName, tunnelName string) error {
	conn := &nftables.Conn{}
	tableName := forwardingTableName(desired.GatewayName)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return fmt.Errorf("list gateway forwarding tables: %w", err)
	}
	for _, existing := range tables {
		if existing.Name == tableName {
			conn.DelTable(existing)
		}
	}
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: tableName})
	drop := nftables.ChainPolicyDrop
	forward := conn.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &drop})
	marker := []byte("waycloak-gateway:" + desired.GatewayName)
	prefix := netip.MustParsePrefix(desired.OverlayCIDR).Masked()
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: marker, Exprs: append([]expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED), Xor: binaryutil.NativeEndian.PutUint32(0)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
	}, append(destinationPrefixExpressions(prefix),
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(tunnelName)},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(overlayName)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	)...)})
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, UserData: marker, Exprs: append(sourcePrefixExpressions(prefix),
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(overlayName)},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(tunnelName)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	)})
	postrouting := conn.AddChain(&nftables.Chain{Table: table, Name: "postrouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	conn.AddRule(&nftables.Rule{Table: table, Chain: postrouting, UserData: marker, Exprs: append(sourcePrefixExpressions(prefix),
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: fixedInterfaceName(tunnelName)},
		&expr.Masq{},
	)})
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("replace gateway forwarding policy: %w", err)
	}
	return nil
}

func ensureForwardingLockdown(gatewayName string) error {
	conn := &nftables.Conn{}
	tableName := forwardingTableName(gatewayName)
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return fmt.Errorf("list gateway forwarding tables: %w", err)
	}
	var existing *nftables.Table
	for _, table := range tables {
		if table.Name == tableName {
			existing = table
			break
		}
	}
	if existing != nil {
		chains, listErr := conn.ListChainsOfTableFamily(nftables.TableFamilyIPv4)
		if listErr != nil {
			return fmt.Errorf("list gateway forwarding chains: %w", listErr)
		}
		for _, chain := range chains {
			if chain.Table != nil && chain.Table.Name == tableName && chain.Name == "forward" && chain.Policy != nil && *chain.Policy == nftables.ChainPolicyDrop {
				return nil
			}
		}
		conn.DelTable(existing)
	}
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: tableName})
	drop := nftables.ChainPolicyDrop
	conn.AddChain(&nftables.Chain{Table: table, Name: "forward", Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &drop})
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("install gateway forwarding lockdown: %w", err)
	}
	return nil
}

func sourcePrefixExpressions(prefix netip.Prefix) []expr.Any {
	return addressPrefixExpressions(prefix, 12)
}

func destinationPrefixExpressions(prefix netip.Prefix) []expr.Any {
	return addressPrefixExpressions(prefix, 16)
}

func addressPrefixExpressions(prefix netip.Prefix, offset uint32) []expr.Any {
	mask := net.CIDRMask(prefix.Bits(), 32)
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: prefix.Addr().AsSlice()},
	}
}

func fixedInterfaceName(value string) []byte {
	result := make([]byte, 16)
	copy(result, value)
	return result
}

func forwardingTableName(gatewayName string) string {
	return "waycloak_gw_" + strings.TrimPrefix(OverlayInterfaceName(gatewayName), "wcg")
}
