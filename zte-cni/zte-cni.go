//
// Copyright (c) 2016 Zte Networks, Inc. All rights reserved.
//

// This will form the Zte CNI plugin for networking
// containers spawned using Mesos & k8s containerizers

package main

import (
	"flag"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/version"
	ovs "zte-cni/port"
	"github.com/satori/go.uuid"
	"gopkg.in/natefinch/lumberjack.v2"
	"gopkg.in/yaml.v2"
	dockerClient "github.com/docker/docker/client"
	kapi "k8s.io/kubernetes/pkg/api"
	krestclient "k8s.io/kubernetes/pkg/client/restclient"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
	"io/ioutil"
	"zte-cni/client"
	"zte-cni/config"
	"zte-cni/k8s"
	"os"
	"runtime"
	"strings"
	"net"
	"time"
)
var logMessageCounter int
type logTextFormatter log.TextFormatter
var supportedLogLevels = map[string]log.Level{
	"debug": log.DebugLevel,
	"info":  log.InfoLevel,
	"warn":  log.WarnLevel,
	"error": log.ErrorLevel,
}
// zteCNIConfig will be a pointer variable to
// Config struct that will hold all Zte CNI plugin
// parameters
var zteCNIConfig = &config.Config{}
var operMode string
var orchestrator string
// zteMetadataObj will be a structure pointer
// to hold Zte metadata
var zteMetadataObj = client.ZteMetadata{}

//ZteDockerClient structure holds docker client
type ZteDockerClient struct {
	socketFile string
	dclient    *dockerClient.Client
}
var ztedocker = &ZteDockerClient{}
var isAtomic bool
type containerInfo struct {
	ID string `json:"container_id"`
}
// Const definitions for plugin log location and input parameter file
const (
	paramFile     = "/etc/default/zte-cni.yaml"
	logFolder     = "/var/log/cni/"
	cniLogFile    = "/var/log/cni/zte-cni.log"
	daemonLogFile = "/var/log/cni/zte-daemon.log"
	bridgeName    = "br-int"
	kubernetes    = "k8s"
	mesos         = "mesos"
	openshift     = "ose"
)

func init() {
	// This ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
//	hostname, _ = os.Hostname()
//	log.Infof("Get Hostname %s",hostname)
	// Reading Zte CNI plugin parameter file
	data, err := ioutil.ReadFile(paramFile)
	if err != nil {
		log.Errorf("Error in reading from Zte CNI plugin parameter file: %s\n", err)
	}
	if err = yaml.Unmarshal(data, zteCNIConfig); err != nil {
		log.Errorf("Error in unmarshalling data from Zte CNI parameter file: %s\n", err)
	}
	// Set default values if some values were not set
	// in Zte CNI yaml file
	client.SetDefaultsForZteCNIConfig(zteCNIConfig)
	if _, err = os.Stat(logFolder); err != nil {
		if os.IsNotExist(err) {
			err = os.Mkdir(logFolder, 777)
			if err != nil {
				fmt.Printf("Error creating log folder: %v", err)
			}
		}
	}
	// Use a new flag set so as not to conflict with existing
	// libraries which use "flag"
	flagSet := flag.NewFlagSet("Zte", flag.ExitOnError)
	// Determining the mode of operation
	mode := flagSet.Bool("daemon", false, "a bool")
	//mode := flagSet.Bool("daemon", true, "a bool")
	err = flagSet.Parse(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	var logfile string
	if *mode {
		operMode = "daemon"
		logfile = daemonLogFile
	} else {
		operMode = "cni"
		logfile = cniLogFile
	}
	customFormatter := new(logTextFormatter)
	log.SetFormatter(customFormatter)
	log.SetOutput(&lumberjack.Logger{
		Filename: logfile,
		MaxSize:  zteCNIConfig.LogFileSize,
		MaxAge:   30,
	})
	log.SetLevel(supportedLogLevels[strings.ToLower(zteCNIConfig.LogLevel)])
	// Determine which orchestrator is making the CNI call
	var arg string
	arg = os.Args[0]
	if strings.Contains(arg, mesos) {
		orchestrator = mesos
	} else if strings.Contains(arg, kubernetes) {
		orchestrator = kubernetes
	} else {
		orchestrator = openshift
	}
	switch orchestrator {
	case mesos:
		log.Debugf("CNI call for mesos orchestrator")
	case kubernetes:
		log.Debugf("CNI call for k8s orchestrator")
	case openshift:
		log.Debugf("CNI call for ose orchestrator")
	default:
		panic("Invalid orchestrator for the CNI call")
	}
}

func (f *logTextFormatter) Format(entry *log.Entry) ([]byte, error) {
	logMessageCounter++
	return []byte(fmt.Sprintf("|%v|%s|%04d|%s\n", entry.Time, strings.ToUpper(log.Level.String(entry.Level)), logMessageCounter, entry.Message)), nil
}

func Convert(arg *skel.CmdArgs) string {
	a :=  strings.Replace(arg.Args, "K8S_POD_NAMESPACE", "K8sPodNamespace", -1)
	b :=  strings.Replace(a, "K8S_POD_NAME", "K8sPodName", -1)
	c :=  strings.Replace(b, "K8S_POD_INFRA_CONTAINER_ID", "K8sPodInfraContainerID", -1)
	return c
}

func NetworkConnect(args *skel.CmdArgs) error {
	log.Infof("ContainerID:%s", args.ContainerID)
	log.Infof("Netns:%s", args.Netns)
	log.Infof("IfName:%s", args.IfName)
	log.Infof("Args:%s", args.Args)
	log.Infof("Path:%s", args.Path)
	log.Infof("StdinData:%s", args.StdinData)
	log.Infof("Zte CNI plugin invoked to add an entity to Zte defined ZENIC network")

	var err error
	var gatewayIP string
	var mask string
	var podIP string
	var mac string
	var ovsConnection ovs.OVSConnection
	var result *types.Result
	entityInfo := make(map[string]string)
	for {
		ovsConnection, err = client.ConnectToZTEOVSDB(zteCNIConfig)
		if err != nil {
			log.Errorf("Error connecting to OVS. Will re-try connection")
		} else {
			break
		}
		time.Sleep(time.Duration(3) * time.Second)
	}
	log.Debugf("Successfully established a connection to Zte OVS")
	prams := Convert(args)
	log.Debugf("Args convert %s", prams)
	if prams == "" {
		log.Errorf("Error: Args is nil")
		return nil
	}
	k8sArgs := client.K8sArgs{}
	if orchestrator == kubernetes || orchestrator == openshift {
		log.Debugf("Orchestrator ID is %s", orchestrator)
		// Parsing CNI args obtained for K8S/Openshift
		err = types.LoadArgs(prams, &k8sArgs)
	if err != nil {
		log.Errorf("Error in loading k8s CNI arguments")
		return fmt.Errorf("Error in loading k8s CNI arguments: %s", err)
	}
	log.Debugf("Infra Container ID for pod %s is %s", string(k8sArgs.K8sPodName), string(k8sArgs.K8sPodInfraContainerID))

	entityInfo["name"] = string(k8sArgs.K8sPodName)
	entityInfo["entityport"] = args.IfName
	entityInfo["brport"] = client.GetZtePortName(entityInfo["entityport"], args.ContainerID)
	err := k8s.GetPodZteMetadata(&zteMetadataObj, string(k8sArgs.K8sPodName), string(k8sArgs.K8sPodNamespace), orchestrator)
	if err != nil {
		log.Errorf("Error obtaining Zte metadata")
		return fmt.Errorf("Error obtaining Zte metadata: %s", err)
	}
		entityInfo["uuid"] = string(k8sArgs.K8sPodInfraContainerID)
		log.Infof("Zte metadata obtained for pod %s is Enterprise: %s, Domain: %s, Zone: %s, Network: %s and User:%s", string(k8sArgs.K8sPodName), zteMetadataObj.Enterprise, zteMetadataObj.Domain, zteMetadataObj.Zone, zteMetadataObj.Network, zteMetadataObj.User)
	} else {
		log.Debugf("Orchestrator ID is %s", orchestrator)
		entityInfo["name"] = args.ContainerID
		newContainerUUID := strings.Replace(args.ContainerID, "-", "", -1)
		formattedContainerUUID := newContainerUUID + newContainerUUID
		entityInfo["uuid"] = formattedContainerUUID
		entityInfo["entityport"] = args.IfName
		entityInfo["brport"] = client.GetZtePortName(entityInfo["entityport"], entityInfo["uuid"])
		err = client.GetContainerZteMetadata(&zteMetadataObj, args)
		if err != nil {
			log.Errorf("Error obtaining Zte metadata")
			return fmt.Errorf("Error obtaining Zte metadata: %s", err)
		}
	}
	log.Infof("Attaching entity %s to Zte defined network", entityInfo["name"])
	// Here we setup veth paired interface to connect the Container
	// to Zte defined network
	netns := args.Netns
	err = client.SetupVEth(netns, entityInfo, zteCNIConfig.MTU)
	if err != nil {
		log.Errorf("Error creating veth paired interface for entity %s", entityInfo["name"])
		// Cleaning up veth ports from OVS before returning if we fail during
		// veth create task
		_ = client.DeleteVethPair(entityInfo["brport"], entityInfo["entityport"])
		return fmt.Errorf("Failed to create veth paired interface for the entity")
	}
	log.Debugf("Successfully created a veth paired port for entity %s", entityInfo["name"])

	portUUID := uuid.NewV4().String()
	log.Debugf("Create a portUUID %s",portUUID)
	hostName, _ := os.Hostname()
	log.Infof("Get Hostname: %s",hostName)
	podIP, gatewayIP, mask, mac, err = k8s.GetPodIPFromZteK8sMon(string(k8sArgs.K8sPodName),
		string(k8sArgs.K8sPodNamespace), portUUID, hostName)
	if err != nil{
		log.Errorf("Connect ip failed %s", podIP)
		_ = client.DeleteVethPair(entityInfo["brport"], entityInfo["entityport"])
	}
	ip := net.ParseIP(podIP)
	if ip == nil{
		log.Errorf("Get ip failed %s", podIP)
		_ = client.DeleteVethPair(entityInfo["brport"], entityInfo["entityport"])
		return fmt.Errorf("Get ip from zenic failed")
	}
	entityInfo["ip"] = podIP
	entityInfo["gw"] = gatewayIP
	entityInfo["mask"] = mask
	entityInfo["mac"] = mac

	var info ovs.OVSDB
	info.Name = entityInfo["name"]
	//info.UUID = portUUID
	info.UUID = entityInfo["name"]
	info.Mac = mac
	err = ovsConnection.AddPortToBrint(entityInfo["brport"], info)
	if err != nil {
		log.Errorf("Error adding bridge veth end %s of entity %s to br-int", entityInfo["brport"], entityInfo["name"])
		// Cleaning up veth ports from OVS
		_ = client.DeleteVethPair(entityInfo["brport"], entityInfo["entityport"])
		return fmt.Errorf("Failed to add bridge veth port to br-int")
	}
	log.Debugf("Attached veth interface %s to bridge %s for entity %s", entityInfo["brport"], bridgeName, entityInfo["name"])
	result, err = client.AssignIPToContainerIntf(netns, entityInfo)
	log.Infof("Get assign result", result)
	if err != nil {
		log.Errorf("Error configuring entity %s with an IP address", entityInfo["name"])
		return fmt.Errorf("Error configuring entity interface with IP %v", err)
	}
	log.Infof("Successfully configured entity %s with an IP address %s, macaddress %s", entityInfo["name"], entityInfo["ip"], entityInfo["mac"])
	return result.Print()
}

func NetworkDisconnect(args *skel.CmdArgs) error {
	log.Infof("Zte CNI plugin invoked to detach an entity from a Zte defined ZENIC network")
	var err error
	var ovsConnection ovs.OVSConnection
	var portName string
	entityInfo := make(map[string]string)
	for {
		ovsConnection, err = client.ConnectToZTEOVSDB(zteCNIConfig)
		if err != nil {
			log.Errorf("Error connecting to OVS. Will re-try connection")
		} else {
			break
		}
		time.Sleep(time.Duration(3) * time.Second)
	}
	log.Debugf("Successfully established a connection to Zte OVS")
	prams := Convert(args)
	if prams == "" {
		log.Errorf("Error: Args is nil")
		return nil
	}
	k8sArgs := client.K8sArgs{}
	if orchestrator == kubernetes || orchestrator == openshift {
		log.Debugf("Orchestrator ID is %s", orchestrator)
		// Parsing CNI args obtained for K8S
		err = types.LoadArgs(prams, &k8sArgs)
		if err != nil {
			log.Errorf("Error in loading k8s CNI arguments")
			return fmt.Errorf("Error in loading k8s CNI arguments: %s", err)
		}
		entityInfo["name"] = string(k8sArgs.K8sPodName)
		entityInfo["uuid"] = string(k8sArgs.K8sPodInfraContainerID)
		entityInfo["entityport"] = args.IfName
		entityInfo["zone"] = string(k8sArgs.K8sPodNamespace)
		// Determining the Zte host port name to be deleted from OVSDB table
		portName = client.GetZtePortName(args.IfName, args.ContainerID)
	} else {
		entityInfo["name"] = args.ContainerID
		newContainerUUID := strings.Replace(args.ContainerID, "-", "", -1)
		formattedContainerUUID := newContainerUUID + newContainerUUID
		entityInfo["uuid"] = formattedContainerUUID
		entityInfo["entityport"] = args.IfName
		// Determining the Zte host port name to be deleted from OVSDB table
		portName = client.GetZtePortName(args.IfName, entityInfo["uuid"])
	}
	log.Infof("Detaching entity %s from Zte defined network", entityInfo["name"])
	err = ovsConnection.RemovePortFromBrint(portName)
	if err != nil {
		log.Errorf("Failed to remove veth port %s for entity %s from br-int", portName, entityInfo["name"])
	}
	log.Debugf("Successfully remove port from br-int to Zte OVS", err)
	err = client.DeleteVethPair(portName, entityInfo["entityport"])
	if err != nil {
		log.Errorf("Failed to clear veth ports from OVS for entity %s", entityInfo["name"])
	}
	log.Debugf("Successfully delete Veth Pair  in Zte OVS", err)
	ovsConnection.Disconnect()
	k8s.ReleasePodFromZteK8sMon(string(k8sArgs.K8sPodName),string(k8sArgs.K8sPodNamespace))
	return nil
}

func main() {
	// This is added to handle https://github.com/kubernetes/kubernetes/pull/24983
	// which is a known k8s CNI issue
	if orchestrator == kubernetes || orchestrator == openshift {
		if err := client.AddIgnoreUnknownArgs(); err != nil {
			os.Exit(1)
		}
	}
	var err error
	if operMode == "daemon" {
		log.Infof("Starting Zte CNI audit daemon on agent nodes")
		err = MonitorAgent(zteCNIConfig, orchestrator)
		if err != nil {
			log.Errorf("Error encountered while running Zte CNI daemon: %s\n", err)
		}
	}else {
		skel.PluginMain(NetworkConnect, NetworkDisconnect, version.PluginSupports("0.2.0", "0.3.0"))
	}
}

func MonitorAgent(config *config.Config, orchestrator string) error {
	var err error
	var ovsConnection ovs.OVSConnection
	for {
		ovsConnection, err = client.ConnectToZTEOVSDB(config)
		if err != nil {
			log.Errorf("Error connecting to OVS.Will re-try connection in 5 seconds %s", ovsConnection)
		} else {
			break
		}
		time.Sleep(time.Duration(5) * time.Second)
	}
	log.Debugf("Successfully established a connection to Zte OVS")
	// Setting up docker client connection
	ztedocker.socketFile = "unix:///var/run/docker.sock"
	ztedocker.dclient, err = connectToDockerDaemon(ztedocker.socketFile)
	if err != nil {
		log.Errorf("Connection to docker daemon failed with error: %v", err)
	}
	log.Infof("Starting Zte CNI monitoring daemon for %s agent nodes", orchestrator)
	// Cleaning up stale ports/entities when audit daemon starts
	err = CleanupStalePortsEntities(ovsConnection, orchestrator)
	if err != nil {
		log.Errorf("Error cleaning up stale entities and ports on OVS")
	}
	return nil
}

func getActiveK8SPods(orchestrator string) ([]string, error) {
	log.Infof("Obtaining currently active K8S pods on agent node")
	var podsList []string
	var config *krestclient.Config
	var kubeconfFile string
	var dir string
	hostName, _ := os.Hostname()
	log.Infof("Get Node Hostname: %s",hostName)
	if isAtomic == true {
		dir = "/var/usr/share"
	} else {
		dir = "/usr/share"
	}
	if orchestrator == "k8s" {
		kubeconfFile = dir + "/zte-k8s/zte.kubeconfig"
	} else {
		kubeconfFile = dir + "/zte-openshift/zte.kubeconfig"
	}
	loadingRules := &clientcmd.ClientConfigLoadingRules{}
	loadingRules.ExplicitPath = kubeconfFile
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	kubeConfig, err := loader.ClientConfig()
	if err != nil {
		log.Errorf("Error loading kubeconfig file")
		return podsList, err
	}
	config = kubeConfig
	kubeClient, err := kclient.New(config)
	if err != nil {
		log.Errorf("Error trying to create kubeclient")
		return podsList, err
	}
	var listOpts = &kapi.ListOptions{LabelSelector: labels.Everything(), FieldSelector: fields.Everything()}
	pods, err := kubeClient.Pods(kapi.NamespaceAll).List(*listOpts)
	if err != nil {
		log.Errorf("Error occured while fetching pods from k8s api server")
		return podsList, err
	}
	for _, pod := range pods.Items {
		nodename := pod.Spec.NodeName
		if hostName == nodename {
			skel.PluginMain(NetworkConnect, NetworkDisconnect, version.PluginSupports("0.2.0", "0.3.0"))
		}else {
			return nil,nil
		}
	}
	return podsList, err
}

func connectToDockerDaemon(socketFile string) (*dockerClient.Client, error) {
	err := os.Setenv("DOCKER_HOST", socketFile)
	if err != nil {
		log.Errorf("Setting DOCKER_HOST failed")
		return nil, err
	}
	client, err := dockerClient.NewEnvClient()
	if err != nil {
		log.Errorf("Connecting to docker client failed with error: %v", err)
		return nil, err
	}
	return client, nil
}

func CleanupStalePortsEntities(ovsConnection ovs.OVSConnection, orchestrator string) error {
	log.Debugf("Cleaning up stale ports and entities in OVS as a part of the audit daemon")
	var err error
	var entityUUIDList []string
	switch orchestrator {
	case "k8s":
		entityUUIDList, err = getActiveK8SPods(orchestrator)
		if err != nil {
			log.Errorf("Error occured while obtaining currently active Pods list: %v", err)
			return err
		}
		log.Debugf("Currently active k8s pods list : %v", entityUUIDList)
	case "ose":
		entityUUIDList, err = getActiveK8SPods(orchestrator)
		if err != nil {
			log.Errorf("Error occured while obtaining currently active Pods list: %v", err)
			return err
		}
		log.Debugf("Currently active openshift pods list : %v", entityUUIDList)
	default:
	}
	return err
}

