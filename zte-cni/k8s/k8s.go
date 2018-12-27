package k8s

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	krestclient "k8s.io/kubernetes/pkg/client/restclient"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	"net/http"
	"zte-cni/client"
	"zte-cni/config"
	"os"
)

var zteK8SConfig = &config.ZteK8SConfig{}
var k8RESTConfig *krestclient.Config
var zteK8sConfigFile string
var kubeconfFile string
var isHostAtomic bool

// ZteKubeMonResp will unmarshal JSON
// response from Zte kubemon service
type ZteKubeMonResp struct {
	PodIP 		string   `json:"subnetName"`
	GatewayIP 	string   `json:"gateway_ip"`
	Mask 		string   `json:"mask"`
	Mac			string    `json:"mac"`
}

// Pod will hold fields necessary to query
// Zte kubemon service to obtain pod metadata
type Pod struct {
	Name   string `json:"podName"`
	Zone   string `json:"desiredZone,omitempty"`
	Subnet string `json:"desiredSubnet,omitempty"`
	Action string `json:"action,omitempty"`
}

type ZtePod struct {
	Name string `json:"podName"`
	ID   string `json:"podId"`
	HostName string `json:"hostName"`
}

func getK8SLabelsPodUIDFromAPIServer(podNs string, podname string) error {
	log.Infof("Obtaining labels from API server for pod %s under namespace %s", podname, podNs)
	loadingRules := &clientcmd.ClientConfigLoadingRules{}
	loadingRules.ExplicitPath = kubeconfFile
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	kubeConfig, err := loader.ClientConfig()
	if err != nil {
		log.Errorf("Error loading kubeconfig file")
		return err
	}
	k8RESTConfig = kubeConfig
	kubeClient, err := kclient.New(k8RESTConfig)
	if err != nil {
		log.Errorf("Error trying to create kubeclient")
		return err
	}
	pod, err := kubeClient.Pods(podNs).Get(podname)
	if err != nil {
		log.Errorf("Error occured while querying pod %s under pod namespace %s, pods %s", podname, podNs, pod)
		return err
	}
	return err
}

func getZTEK8SConfig() error {
	// Reading Zte K8S yaml file
	data, err := ioutil.ReadFile(zteK8sConfigFile)
	if err != nil {
		return fmt.Errorf("Error in reading from Zte k8s yaml file: %s", err)
	}
	if err = yaml.Unmarshal(data, zteK8SConfig); err != nil {
		return fmt.Errorf("Error in unmarshalling data from Zte k8s yaml file: %s", err)
	}
	return err
}

func GetPodIPFromZteK8sMon(podname string, ns string, podID string, hostName string) (string, string, string, string, error) {
	log.Infof("Obtaining zte ip for pod %s under namespace %s", podname, ns)
	var result = new(ZteKubeMonResp)
	url := zteK8SConfig.ZteK8SMonServer + "/namespaces/" + ns + "/pods"
	client := &http.Client{}
	pod := &ZtePod{Name:podname, ID:podID, HostName:hostName}
	out, err := json.Marshal(pod)
	if err != nil {
		log.Errorf("Error occured while marshalling Pod data to communicate with Zte K8S monitor")
		return "","","","", err
	}
	var jsonStr = []byte(string(out))
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonStr))
	if err != nil {
		log.Errorf("Error occured while sending POST call to Zte K8S monitor" +
			" to obtain pod ip: %v", err)
		return "","","","", err
	}
	log.Debugf("Response sent to Zte kubemon is %v", bytes.NewBuffer(jsonStr))
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Error occured while reading response obtained from Zte" +
			" K8S monitor")
		return "","","","", err
	}
	err = json.Unmarshal(body, &result)
	if err != nil {
		log.Errorf("Error occured while unmarshalling Pod data obtained from" +
			" Zte K8S monitor")
		return result.PodIP,result.GatewayIP,result.Mask, result.Mac, err
	}
	log.Debugf("Pod ip information obtained from Zte K8S monitor : %s",
		result.PodIP, result.GatewayIP, result.Mask, result.Mac)
	return result.PodIP,result.GatewayIP,result.Mask, result.Mac, nil
}

func ReleasePodFromZteK8sMon(podname string, ns string) error {
	initDataDir("k8s")
	errCon := getZTEK8SConfig()
	if errCon != nil {
		log.Errorf("Error in parsing ZTE config file")
		return fmt.Errorf("Error in parsing ZTE config file: %s", errCon)
	}
	log.Infof("Releaseing pod %s under namespace %s", podname, ns)
	client := &http.Client{}
	url := zteK8SConfig.ZteK8SMonServer + "/namespaces/" + ns + "/pods" + "/" +  podname
	req, err := http.NewRequest("DELETE", url, nil)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache")
	if err != nil {
		log.Errorf("release pod req:%v, error:%s", req, err)
	}
	_, err = client.Do(req)
	if err != nil {
		log.Errorf("Error release while sending DELETE call to Zte K8S" +
			" monitor" + " to release pod : %v", err)
		return err
	}
	return nil
}

func initDataDir(orchestrator string) {
	isHostAtomic = VerifyHostType()
	var dir string
	if isHostAtomic == true {
		dir = "/var/usr/share/"
	} else {
		dir = "/usr/share/"
	}
	if orchestrator == "k8s" {
		zteK8sConfigFile = dir + "/zte-k8s/zte-k8s.yaml"
		kubeconfFile = dir + "/zte-k8s/zte.kubeconfig"
	} else {
		zteK8sConfigFile = dir + "/zte-openshift/zte-openshift.yaml"
		kubeconfFile = dir + "/zte-openshift/zte.kubeconfig"
	}
}
// VerifyHostType will determine the base host
// as RHEL server or RHEL atomic
func VerifyHostType() bool {
	// check if the host is an atomic host
	_, err := os.Stat("/run/ostree-booted")
	if err != nil {
		log.Infof("This is a RHEL server host")
		return false
	}
	log.Infof("This is a RHEL atomic host")
	return true
}
// GetPodZteMetadata will populate ZteMetadata struct
// needed for port resolution using CNI plugin
func GetPodZteMetadata(zteMetadata *client.ZteMetadata, name string, ns string, orchestrator string) error {
	initDataDir(orchestrator)
	log.Infof("Obtaining Zte Metadata for pod %s under namespace %s", name, ns)
	var err error
	// Parsing Zte K8S yaml file on K8S agent nodes
	err = getZTEK8SConfig()
	if err != nil {
		log.Errorf("Error in parsing Zte k8s yaml file")
		return fmt.Errorf("Error in parsing Zte k8s yaml file: %s", err)
	}
	// Obtaining pod labels if set from K8S API server
	err = getK8SLabelsPodUIDFromAPIServer(ns, name)
	if err != nil {
		log.Errorf("Error in obtaining pod labels from API server")
		return fmt.Errorf("Error in obtaining pod labels from API server: %s", err)
	}
	return err
}
