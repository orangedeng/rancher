package clusterprovisioner

// PANDARIA: networkaddons hijack cluster provisioning process to change rke config for some extra addons

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/rancher/rancher/pkg/image"
	"github.com/rancher/rancher/pkg/settings"
	cv1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	pluginMultusFlannel = "multus-flannel-macvlan"
	pluginMultusCanal   = "multus-canal-macvlan"
	extraPluginName     = "pandariaExtraPluginName"
	pathTemplateEnv     = "NETWORK_ADDONS_TEMPLATE_DIR"
	pathConfigEnv       = "NETWORK_ADDONS_DIR"
	PluginConfigName    = "rke-user-includes-addons"
)

func (p *Provisioner) handleNetworkPlugin(old v3.ClusterSpec, clusterName string, updateClusterConfigFile bool) (v3.ClusterSpec, error) {
	spec := old.DeepCopy()

	pluginName := GetAddonsName(*spec)
	if pluginName == "" {
		return *spec, nil
	}

	return HandleNetworkPlugin(spec, clusterName, pluginName, updateClusterConfigFile, p.NetworkAddonsConfigLister, p.NetworkAddonsConfig)
}

func HandleNetworkPlugin(spec *v3.ClusterSpec, clusterName, networkAddonName string, updateClusterConfigFile bool, networkAddonsConfigLister cv1.ConfigMapLister, networkAddonsConfig cv1.ConfigMapInterface) (v3.ClusterSpec, error) {
	clusterConfigPath := fmt.Sprintf("%s.%s", fmt.Sprintf("%s%s%s.yaml", os.Getenv(pathConfigEnv), string(os.PathSeparator), networkAddonName), clusterName)

	var (
		configContent string
		err           error
	)
	if updateClusterConfigFile {
		configContent, err = GetLatestTemplate(networkAddonName)
		if err != nil {
			return v3.ClusterSpec{}, err
		}
		// create or update config map
		configMap, queryErr := networkAddonsConfigLister.Get(clusterName, PluginConfigName)
		canUpdateConfigMap := true
		if queryErr != nil {
			if k8serrors.IsNotFound(queryErr) {
				canUpdateConfigMap = false
			} else {
				return v3.ClusterSpec{}, queryErr
			}
		}

		if configMap != nil && canUpdateConfigMap {
			configMap.Data[networkAddonName] = configContent
			if _, err := networkAddonsConfig.Update(configMap); err != nil {
				return v3.ClusterSpec{}, err
			}
		} else {
			if _, err := networkAddonsConfig.Create(newNetWorkAddonsConfigMap(clusterName, networkAddonName, configContent)); err != nil {
				return v3.ClusterSpec{}, err
			}
		}
	} else {
		configMap, queryErr := networkAddonsConfigLister.Get(clusterName, PluginConfigName)
		if queryErr != nil {
			return v3.ClusterSpec{}, fmt.Errorf("network addons error when query config: %s", queryErr.Error())
		}
		configContent = configMap.Data[networkAddonName]
	}

	var (
		content string
		cfg     = spec.RancherKubernetesEngineConfig
	)
	switch networkAddonName {
	case pluginMultusFlannel:
		content = applyMultusFlannelOption(configContent, cfg.Network.Options)
	case pluginMultusCanal:
		content = applyMultusCanalOption(configContent, cfg.Network.Options)
	}

	rkeRegistry := getDefaultRKERegistry(cfg.PrivateRegistries)
	logrus.Debugf("networkaddons: got rke registry: %s", rkeRegistry)
	if rkeRegistry != "" {
		content = resolveRKERegistry(content, rkeRegistry)
	} else {
		content = resolveSystemRegistry(content)
	}
	content = resolveControllerClusterCIDR(cfg.Services.KubeController.ClusterCIDR, content)

	// update cluster config file
	if err := ioutil.WriteFile(clusterConfigPath, []byte(content), 0644); err != nil {
		logrus.Errorf("networkaddons: %v", err)
		return v3.ClusterSpec{}, err
	}

	// rewrite network option and insert addons_include
	cfg.Network.Plugin = "none"
	cfg.Network.Options[extraPluginName] = networkAddonName

	if cfg.AddonsInclude == nil {
		cfg.AddonsInclude = []string{}
	} else {
		cfg.AddonsInclude = removeContainString(cfg.AddonsInclude, networkAddonName)
	}
	cfg.AddonsInclude = append([]string{clusterConfigPath}, cfg.AddonsInclude...)

	// add certs args
	setCertsArgs(cfg)
	return *spec, nil
}

// GetAddonsName will return the network addons name if possible
func GetAddonsName(spec v3.ClusterSpec) string {
	if spec.RancherKubernetesEngineConfig != nil {
		switch spec.RancherKubernetesEngineConfig.Network.Plugin {
		case pluginMultusFlannel:
			return pluginMultusFlannel
		case pluginMultusCanal:
			return pluginMultusCanal
		case "none":
			switch spec.RancherKubernetesEngineConfig.Network.Options[extraPluginName] {
			case pluginMultusFlannel:
				return pluginMultusFlannel
			case pluginMultusCanal:
				return pluginMultusCanal
			}
		}
	}
	return ""
}

// GetLatestTemplate will return []byte of the latest template file
func GetLatestTemplate(networkAddonName string) (string, error) {
	templatePath := fmt.Sprintf("%s%s%s.yaml", os.Getenv(pathTemplateEnv), string(os.PathSeparator), networkAddonName)
	if _, err := os.Stat(templatePath); err != nil {
		logrus.Errorf("networkaddons: %v", err)
		return "", err
	}

	b, err := ioutil.ReadFile(templatePath)
	if err != nil {
		logrus.Errorf("networkaddons: %v", err)
		return "", err
	}
	return string(b), nil
}

func applyMultusFlannelOption(addons string, option map[string]string) string {
	if option["flannel_iface"] != "" {
		addons = strings.Replace(addons, "# - --iface=eth0", fmt.Sprintf("- --iface=%s", option["flannel_iface"]), -1)
	}

	return addons
}

func applyMultusCanalOption(addons string, option map[string]string) string {
	if option["canal_iface"] != "" {
		addons = strings.Replace(addons, "canal_iface: \"\"", fmt.Sprintf("canal_iface: \"%s\"", option["canal_iface"]), -1)
	}

	if option["veth_mtu"] != "" {
		mtuValue, err := strconv.Atoi(option["veth_mtu"])
		if err == nil && mtuValue != 0 {
			addons = strings.Replace(addons, "veth_mtu: \"1450\"", fmt.Sprintf("veth_mtu: \"%d\"", mtuValue), -1)
		}
	}

	return addons
}

func replaceImage(origin string) string {
	s := strings.SplitN(origin, ":", 2)
	if len(s) != 2 {
		return origin
	}
	newImage := "image: " + image.Resolve(strings.TrimLeft(s[1], " "))
	logrus.Debugf("origin image: %s, registry prefixed image: %s", origin, newImage)
	return newImage
}

// resolveSystemRegistry find all image field in yaml content
// and replace with new image value which system registry prefixed
func resolveSystemRegistry(content string) string {
	if settings.SystemDefaultRegistry.Get() == "" {
		return content
	}
	exp := `image:.*`
	return regexp.MustCompile(exp).ReplaceAllStringFunc(content, replaceImage)
}

func getDefaultRKERegistry(registries []v3.PrivateRegistry) string {
	var registry string
	for _, reg := range registries {
		if reg.IsDefault {
			registry = reg.URL
			break
		}
	}
	return registry
}

// resolveRKERegistry can add rke registry prefix for the yaml content
func resolveRKERegistry(content, registry string) string {
	exp := `image:.*`
	return regexp.MustCompile(exp).ReplaceAllStringFunc(content, func(origin string) string {
		s := strings.SplitN(origin, ":", 2)
		if len(s) != 2 {
			return origin
		}
		oldImg := strings.TrimLeft(s[1], " ")
		if !strings.HasPrefix(oldImg, registry) {
			res := "image: " + path.Join(registry, oldImg)
			logrus.Debugf("networkaddons: %s replaced by %s", oldImg, res)
			return res
		}

		return origin
	})
}

func resolveControllerClusterCIDR(cidr, content string) string {
	if cidr == "" {
		return content
	}
	_, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return content
	}
	if cidr != "10.42.0.0/16" {
		exp := `"Network": "10.42.0.0/16"`
		return regexp.MustCompile(exp).ReplaceAllStringFunc(content, func(origin string) string {
			s := strings.SplitN(origin, ":", 2)
			if len(s) != 2 {
				return origin
			}
			res := fmt.Sprintf(`"Network": "%s"`, cidr)
			logrus.Debugf("networkaddons: Network cidr replaced by %s", res)
			return res
		})
	}
	return content
}

// setCertsArgs for kube-apiserver allowing admission webhook
func setCertsArgs(cfg *v3.RancherKubernetesEngineConfig) {
	setArg := func(m map[string]string, key string, value string) {
		if m[key] == "" {
			m[key] = value
		}
	}
	if cfg.Services.KubeController.ExtraArgs == nil {
		cfg.Services.KubeController.ExtraArgs = map[string]string{}
	}
	setArg(cfg.Services.KubeController.ExtraArgs, "client-ca-file", "/etc/kubernetes/ssl/kube-ca.pem")
	setArg(cfg.Services.KubeController.ExtraArgs, "cluster-signing-cert-file", "/etc/kubernetes/ssl/kube-ca.pem")
	setArg(cfg.Services.KubeController.ExtraArgs, "cluster-signing-key-file", "/etc/kubernetes/ssl/kube-ca-key.pem")
	setArg(cfg.Services.KubeController.ExtraArgs, "requestheader-client-ca-file", "/etc/kubernetes/ssl/kube-apiserver-requestheader-ca.pem")
}

func removeContainString(in []string, s string) []string {
	out := []string{}
	for _, v := range in {
		if strings.Contains(v, s) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func newNetWorkAddonsConfigMap(clusterName, networkAddonName, configContent string) *v1.ConfigMap {
	return &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind: "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      PluginConfigName,
			Namespace: clusterName,
		},
		Data: map[string]string{networkAddonName: configContent},
	}
}
