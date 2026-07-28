// Copyright 2026 The Waycloak Authors.
// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	wayv1 "github.com/Amoenus/waycloak/api/v1beta1"
	waycni "github.com/Amoenus/waycloak/internal/cni"
	"github.com/Amoenus/waycloak/internal/cniinstall"
	"github.com/Amoenus/waycloak/internal/dataplane"
	"github.com/containernetworking/cni/pkg/skel"
	cnitypes "github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := install(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add: cmdAdd, Del: cmdDel, Check: cmdCheck, GC: cmdGC, Status: cmdStatus,
	}, version.PluginSupports("0.3.1", "0.4.0", "1.0.0", "1.1.0"), "Waycloak chained CNI feasibility plugin")
}

func install() error {
	if len(os.Args) != 12 {
		return errors.New("usage: waycloak-cni install SOURCE BINARY CONFIG RECEIPT BACKUP SOCKET KEY STATE RELEASE_VERSION RELEASE_DIGEST")
	}
	return cniinstall.Install(cniinstall.Options{
		SourceBinary: os.Args[2], BinaryPath: os.Args[3], ConfigPath: os.Args[4], ReceiptPath: os.Args[5], BackupPath: os.Args[6],
		AgentSocket: os.Args[7], AgentKeyFile: os.Args[8], StateDirectory: os.Args[9],
		ReleaseIdentity: wayv1.ReleaseIdentity{Version: os.Args[10], ManifestDigest: os.Args[11]},
	})
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
