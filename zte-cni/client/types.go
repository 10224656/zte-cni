package client

import (
	"github.com/containernetworking/cni/pkg/types"
	"net"
)

// Args will hold the network metadata
// info to be passed to CNI plugins
type Args struct {
	Mesos Mesos `json:"org.apache.mesos,omitempty"`
}

// Mesos will hold the network information
// that will be passed to CNI plugins by Mesos runtime
type Mesos struct {
	NetworkInfo NetworkInfo `json:"network_info"`
}

// NetworkInfo defines CNI network name
// and network metadata labels that will be passed by CNI
type NetworkInfo struct {
	Name   string `json:"name"`
	Labels struct {
		Labels []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"labels,omitempty"`
	} `json:"labels,omitempty"`
}

// NetConf stores the common network config for Zte CNI plugin
type NetConf struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Hostname string `json:"hostname"`
	Args     Args   `json:"args"`
}

type K8sArgs struct {
	types.CommonArgs
	IP                         net.IP
	K8sPodName               types.UnmarshallableString
	K8sPodNamespace          types.UnmarshallableString
	K8sPodInfraContainerID types.UnmarshallableString
}

// ZteMetadata will hold metadata needed to resolve
// a port using Zte defined overlay network
type ZteMetadata struct {
	Enterprise        string
	Domain            string
	Zone              string
	Network           string
	User              string
	PolicyGroup       string
	StaticIP          string
	RedirectionTarget string
}
