package resourcequota

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/ref"
	validate "github.com/rancher/rancher/pkg/resourcequota"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func convertResourceListToLimit(rList corev1.ResourceList) (*v3.ResourceQuotaLimit, error) {
	converted, err := convert.EncodeToMap(rList)
	if err != nil {
		return nil, err
	}

	convertedMap := map[string]interface{}{}
	scPvc := map[string]string{}
	scStorage := map[string]string{}

	for key, value := range converted {
		if strings.HasSuffix(key, validate.StorageClassPVCQuotaSuffix) {
			scName := strings.Split(key, ".")[0]
			scPvc[scName] = convert.ToString(value)
		} else if strings.HasSuffix(key, validate.StorageClassStorageQuotaSuffix) {
			scName := strings.Split(key, ".")[0]
			scStorage[scName] = convert.ToString(value)
		} else {
			convertedMap[key] = convert.ToString(value)
		}
	}

	if len(scStorage) > 0 {
		convertedMap[validate.StorageClassStorageQuotaKey] = scStorage
	}

	if len(scPvc) > 0 {
		convertedMap[validate.StorageClassPVCQuotaKey] = scPvc
	}

	toReturn := &v3.ResourceQuotaLimit{}
	err = convert.ToObj(convertedMap, toReturn)

	return toReturn, err
}

func convertResourceLimitResourceQuotaSpec(limit *v3.ResourceQuotaLimit) (*corev1.ResourceQuotaSpec, error) {
	converted, err := convertProjectResourceLimitToResourceList(limit)
	if err != nil {
		return nil, err
	}
	quotaSpec := &corev1.ResourceQuotaSpec{
		Hard: converted,
	}
	return quotaSpec, err
}

func convertProjectResourceLimitToResourceList(limit *v3.ResourceQuotaLimit) (corev1.ResourceList, error) {
	in, err := json.Marshal(limit)
	if err != nil {
		return nil, err
	}
	limitsMap := map[string]interface{}{}
	err = json.Unmarshal(in, &limitsMap)
	if err != nil {
		return nil, err
	}

	limits := corev1.ResourceList{}
	for key, value := range limitsMap {
		switch value.(type) {
		case string:
			v := value.(string)
			var resourceName corev1.ResourceName
			if val, ok := resourceQuotaConversion[key]; ok {
				resourceName = corev1.ResourceName(val)
			} else {
				resourceName = corev1.ResourceName(key)
			}

			resourceQuantity, err := resource.ParseQuantity(v)
			if err != nil {
				return nil, err
			}

			limits[resourceName] = resourceQuantity
		case map[string]interface{}:
			valuemaps := value.(map[string]interface{})
			for k, v := range valuemaps {
				valueString, ok := v.(string)
				if ok {
					resourceQuantity, err := resource.ParseQuantity(valueString)
					if err != nil {
						return nil, err
					}

					var rn corev1.ResourceName
					if key == validate.StorageClassStorageQuotaKey {
						resourceNameStr := fmt.Sprintf("%s.%s", k, validate.StorageClassStorageQuotaSuffix)
						rn = corev1.ResourceName(resourceNameStr)
					} else if key == validate.StorageClassPVCQuotaKey {
						resourceNameStr := fmt.Sprintf("%s.%s", k, validate.StorageClassPVCQuotaSuffix)
						rn = corev1.ResourceName(resourceNameStr)
					} else {
						rn = corev1.ResourceName(key)
					}

					limits[rn] = resourceQuantity
				}
			}
		default:
		}
	}
	return limits, nil
}

func convertContainerResourceLimitToResourceList(limit *v3.ContainerResourceLimit) (corev1.ResourceList, corev1.ResourceList, error) {
	in, err := json.Marshal(limit)
	if err != nil {
		return nil, nil, err
	}
	limitsMap := map[string]string{}
	err = json.Unmarshal(in, &limitsMap)
	if err != nil {
		return nil, nil, err
	}

	if len(limitsMap) == 0 {
		return nil, nil, nil
	}

	limits := corev1.ResourceList{}
	requests := corev1.ResourceList{}
	for key, value := range limitsMap {
		var resourceName corev1.ResourceName
		request := false
		if val, ok := limitRangerRequestConversion[key]; ok {
			resourceName = corev1.ResourceName(val)
			request = true
		} else if val, ok := limitRangerLimitConversion[key]; ok {
			resourceName = corev1.ResourceName(val)
		}
		if resourceName == "" {
			continue
		}

		resourceQuantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, nil, err
		}
		if request {
			requests[resourceName] = resourceQuantity
		} else {
			limits[resourceName] = resourceQuantity
		}

	}
	return requests, limits, nil
}

var limitRangerRequestConversion = map[string]string{
	"requestsCpu":    "cpu",
	"requestsMemory": "memory",
}

var limitRangerLimitConversion = map[string]string{
	"limitsCpu":    "cpu",
	"limitsMemory": "memory",
}

var resourceQuotaConversion = map[string]string{
	"replicationControllers": "replicationcontrollers",
	"configMaps":             "configmaps",
	"persistentVolumeClaims": "persistentvolumeclaims",
	"servicesNodePorts":      "services.nodeports",
	"servicesLoadBalancers":  "services.loadbalancers",
	"requestsCpu":            "requests.cpu",
	"requestsMemory":         "requests.memory",
	"requestsStorage":        "requests.storage",
	"limitsCpu":              "limits.cpu",
	"limitsMemory":           "limits.memory",
	"requestsGpuMemory":      "requests.rancher.io/gpu-mem", // PANDARIA
	"requestsGpuCount":       "requests.nvidia.com/gpu",     // PANDARIA
}

func getNamespaceResourceQuota(ns *corev1.Namespace) string {
	if ns.Annotations == nil {
		return ""
	}
	return ns.Annotations[resourceQuotaAnnotation]
}

func getNamespaceContainerDefaultResourceLimit(ns *corev1.Namespace) string {
	if ns.Annotations == nil {
		return ""
	}
	return ns.Annotations[limitRangeAnnotation]
}

func getProjectResourceQuotaLimit(ns *corev1.Namespace, projectLister v3.ProjectLister) (*v3.ResourceQuotaLimit, string, error) {
	projectID := getProjectID(ns)
	if projectID == "" {
		return nil, "", nil
	}
	projectNamespace, projectName := getProjectNamespaceName(projectID)
	if projectName == "" {
		return nil, "", nil
	}
	project, err := projectLister.Get(projectNamespace, projectName)
	if err != nil || project.Spec.ResourceQuota == nil {
		return nil, "", err
	}
	return &project.Spec.ResourceQuota.Limit, projectID, nil
}

func getProjectNamespaceDefaultQuota(ns *corev1.Namespace, projectLister v3.ProjectLister) (*v3.NamespaceResourceQuota, error) {
	projectID := getProjectID(ns)
	if projectID == "" {
		return nil, nil
	}
	projectNamespace, projectName := getProjectNamespaceName(projectID)
	if projectName == "" {
		return nil, nil
	}
	project, err := projectLister.Get(projectNamespace, projectName)
	if err != nil || project.Spec.ResourceQuota == nil {
		return nil, err
	}
	return project.Spec.NamespaceDefaultResourceQuota, nil
}

func getProjectContainerDefaultLimit(ns *corev1.Namespace, projectLister v3.ProjectLister) (*v3.ContainerResourceLimit, error) {
	projectID := getProjectID(ns)
	if projectID == "" {
		return nil, nil
	}
	projectNamespace, projectName := ref.Parse(projectID)
	if projectName == "" {
		return nil, nil
	}
	project, err := projectLister.Get(projectNamespace, projectName)
	if err != nil || project.Spec.ResourceQuota == nil {
		return nil, err
	}
	return project.Spec.ContainerDefaultResourceLimit, nil
}

func getNamespaceResourceQuotaLimit(ns *corev1.Namespace) (*v3.ResourceQuotaLimit, error) {
	value := getNamespaceResourceQuota(ns)
	if value == "" {
		return nil, nil
	}
	var nsQuota v3.NamespaceResourceQuota
	err := json.Unmarshal([]byte(convert.ToString(value)), &nsQuota)
	if err != nil {
		return nil, err
	}
	return &nsQuota.Limit, err
}

func getNamespaceContainerResourceLimit(ns *corev1.Namespace) (*v3.ContainerResourceLimit, error) {
	value := getNamespaceContainerDefaultResourceLimit(ns)
	// rework after api framework change is done
	// when annotation field is passed as null, the annotation should be removed
	// instead of being updated with the null value
	if value == "" || value == "null" {
		return nil, nil
	}
	var nsLimit v3.ContainerResourceLimit
	err := json.Unmarshal([]byte(convert.ToString(value)), &nsLimit)
	if err != nil {
		return nil, err
	}
	return &nsLimit, err
}

func getProjectID(ns *corev1.Namespace) string {
	if ns.Annotations != nil {
		return ns.Annotations[projectIDAnnotation]
	}
	return ""
}

func getProjectNamespaceName(projectID string) (string, string) {
	if projectID == "" {
		return "", ""
	}
	parts := strings.Split(projectID, ":")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func convertPodResourceLimitToLimitRangeSpec(podResourceLimit *v3.ContainerResourceLimit) (*corev1.LimitRangeSpec, error) {
	request, limit, err := convertContainerResourceLimitToResourceList(podResourceLimit)
	if err != nil {
		return nil, err
	}
	if request == nil && limit == nil {
		return nil, nil
	}

	item := corev1.LimitRangeItem{
		Type:           corev1.LimitTypeContainer,
		Default:        limit,
		DefaultRequest: request,
	}
	limits := []corev1.LimitRangeItem{item}
	limitRangeSpec := &corev1.LimitRangeSpec{
		Limits: limits,
	}
	return limitRangeSpec, err
}
