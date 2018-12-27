// This module includes utilities that can used by Zte CNI plugin
// during runtime

package client

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/containernetworking/cni/pkg/ip"
	"github.com/containernetworking/cni/pkg/ns"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	ovsSdk "zte-cni/port"
	"github.com/vishvananda/netlink"
	"net"
	"zte-cni/config"
	"os"
	"regexp"
	"strings"
)

const (
	addCli    = "add"
	deleteCli = "del"
)

// AddIgnoreUnknownArgs appends the 'IgnoreUnknown=1' option to CNI_ARGS before calling the CNI plugin.
// Otherwise, it will complain about the Kubernetes arguments.
// See https://github.com/kubernetes/kubernetes/pull/24983
func AddIgnoreUnknownArgs() error {
	cniArgs := "IgnoreUnknown=1"
	if os.Getenv("CNI_ARGS") != "" {
		cniArgs = fmt.Sprintf("%s;%s", cniArgs, os.Getenv("CNI_ARGS"))
	}
	return os.Setenv("CNI_ARGS", cniArgs)
}

// SetupVEth will set up veth pair for container to be connected to Zte defined ZENIC network
func SetupVEth(netns string, containerInfo map[string]string, mtu int) (err error) {
	log.Debugf("Creating veth paired ports for container %s with container port %s and host port %s", containerInfo["name"],
		containerInfo["entityport"], containerInfo["brport"])
	// Creating veth pair to attach container ns to a host ns to a Zte network
	err = ns.WithNetNSPath(netns, func(hostNS ns.NetNS) error {
		localVethPair := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: containerInfo["brport"], MTU: mtu},
			PeerName:  containerInfo["entityport"],
		}
		if err = netlink.LinkAdd(localVethPair); err != nil {
			log.Errorf("Failed to create veth paired port for container %s", containerInfo["name"])
			return fmt.Errorf("Failed to create a veth paired port for the container")
		}
		contVeth, errStr := netlink.LinkByName(containerInfo["entityport"])
		if errStr != nil {
			log.Errorf("Failed to lookup container port %s for container %s", containerInfo["entityport"], containerInfo["name"])
			return fmt.Errorf("Failed to lookup container end veth port")
		}
		//contVethMAC = contVeth.Attrs().HardwareAddr.String()
		//log.Debugf("MAC address for container end of veth paired port for container %s", containerInfo["name"])

		// Bring the container side veth port up
		err = netlink.LinkSetUp(contVeth)
		if err != nil {
			log.Errorf("Error enabling container end of veth port for container %s", containerInfo["name"])
			return fmt.Errorf("Failed to container end veth interface")
		}
		brVeth, errStr := netlink.LinkByName(containerInfo["brport"])
		if errStr != nil {
			log.Errorf("Failed to lookup host port %s for container %s", containerInfo["brport"], containerInfo["name"])
			return fmt.Errorf("Failed to lookup alubr0 bridge end veth port")
		}
		// Now that the everything has been successfully set up in the container, move the "host" end of the
		// veth into the host namespace.
		if err = netlink.LinkSetNsFd(brVeth, int(hostNS.Fd())); err != nil {
			return fmt.Errorf("failed to move veth to host netns: %v", err)
		}
		return nil
	})
	if err != nil {
		return  err
	}
	brVeth, err := netlink.LinkByName(containerInfo["brport"])
	if err != nil {
		log.Errorf("Failed to lookup host port %s for container %s", containerInfo["brport"], containerInfo["name"])
		return fmt.Errorf("failed to lookup %q: %v", containerInfo["brport"], err)
	}
	if err = netlink.LinkSetUp(brVeth); err != nil {
		log.Errorf("Error enabling host end of veth port for container %s", containerInfo["name"])
		return fmt.Errorf("failed to set %q up: %v", containerInfo["brport"], err)
	}
	return err
}

// AssignIPToContainerIntf will configure the container end of the veth
// interface with IP address assigned by the Zte CNI plugin
func AssignIPToContainerIntf(netns string, containerInfo map[string]string) (*types.Result, error) {
	var err error
	r := &types.Result{}
	log.Debugf("Configuring container %s interface %s with IP %s and default gateway %s assigned by Zte CNI plugin", containerInfo["name"],
		containerInfo["entityport"], containerInfo["ip"], containerInfo["gw"], containerInfo["mac"])
	netmask := net.IPMask(net.ParseIP(containerInfo["mask"]).To4())
	prefixSize, _ := netmask.Size()
	ipV4Network := net.IPNet{IP: net.ParseIP(containerInfo["ip"]), Mask: net.CIDRMask(prefixSize, 32)}
	macadd, err1 := net.ParseMAC(containerInfo["mac"])
	if err1 != nil {
		log.Errorf("Failed to assign macaddress to container %s", err1)
		return nil, err1
	}
	log.Infof("MAC address for container end of veth paired port for container %s", macadd)
	r.IP4 = &types.IPConfig{IP: ipV4Network, Mac: macadd}
	err = ns.WithNetNSPath(netns, func(hostNS ns.NetNS) error {
		contVeth, errStr := netlink.LinkByName(containerInfo["entityport"])
		//contVeth.Attrs().HardwareAddr = macadd
		if errStr != nil {
			err = fmt.Errorf("failed to lookup %q: %v", containerInfo["entityport"], err)
			return err
		}
		// Add a connected route to a dummy next hop so that a default route can be set
		gw := net.ParseIP(containerInfo["gw"])
		gwNet := &net.IPNet{IP: gw, Mask: net.CIDRMask(32, 32)}
		if err = netlink.RouteAdd(&netlink.Route{
			LinkIndex: contVeth.Attrs().Index,
			Scope:     netlink.SCOPE_LINK,
			Dst:       gwNet}); err != nil {
			return fmt.Errorf("failed to add route %v", err)
		}
		if err = ip.AddDefaultRoute(gw, contVeth); err != nil {
			log.Infof("Default route already exists within the container; skip re-configuring default route")
		} else {
			log.Debugf("Successfully added default route to container %s via gateway %s", containerInfo["name"], containerInfo["gw"])
		}
		if err = netlink.AddrAdd(contVeth, &netlink.Addr{IPNet: &r.IP4.IP}); err != nil {
			log.Errorf("Failed to assign IP %s to container %s", &r.IP4.IP, containerInfo["name"])
			return fmt.Errorf("failed to add IP addr to %q: %v", containerInfo["entityport"], err)
		}
		if err = netlink.LinkSetHardwareAddr(contVeth, macadd); err != nil {
			return fmt.Errorf("failed to add mac addr to %q: %v", containerInfo["entityport"], err)
		}
		log.Debugf("Successfully assigned IP %s to container %s", &r.IP4.IP, containerInfo["name"])
		return err
	})
	return r, err
}

// ConnectToZTEOVSDB will try connecting to OVS OVSDB via unix socket connection
func ConnectToZTEOVSDB(conf *config.Config) (ovsSdk.OVSConnection, error) {
	log.Debugf("Receive a connect to ovsdb ", conf)
	ovsConnection, err := ovsSdk.NewUnixSocketConnection(conf.OVSEndpoint)
	if err != nil {
		return ovsConnection, fmt.Errorf("Couldn't connect to OVS: %s", err)
	}
	return ovsConnection, nil
}

// DeleteVethPair will help user delete veth pairs on OVS
func DeleteVethPair(brPort string, entityPort string) error {
	log.Debugf("Deleting veth paired port %s as a part of Zte CNI cleanup", brPort)
	localVethPair := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: brPort},
		PeerName:  entityPort,
	}
	err := netlink.LinkDel(localVethPair)
	if err != nil {
		log.Errorf("Deleting veth pair %+v failed with error: %s", localVethPair, err)
		return err
	}
	return nil
}

// GetZtePortName creates a unique port name
// for container host port entry
func GetZtePortName(intf string, uuid string) string {
	// Extracting port prefix from container interface name
	// Eg: port prefix for interface "eth0" will be "0"
	re := regexp.MustCompile("[0-9]+")
	intfPrefix := re.FindAllString(intf, -1)
	// Formatting UUID string by removing "-"
	formattedUUID := strings.Replace(uuid, "-", "", -1)
	ztePortName := intfPrefix[0] + generateVEthString(formattedUUID)
	return ztePortName
}

// generateVEthString generates a unique SHA encoded
// string for veth Zte host port
func generateVEthString(uuid string) string {
	h := sha1.New()
	_, err := h.Write([]byte(uuid))
	if err != nil {
		log.Errorf("Error generating unique hash string for entity")
	}
	return fmt.Sprintf("%s", hex.EncodeToString(h.Sum(nil))[:14])
}

// GetContainerZteMetadata populates ZteMetadata struct
// with network information from labels passed from CNI NetworkProtobuf
func GetContainerZteMetadata(zteMetadata *ZteMetadata, args *skel.CmdArgs) error {
	var err error
	// Loading CNI network configuration
	conf := NetConf{}
	if err = json.Unmarshal(args.StdinData, &conf); err != nil {
		return fmt.Errorf("Failed to load netconf from CNI: %v", err)
	}
	// Parse endpoint labels passed in by Mesos, and store in a map.
	labels := map[string]string{}
	for _, label := range conf.Args.Mesos.NetworkInfo.Labels.Labels {
		labels[label.Key] = label.Value
	}
	if _, ok := labels["enterprise"]; ok {
		zteMetadata.Enterprise = labels["enterprise"]
	}
	if _, ok := labels["domain"]; ok {
		zteMetadata.Domain = labels["domain"]
	}
	if _, ok := labels["zone"]; ok {
		zteMetadata.Zone = labels["zone"]
	}
	if _, ok := labels["network"]; ok {
		zteMetadata.Network = labels["network"]
	}
	if _, ok := labels["user"]; ok {
		zteMetadata.User = labels["user"]
	}
	if _, ok := labels["policy_group"]; ok {
		zteMetadata.PolicyGroup = labels["policy_group"]
	}
	if _, ok := labels["static_ip"]; ok {
		zteMetadata.StaticIP = labels["static_ip"]
	}
	if _, ok := labels["redirection_target"]; ok {
		zteMetadata.RedirectionTarget = labels["redirection_target"]
	}
	return err
}

// SetDefaultsForZteCNIConfig will set default values for
// Zte CNI yaml parameters if they have not been set
func SetDefaultsForZteCNIConfig(conf *config.Config) {
	if conf.OVSEndpoint == "" {
		log.Warnf("OVS endpoint not set. Using default value")
		conf.OVSEndpoint = "/var/run/openvswitch/db.sock"
	}
	if conf.OVSBridge == "" {
		log.Warnf("OVS bridge not set. Using default value")
		conf.OVSBridge = "alubr0"
	}
	if conf.MonitorInterval == 0 {
		log.Warnf("Monitor interval for audit daemon not set. Using default value")
		conf.MonitorInterval = 60
	}
	if conf.CNIVersion == "" {
		log.Warnf("CNI version not set. Using default value")
		conf.CNIVersion = "0.2.0"
	}
	if conf.LogLevel == "" {
		log.Warnf("Expected log level not set. Using default value")
		conf.LogLevel = "info"
	}
	if conf.PortResolveTimer == 0 {
		log.Warnf("OVSDB port resolution wait timer not set. Using default value")
		conf.PortResolveTimer = 60
	}
	if conf.LogFileSize == 0 {
		log.Warnf("CNI log file size in MB not set. Using default value")
		conf.LogFileSize = 1
	}
	if conf.OVSConnectionCheckTimer == 0 {
		log.Warnf("OVS Connection keep alive timer not set. Using default value")
		conf.OVSConnectionCheckTimer = 180
	}
}
