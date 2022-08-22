// Copyright 2018-2019 Red Hat, Inc.
// Copyright 2014 CNI authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Go version 1.10 or greater is required. Before that, switching namespaces in
// long running processes in go did not work in a reliable way.
//go:build go1.10
// +build go1.10

package plugin

import (
	"errors"
	"fmt"
	"log"
	"runtime"

	"github.com/k8snetworkplumbingwg/ovs-cni/pkg/config"
	"go.uber.org/zap"

	"github.com/containernetworking/cni/pkg/skel"
	cnitypes "github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"

	"github.com/k8snetworkplumbingwg/ovs-cni/pkg/ovsdb"
	"github.com/k8snetworkplumbingwg/ovs-cni/pkg/types"
)

var logger *zap.SugaredLogger

func init() {
	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()

	logger = InitLogger("./producer.log")
	defer logger.Sync()
	logger.Info("Starting PLUGIN....")
}

func logCall(command string, args *skel.CmdArgs) {
	log.Printf("CNI %s was called for container ID: %s, network namespace %s, interface name %s, configuration: %s",
		command, args.ContainerID, args.Netns, args.IfName, string(args.StdinData[:]))
}

func getPortUUID(ovsDriver *ovsdb.OvsBridgeDriver, interfaces []*current.Interface) (string, error) {
	for _, iface := range interfaces {
		uuid, err := ovsDriver.GetPortUUID(iface.Name)
		if err == nil {
			return uuid.GoUUID, nil
		}
	}

	return "", errors.New("cannot find port in db")
}

func attachPortToMirror(ovsDriver *ovsdb.OvsBridgeDriver, portUUIDStr string, mirror *types.Mirror) error {
	err := ovsDriver.AttachPortToMirrorProducer(portUUIDStr, mirror.Name, mirror.Ingress, mirror.Egress)
	if err != nil {
		return err
	}

	return nil
}

func detachPortFromMirror(ovsDriver *ovsdb.OvsBridgeDriver, portUUIDStr string, mirror *types.Mirror) error {
	err := ovsDriver.DetachPortFromMirrorProducer(portUUIDStr, mirror.Name)
	if err != nil {
		return err
	}

	return nil
}

// CmdAdd add handler for attaching container into network
func CmdAdd(args *skel.CmdArgs) error {
	logCall("ADD", args)

	netconf, err := config.LoadMirrorConf(args.StdinData)
	if err != nil {
		return err
	}

	ovsDriver, err := ovsdb.NewOvsBridgeDriver(netconf.BrName, netconf.SocketFile)
	if err != nil {
		return err
	}

	portUUID, err := getPortUUID(ovsDriver, netconf.PrevResult.Interfaces)
	if err != nil {
		return fmt.Errorf("cannot get existing portUuid from db %v", err)
	}

	for _, mirror := range netconf.Mirrors {

		err = ovsDriver.CreateMirror(netconf.BrName, mirror.Name)
		if err != nil {
			return fmt.Errorf("cannot create mirror %s: %v ", mirror.Name, err)
		}

		if err = attachPortToMirror(ovsDriver, portUUID, mirror); err != nil {
			return fmt.Errorf("cannot attach port %s to mirror %s: %v", portUUID, mirror.Name, err)
		}
	}

	result := &current.Result{
		Interfaces: netconf.PrevResult.Interfaces,
	}

	return cnitypes.PrintResult(result, netconf.CNIVersion)
}

// CmdDel remove handler for deleting container from network
func CmdDel(args *skel.CmdArgs) error {
	logCall("DEL", args)

	logger.Info("--------------cmdDel--------------")
	logger.Info(args.IfName)
	logger.Info(args.ContainerID)
	logger.Info(args.Netns)
	logger.Info(args.Args)
	logger.Info(args.Path)
	logger.Info(fmt.Sprintf("cmdDel - the config data: %s\n", args.StdinData))

	netconf, err := config.LoadMirrorConf(args.StdinData)
	if err != nil {
		return err
	}

	logger.Infof("cmdDel - netconf parsed from StdinData is: %#v", netconf)
	logger.Infof("cmdDel - netconf prevresult: %#v", netconf.PrevResult)
	logger.Infof("cmdDel - netconf interfaces: %#v", netconf.PrevResult.Interfaces)

	ovsDriver, err := ovsdb.NewOvsBridgeDriver(netconf.BrName, netconf.SocketFile)
	if err != nil {
		return err
	}

	portUUID, err := getPortUUID(ovsDriver, netconf.PrevResult.Interfaces)
	if err != nil {
		return fmt.Errorf("cannot get existing portUuid from db %v", err)
	}

	for _, mirror := range netconf.Mirrors {

		mirrorExist, err := ovsDriver.IsMirrorPresent(mirror.Name)
		if err != nil {
			return err
		}
		if !mirrorExist {
			// skip error because CNI spec states that "Plugins should generally complete a DEL action without error even if some resources are missing"
			continue
		}

		if err = detachPortFromMirror(ovsDriver, portUUID, mirror); err != nil {
			return fmt.Errorf("cannot detach port %s from mirror %s: %v", portUUID, mirror.Name, err)
		}

		err = ovsDriver.DeleteMirror(netconf.BrName, mirror.Name)
		if err != nil {
			return fmt.Errorf("cannot create mirror %s: %v ", mirror.Name, err)
		}
	}

	result := &current.Result{
		Interfaces: netconf.PrevResult.Interfaces,
	}

	return cnitypes.PrintResult(result, netconf.CNIVersion)
}

// CmdCheck check handler to make sure networking is as expected.
func CmdCheck(args *skel.CmdArgs) error {
	logCall("CHECK", args)

	netconf, err := config.LoadMirrorConf(args.StdinData)
	if err != nil {
		return err
	}

	ovsDriver, err := ovsdb.NewOvsBridgeDriver(netconf.BrName, netconf.SocketFile)
	if err != nil {
		return err
	}

	portUUID, err := getPortUUID(ovsDriver, netconf.PrevResult.Interfaces)
	if err != nil {
		return fmt.Errorf("cannot get existing portUuid from db %v", err)
	}

	for _, mirror := range netconf.Mirrors {
		mirrorExist, err := ovsDriver.CheckMirrorProducerWithPorts(mirror.Name, mirror.Ingress, mirror.Egress, portUUID)
		if err != nil {
			return err
		}

		if !mirrorExist {
			return fmt.Errorf("mirror %s not present", mirror.Name)
		}
	}

	return nil
}
