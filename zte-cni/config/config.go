package config

// ZteK8SConfig struct will be used to read and
// parse values from Zte zte-k8s yaml file on k8s agent nodes
type  ZteK8SConfig struct {
	K8SAPIServer              string `yaml:"masterApiServer"`
	ZteK8SMonServer         string `yaml:"zteMonRestServer"`
	DockerBridgeName          string `yaml:"dockerBridgeName"`
	InterfaceMTU              string `yaml:"interfaceMTU"`
	ServiceCIDR               string `yaml:"serviceCIDR"`
	LogLevel                  string `yaml:"logLevel"`
}

// Config struct will be used to read values from Zte CNI
// parameter file necessary for audit daemon and CNI plugin
type Config struct {
	OVSEndpoint             string
	OVSBridge               string
	MonitorInterval         int
	CNIVersion              string
	LogLevel                string
	PortResolveTimer        int
	LogFileSize             int
	OVSConnectionCheckTimer int
	MTU                     int
	StaleEntryTimeout       int64
}
