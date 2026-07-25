// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	waycni "github.com/Amoenus/waycloak/internal/cni"
	"github.com/Amoenus/waycloak/internal/dataplane"
	"github.com/containernetworking/cni/pkg/skel"
	cnitypes "github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/version"
)

func main() {
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add: cmdAdd, Del: cmdDel, Check: cmdCheck, GC: cmdGC, Status: cmdStatus,
	}, version.PluginSupports("0.4.0", "1.0.0", "1.1.0"), "Waycloak chained CNI feasibility plugin")
}

func cmdAdd(args *skel.CmdArgs) error {
	parsed, err := waycni.Parse(args.StdinData, args.ContainerID, args.Netns, args.IfName, args.Args, true, true)
	if err != nil {
		return err
	}
	plugin := waycni.NewPlugin(parsed, waycni.LinuxEnforcer{Backend: dataplane.NewBackend()})
	if err := plugin.Add(context.Background(), parsed.Request); err != nil {
		return err
	}
	return cnitypes.PrintResult(parsed.Conf.PrevResult, parsed.Conf.CNIVersion)
}

func cmdCheck(args *skel.CmdArgs) error {
	parsed, err := waycni.Parse(args.StdinData, args.ContainerID, args.Netns, args.IfName, args.Args, true, true)
	if err != nil {
		return err
	}
	return waycni.NewPlugin(parsed, waycni.LinuxEnforcer{Backend: dataplane.NewBackend()}).Check(context.Background(), parsed.Request)
}

func cmdDel(args *skel.CmdArgs) error {
	parsed, err := waycni.Parse(args.StdinData, args.ContainerID, args.Netns, args.IfName, args.Args, false, false)
	if err != nil {
		return err
	}
	key := waycni.Key{Network: parsed.Conf.Name, ContainerID: args.ContainerID, IfName: args.IfName}
	return waycni.NewPlugin(parsed, waycni.LinuxEnforcer{Backend: dataplane.NewBackend()}).Delete(context.Background(), key, args.Netns)
}

func cmdGC(args *skel.CmdArgs) error {
	parsed, err := waycni.Parse(args.StdinData, "", "", "", "", false, false)
	if err != nil {
		return err
	}
	valid := make(map[waycni.Key]struct{}, len(parsed.Conf.ValidAttachments))
	for _, attachment := range parsed.Conf.ValidAttachments {
		key := waycni.Key{Network: parsed.Conf.Name, ContainerID: attachment.ContainerID, IfName: attachment.IfName}
		if err := key.Validate(); err != nil {
			return err
		}
		valid[key] = struct{}{}
	}
	return waycni.NewPlugin(parsed, waycni.LinuxEnforcer{Backend: dataplane.NewBackend()}).GC(context.Background(), parsed.Conf.Name, valid)
}

func cmdStatus(args *skel.CmdArgs) error {
	parsed, err := waycni.Parse(args.StdinData, "", "", "", "", false, false)
	if err != nil {
		return err
	}
	err = waycni.NewPlugin(parsed, waycni.LinuxEnforcer{Backend: dataplane.NewBackend()}).Status(context.Background())
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("local Waycloak agent socket is unavailable: %w", err)
	}
	return err
}
