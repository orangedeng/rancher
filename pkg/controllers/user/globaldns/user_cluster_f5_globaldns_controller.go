package globaldns

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	f5cisv1 "github.com/F5Networks/k8s-bigip-ctlr/config/apis/cis/v1"
	"github.com/rancher/rancher/pkg/namespace"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
)

type VirtualServerInfo struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

func (g *UserGlobalDNSController) reconcileMultiClusterAppF5(obj *v3.GlobalDNS) ([]string, []VirtualServerInfo, error) {
	// If multiclusterappID is set, look for ingresses in the projects of multiclusterapp's targets
	// Get multiclusterapp by name set on GlobalDNS spec
	mcappName, err := getMultiClusterAppName(obj.Spec.MultiClusterAppName)
	if err != nil {
		return nil, nil, err
	}

	mcapp, err := g.multiclusterappLister.Get(namespace.GlobalNamespace, mcappName)
	if err != nil && k8serrors.IsNotFound(err) {
		logrus.Debugf("UserGlobalDNSController: Object Not found Error %v, while listing MulticlusterApp by name %v", err, mcappName)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("UserGlobalDNSController: Error %v Listing MulticlusterApp by name %v", err, mcappName)
	}

	// go through target projects which are part of the current cluster and find all virtualservers
	var allVirtualServers []*f5cisv1.VirtualServer

	for _, t := range mcapp.Spec.Targets {
		split := strings.SplitN(t.ProjectName, ":", 2)
		if len(split) != 2 {
			return nil, nil, fmt.Errorf("error in splitting project ID %v", t.ProjectName)
		}
		// check if the target project in this iteration is same as the cluster in current context
		if split[0] != g.clusterName {
			continue
		}

		// each target will have appName, this appName is also the namespace in which all workloads for this app are created
		virtualservers, err := g.virtualServerLister.List(t.AppName, labels.NewSelector())
		if err != nil {
			return nil, nil, err
		}
		allVirtualServers = append(allVirtualServers, virtualservers...)
	}

	// go through target projects which are part of the current cluster and find all transportservers
	var allTransportServers []*f5cisv1.TransportServer

	for _, t := range mcapp.Spec.Targets {
		split := strings.SplitN(t.ProjectName, ":", 2)
		if len(split) != 2 {
			return nil, nil, fmt.Errorf("error in splitting project ID %v", t.ProjectName)
		}
		// check if the target project in this iteration is same as the cluster in current context
		if split[0] != g.clusterName {
			continue
		}

		// each target will have appName, this appName is also the namespace in which all workloads for this app are created
		transportservers, err := g.transportServerLister.List(t.AppName, labels.NewSelector())
		if err != nil {
			return nil, nil, err
		}
		allTransportServers = append(allTransportServers, transportservers...)
	}

	//gather endpoints
	return g.fetchGlobalDNSEndpointsForF5(allVirtualServers, allTransportServers, obj)
}

func (g *UserGlobalDNSController) reconcileProjectsF5(obj *v3.GlobalDNS) ([]string, []VirtualServerInfo, error) {
	// go through target projects which are part of the current cluster and find all virtualservers and transportservers
	var allVirtualServers []*f5cisv1.VirtualServer

	var allTransportServers []*f5cisv1.TransportServer

	allNamespaces, err := g.namespaceLister.List("", labels.NewSelector())
	if err != nil {
		return nil, nil, fmt.Errorf("UserGlobalDNSController: Error listing cluster namespaces %v", err)
	}

	for _, projectNameSet := range obj.Spec.ProjectNames {
		split := strings.SplitN(projectNameSet, ":", 2)
		if len(split) != 2 {
			return nil, nil, fmt.Errorf("UserGlobalDNSController: Error in splitting project Name %v", projectNameSet)
		}
		// check if the project in this iteration belongs to the same cluster in current context
		if split[0] != g.clusterName {
			continue
		}
		projectID := split[1]
		//list all namespaces in this project and list all ingresses within each namespace
		var namespacesInProject []string
		for _, namespace := range allNamespaces {
			nameSpaceProject := namespace.ObjectMeta.Labels[projectSelectorLabel]
			if strings.EqualFold(projectID, nameSpaceProject) {
				namespacesInProject = append(namespacesInProject, namespace.Name)
			}
		}
		for _, namespace := range namespacesInProject {
			virtualservers, err := g.virtualServerLister.List(namespace, labels.NewSelector())
			if err != nil {
				return nil, nil, err
			}
			allVirtualServers = append(allVirtualServers, virtualservers...)

			transportservers, err := g.transportServerLister.List(namespace, labels.NewSelector())
			if err != nil {
				return nil, nil, err
			}

			allTransportServers = append(allTransportServers, transportservers...)
		}
	}
	//gather endpoints
	return g.fetchGlobalDNSEndpointsForF5(allVirtualServers, allTransportServers, obj)
}

func (g *UserGlobalDNSController) fetchGlobalDNSEndpointsForF5(virtualServers []*f5cisv1.VirtualServer, transportServers []*f5cisv1.TransportServer, obj *v3.GlobalDNS) ([]string, []VirtualServerInfo, error) {
	if len(virtualServers) == 0 {
		return nil, nil, nil
	}

	var vsInfos []VirtualServerInfo
	var allEndpoints []string
	//gather endpoints from all virtualservers
	for _, vs := range virtualServers {
		if gdns, ok := vs.Annotations[annotationGlobalDNS]; ok {
			// check if the globalDNS in annotation is same as the FQDN set on the GlobalDNS
			if gdns != obj.Spec.FQDN {
				continue
			}
			vsInfo := VirtualServerInfo{}
			vsInfo.Name = vs.Spec.VirtualServerName
			vsPort := strconv.Itoa((int)(vs.Spec.VirtualServerHTTPPort))
			if vs.Spec.TLSProfileName != "" {
				vsPort = strconv.Itoa((int)(vs.Spec.VirtualServerHTTPSPort))
			}
			vsInfo.Destination = fmt.Sprintf("%s:%s", vs.Spec.VirtualServerAddress, vsPort)
			vsInfos = append(vsInfos, vsInfo)
			vsep := vs.Spec.VirtualServerAddress
			allEndpoints = append(allEndpoints, vsep)
		}
	}

	//gather endpoints from all transportservers
	for _, ts := range transportServers {
		if gdns, ok := ts.Annotations[annotationGlobalDNS]; ok {
			// check if the globalDNS in annotation is same as the FQDN set on the GlobalDNS
			if gdns != obj.Spec.FQDN {
				continue
			}
			vsInfo := VirtualServerInfo{}
			vsInfo.Name = ts.Spec.VirtualServerName
			vsInfo.Destination = fmt.Sprintf("%s:%s", ts.Spec.VirtualServerAddress, strconv.Itoa((int)(ts.Spec.VirtualServerPort)))
			vsInfos = append(vsInfos, vsInfo)
			vsep := ts.Spec.VirtualServerAddress
			allEndpoints = append(allEndpoints, vsep)
		}
	}
	return allEndpoints, vsInfos, nil
}

func (g *UserGlobalDNSController) getVirtualServerInfos(vsInfos []VirtualServerInfo, vsInfoAnno string) (string, error) {

	existedInfos := map[string][]VirtualServerInfo{}
	if vsInfoAnno != "" {
		err := json.Unmarshal(([]byte)(vsInfoAnno), &existedInfos)
		if err != nil {
			return "", err
		}
	}

	existedInfos[g.clusterName] = vsInfos

	infoBytes, err := json.Marshal(existedInfos)
	if err != nil {
		return "", nil
	}
	return (string)(infoBytes), nil

}
