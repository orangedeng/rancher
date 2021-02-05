package globaldns

import (
	"encoding/json"

	"github.com/rancher/rancher/pkg/settings"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"k8s.io/apimachinery/pkg/runtime"
)

func (n *ProviderCatalogLauncher) handleF5BIGIPProvider(obj *v3.GlobalDNSProvider) (runtime.Object, error) {
	rancherInstallUUID := settings.InstallUUID.Get()
	// create external-dns bigipf5 provider
	deviceIps, err := json.Marshal(obj.Spec.F5BIGIPProviderConfig.F5BIGIPDeviceIPS)
	if err != nil {
		return nil, err
	}
	answers := map[string]string{
		"provider":                         "f5bigip",
		"f5bigip.f5_bigip_host":            obj.Spec.F5BIGIPProviderConfig.F5BIGIPHost,
		"f5bigip.f5_bigip_port":            obj.Spec.F5BIGIPProviderConfig.F5BIGIPPort,
		"f5bigip.f5_bigip_user":            obj.Spec.F5BIGIPProviderConfig.F5BIGIPUser,
		"f5bigip.f5_bigip_passwd":          obj.Spec.F5BIGIPProviderConfig.F5BIGIPPasswd,
		"f5bigip.f5_bigip_datacenter_name": obj.Spec.F5BIGIPProviderConfig.F5BIGIPDatacenterName,
		"f5bigip.f5_bigip_server_name":     obj.Spec.F5BIGIPProviderConfig.F5BIGIPServerName,
		"f5bigip.f5_bigip_device_ips":      string(deviceIps),
		"txtOwnerId":                       rancherInstallUUID + "_" + obj.Name,
		"rbac.create":                      "true",
		"policy":                           "sync",
		"registry":                         "noop",
	}

	if obj.Spec.RootDomain != "" {
		answers["domainFilters[0]"] = obj.Spec.RootDomain
	}

	return n.createUpdateExternalDNSApp(obj, answers)
}
