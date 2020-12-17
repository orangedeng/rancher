package crossclusterclone

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/norman/types/values"
	"github.com/rancher/rancher/pkg/api/store/storageclass"
	"github.com/rancher/rancher/pkg/clustermanager"
	"github.com/rancher/rancher/pkg/resourcequota"
	apps "github.com/rancher/types/apis/apps/v1"
	batchv1 "github.com/rancher/types/apis/batch/v1"
	batchv1beta1 "github.com/rancher/types/apis/batch/v1beta1"
	clusterschema "github.com/rancher/types/apis/cluster.cattle.io/v3/schema"
	v1 "github.com/rancher/types/apis/core/v1"
	mgmntv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	clusterclient "github.com/rancher/types/client/cluster/v3"
	"github.com/sirupsen/logrus"
	api "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	quota "k8s.io/kubernetes/pkg/quota/v1"
)

type Validator struct {
	ClusterManager *clustermanager.Manager
}

func (v *Validator) Validator(request *types.APIContext, schema *types.Schema, data map[string]interface{}) error {
	if request.Method == http.MethodPost {
		originContext := request.SubContext["/v3/schemas/project"]

		target := data["target"]
		cloneTarget := convert.ToMapInterface(target)
		projectID := convert.ToString(cloneTarget["project"])
		request.SubContext = map[string]string{
			"/v3/schemas/project": projectID,
		}
		ns := convert.ToString(cloneTarget["namespace"])

		// check permission
		err := isPermissionFit(request, schema, projectID, ns, data)
		if err != nil {
			return err
		}

		pvcList := convert.ToMapSlice(data["pvcList"])
		if len(pvcList) > 0 {
			// pvc data validation
			parts := strings.SplitN(projectID, ":", 2)
			if len(parts) == 2 {
				clusterName := parts[0]
				c, err := v.ClusterManager.UserContext(clusterName)
				if err != nil {
					return err
				}
				for _, pvc := range pvcList {
					storageClassID, _ := pvc["storageClassId"].(string)
					if storageClassID != "" {
						storageClass, err := c.Storage.StorageClasses("").Get(storageClassID, metav1.GetOptions{})
						if err != nil {
							return err
						}
						if storageClass.Provisioner == storageclass.AzureDisk {
							if storageClass.Parameters[storageclass.StorageAccountType] == "" && storageClass.Parameters[storageclass.SkuName] == "" {
								return httperror.NewAPIError(httperror.InvalidBodyContent, fmt.Sprintf("invalid storage class [%s]: must provide "+
									"storageaccounttype or skuName", storageClass.Name))
							}
						}
					}
				}
			}
		}

		// check quota
		isFit, err := isQuotaSufficient(request, ns, data)
		if !isFit {
			return httperror.NewAPIError(httperror.PermissionDenied, err.Error())
		}

		request.SubContext = map[string]string{
			"/v3/schemas/project": originContext,
		}
	}
	return nil
}

func isPermissionFit(request *types.APIContext, schema *types.Schema, project, ns string, data map[string]interface{}) error {
	// check create workload permission
	workloadData := convert.ToMapInterface(data["workload"])
	err := checkWorkloadPermission(request, schema, workloadData, ns)
	if err != nil {
		return err
	}

	// check related resource permission
	secretData := convert.ToMapSlice(data["secretList"])
	credData := convert.ToMapSlice(data["credentialList"])
	certData := convert.ToMapSlice(data["certificateList"])
	secretData = append(secretData, credData...)
	secretData = append(secretData, certData...)
	if len(secretData) > 0 {
		secretState := map[string]interface{}{
			"name":        secretData[0]["name"],
			"namespaceId": ns,
		}
		if err := request.AccessControl.CanDo(v1.SecretGroupVersionKind.Group, v1.SecretResource.Name, "create", request, secretState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create secrets in target namespace: %s", ns))
		}
	}

	configData := convert.ToMapSlice(data["configMapList"])
	if len(configData) > 0 {
		configMapState := map[string]interface{}{
			"name":        configData[0]["name"],
			"namespaceId": ns,
		}
		if err := request.AccessControl.CanDo(v1.ConfigMapGroupVersionKind.Group, v1.ConfigMapResource.Name, "create", request, configMapState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create configMaps in target namespace: %s", ns))
		}
	}

	pvcData := convert.ToMapSlice(data["pvcList"])
	if len(pvcData) > 0 {
		pvcState := map[string]interface{}{
			"name":        pvcData[0]["name"],
			"namespaceId": ns,
		}
		if err := request.AccessControl.CanDo(v1.PersistentVolumeClaimGroupVersionKind.Group, v1.PersistentVolumeClaimResource.Name, "create", request, pvcState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create pvc in target namespace: %s", ns))
		}
	}

	svcData := convert.ToMapSlice(data["serviceList"])
	if len(svcData) > 0 {
		svcState := map[string]interface{}{
			"name":        svcData[0]["name"],
			"namespaceId": ns,
		}
		if err := request.AccessControl.CanDo(v1.ServiceGroupVersionKind.Group, v1.ServiceResource.Name, "create", request, svcState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create service in target namespace: %s", ns))
		}
	}

	return nil
}

func checkWorkloadPermission(request *types.APIContext, schema *types.Schema, workloadData map[string]interface{}, ns string) error {
	workloadState := map[string]interface{}{
		"name":        workloadData["name"],
		"namespaceId": ns,
	}
	if _, ok := workloadData["deploymentConfig"]; ok {
		if err := request.AccessControl.CanDo(apps.DeploymentGroupVersionKind.Group, apps.DeploymentResource.Name, "create", request, workloadState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create Deployments in target namespace: %s", ns))
		}
	} else if _, ok := workloadData["daemonSetConfig"]; ok {
		if err := request.AccessControl.CanDo(apps.DaemonSetGroupVersionKind.Group, apps.DaemonSetResource.Name, "create", request, workloadState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create DaemonSets in target namespace: %s", ns))
		}
	} else if _, ok := workloadData["statefulSetConfig"]; ok {
		if err := request.AccessControl.CanDo(apps.StatefulSetGroupVersionKind.Group, apps.StatefulSetResource.Name, "create", request, workloadState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create StatefulSets in target namespace: %s", ns))
		}
	} else if _, ok := workloadData["jobConfig"]; ok {
		if err := request.AccessControl.CanDo(batchv1.JobGroupVersionKind.Group, batchv1.JobResource.Name, "create", request, workloadState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create Jobs in target namespace: %s", ns))
		}
	} else if _, ok := workloadData["cronJobConfig"]; ok {
		if err := request.AccessControl.CanDo(batchv1beta1.CronJobGroupVersionKind.Group, batchv1beta1.CronJobResource.Name, "create", request, workloadState, schema); err != nil {
			return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create CronJobs in target namespace: %s", ns))
		}
	}

	// check default service permission
	svcState := map[string]interface{}{
		"name":        workloadData["name"],
		"namespaceId": ns,
	}
	if err := request.AccessControl.CanDo(v1.ServiceGroupVersionKind.Group, v1.ServiceResource.Name, "create", request, svcState, schema); err != nil {
		return httperror.NewAPIError(httperror.PermissionDenied, fmt.Sprintf("has no permission to create service in target namespace: %s", ns))
	}

	return nil
}

func isQuotaSufficient(request *types.APIContext, ns string, data map[string]interface{}) (bool, error) {
	targetNS := &clusterclient.Namespace{}
	err := access.ByID(request, &clusterschema.Version, clusterclient.NamespaceType, ns, targetNS)
	if err != nil {
		return false, err
	}
	// skip quota check if there's no quota limit on target ns
	if targetNS.ResourceQuota == nil {
		return true, nil
	}

	quotaLimit, nsLimitQuota, err := getNamespacedLimitQuota(targetNS)
	if err != nil {
		return false, err
	}
	nsUsedQuota, err := getNamespacedUsedQuota(targetNS)
	if err != nil {
		return false, err
	}

	validateQuota := api.ResourceList{}

	// summary workload related quota
	workloadQuota, err := summaryWorkloadRelatedQuota(data, quotaLimit)
	if err != nil {
		return false, err
	}
	validateQuota = quota.Add(validateQuota, workloadQuota)

	// check quota for secrets, configmaps etc.
	preResourceQuota := summaryRelatedResourceQuota(data)
	validateQuota = quota.Add(validateQuota, preResourceQuota)

	if len(validateQuota) == 0 {
		// if there's no new quota resource to add, skip check
		return true, nil
	}

	for key, quotaValue := range nsUsedQuota {
		if _, ok := validateQuota[key]; ok {
			validateUsedQuota := api.ResourceList{}
			validateUsedQuota[key] = quotaValue
			validateQuota = quota.Add(validateQuota, validateUsedQuota)
		}
	}

	allowed, exceeded := quota.LessThanOrEqual(validateQuota, nsLimitQuota)
	if allowed {
		return true, nil
	}

	return false, errors.New(fmt.Sprintf("There's no enough quota %v available for target namespace %v", exceeded, ns))
}

func generateDNSName(workloadName, dnsName string) bool {
	if dnsName == "" {
		return true
	}
	// regenerate the name in case port type got changed
	if strings.EqualFold(dnsName, workloadName) || strings.HasPrefix(dnsName, fmt.Sprintf("%s-", workloadName)) {
		return true
	}
	return false
}

func getNamespacedLimitQuota(namespace *clusterclient.Namespace) (*mgmntv3.ResourceQuotaLimit, api.ResourceList, error) {
	quota := namespace.ResourceQuota.Limit
	quotaLimit := &mgmntv3.ResourceQuotaLimit{}
	err := convert.ToObj(quota, quotaLimit)
	if err != nil {
		return nil, nil, err
	}
	nsLimitQuota, err := resourcequota.ConvertLimitToResourceList(quotaLimit)
	if err != nil {
		return nil, nil, err
	}
	return quotaLimit, nsLimitQuota, nil
}

func getNamespacedUsedQuota(namespace *clusterclient.Namespace) (api.ResourceList, error) {
	nsUsedQuota := api.ResourceList{}
	if quotaUsage, ok := namespace.Annotations["field.cattle.io/resourceQuotaUsage"]; ok {
		nsLimit := &mgmntv3.ResourceQuotaLimit{}
		err := json.Unmarshal([]byte(quotaUsage), nsLimit)
		if err != nil {
			logrus.Errorf("can't convert [%v] resource quota usage value %v to ResourceQuotaLimit", namespace.Name, quotaUsage)
			return nil, err
		}
		nsUsedQuota, err = resourcequota.ConvertLimitToResourceList(nsLimit)
		if err != nil {
			return nil, err
		}
	}
	return nsUsedQuota, nil
}

func summaryRelatedResourceQuota(data map[string]interface{}) api.ResourceList {
	preResourceQuota := api.ResourceList{}
	// summary all secrets
	var secretCount int
	secretList := convert.ToMapSlice(data["secretList"])
	if len(secretList) > 0 {
		secretCount += len(secretList)
	}
	credentialList := convert.ToMapSlice(data["credentialList"])
	if len(credentialList) > 0 {
		secretCount += len(credentialList)
	}
	certificateList := convert.ToMapSlice(data["certificateList"])
	if len(certificateList) > 0 {
		secretCount += len(certificateList)
	}
	if secretCount > 0 {
		preResourceQuota[clusterclient.ResourceQuotaLimitFieldSecrets] = *resource.NewQuantity(int64(secretCount), resource.DecimalSI)
	}
	// summary configmaps
	configMapList := convert.ToMapSlice(data["configMapList"])
	if len(configMapList) > 0 {
		preResourceQuota[clusterclient.ResourceQuotaLimitFieldConfigMaps] = *resource.NewQuantity(int64(len(configMapList)), resource.DecimalSI)
	}
	// summary pvcs
	pvcList := convert.ToMapSlice(data["pvcList"])
	if len(pvcList) > 0 {
		preResourceQuota[clusterclient.ResourceQuotaLimitFieldPersistentVolumeClaims] = *resource.NewQuantity(int64(len(pvcList)), resource.DecimalSI)
	}
	return preResourceQuota
}

func summaryWorkloadRelatedQuota(data map[string]interface{}, quotaLimit *mgmntv3.ResourceQuotaLimit) (api.ResourceList, error) {
	workloadData := convert.ToMapInterface(data["workload"])
	containerData := convert.ToMapSlice(workloadData["containers"])
	serviceData := convert.ToMapSlice(data["serviceList"])
	workloadQuota := api.ResourceList{}
	for _, c := range containerData {
		// check container resource limit
		if r, ok := c["resources"]; ok {
			conResource := convert.ToMapInterface(r)
			containerResourceQuota, err := getContainerResource(conResource, quotaLimit)
			if err != nil {
				return nil, err
			}
			workloadQuota = quota.Add(workloadQuota, containerResourceQuota)
		} else {
			return nil, errors.New("Invalid resource: invalid resource quota setting")
		}

		// summary services quota
		cMap, err := convert.EncodeToMap(c)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Failed to transform container to map: %v", err))
		}
		v, ok := values.GetValue(cMap, "ports")
		var serviceNum int64
		if ok && v != nil {
			portQuota := api.ResourceList{}
			ports := convert.ToInterfaceSlice(v)
			var nodePortCount, loadBalancerCount int64
			usedNames := map[string]bool{}
			for _, p := range ports {
				port, err := convert.EncodeToMap(p)
				if err != nil {
					logrus.Warnf("Failed to transform port to map %v", err)
					continue
				}
				switch kind := convert.ToString(port["kind"]); kind {
				case "NodePort":
					nodePortCount++
				case "LoadBalancer":
					loadBalancerCount++
				}
				if generateDNSName(convert.ToString(workloadData["name"]), convert.ToString(port["dnsName"])) {
					var dnsName string
					if port["kind"] == "ClusterIP" {
						// use workload name for clusterIP service as it will be used by dns resolution
						dnsName = strings.ToLower(convert.ToString(workloadData["name"]))
					} else {
						dnsName = fmt.Sprintf("%s-%s", strings.ToLower(convert.ToString(workloadData["name"])),
							strings.ToLower(convert.ToString(port["kind"])))
					}
					if _, ok := usedNames[dnsName]; !ok {
						usedNames[dnsName] = true
					}
				}
			}
			serviceNum = int64(len(usedNames)) + int64(len(serviceData))
			portQuota[clusterclient.ResourceQuotaLimitFieldServicesNodePorts] = *resource.NewQuantity(nodePortCount, resource.DecimalSI)
			portQuota[clusterclient.ResourceQuotaLimitFieldServicesLoadBalancers] = *resource.NewQuantity(loadBalancerCount, resource.DecimalSI)
			workloadQuota = quota.Add(workloadQuota, portQuota)
		} else {
			// if there's no port, rancher will create a default service
			serviceNum = int64(len(serviceData)) + 1
		}
		workloadQuota[clusterclient.ResourceQuotaLimitFieldServices] = *resource.NewQuantity(serviceNum, resource.DecimalSI)
	}

	return workloadQuota, nil
}

func getContainerResource(conResource map[string]interface{}, quotaLimit *mgmntv3.ResourceQuotaLimit) (api.ResourceList, error) {
	containerResourceQuota := api.ResourceList{}
	if _, ok := conResource["limits"]; !ok && (quotaLimit.LimitsCPU != "" || quotaLimit.LimitsMemory != "") {
		return nil, errors.New("Invalid resource: Missing resources limits quota setting")
	}
	if _, ok := conResource["requests"]; !ok && (quotaLimit.RequestsCPU != "" || quotaLimit.RequestsMemory != "") {
		return nil, errors.New("Invalid resource: Missing resources requests quota setting")
	}
	limitResource := convert.ToMapInterface(conResource["limits"])
	if limitCPU, ok := limitResource["cpu"].(string); !ok && quotaLimit.LimitsCPU != "" {
		return nil, errors.New("Invalid resource: Missing limits cpu setting")
	} else if ok && quotaLimit.LimitsCPU != "" {
		conLimitCPU, err := resource.ParseQuantity(limitCPU)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Invalid resource: invalid cpu value %v, error: %v", limitCPU, err))
		}
		containerResourceQuota[clusterclient.ResourceQuotaLimitFieldLimitsCPU] = conLimitCPU
	}
	if limitMemory, ok := limitResource["memory"].(string); !ok && quotaLimit.LimitsMemory != "" {
		return nil, errors.New("Invalid resource: Missing limits memory setting")
	} else if ok && quotaLimit.LimitsMemory != "" {
		conLimitMemory, err := resource.ParseQuantity(limitMemory)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Invalid resource: invalid memory value %v, error: %v", limitMemory, err))
		}
		containerResourceQuota[clusterclient.ResourceQuotaLimitFieldLimitsMemory] = conLimitMemory
	}
	requestResource := convert.ToMapInterface(conResource["requests"])
	if requestCPU, ok := requestResource["cpu"].(string); !ok && quotaLimit.RequestsCPU != "" {
		return nil, errors.New("Invalid resource: Missing requests cpu setting")
	} else if ok && quotaLimit.RequestsCPU != "" {
		conRequestCPU, err := resource.ParseQuantity(requestCPU)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Invalid resource: invalid request cpu value %v, error: %v", requestCPU, err))
		}
		containerResourceQuota[clusterclient.ResourceQuotaLimitFieldRequestsCPU] = conRequestCPU
	}
	if requestMemory, ok := requestResource["memory"].(string); !ok && quotaLimit.RequestsMemory != "" {
		return nil, errors.New("Invalid resource: Missing requests memory setting")
	} else if ok && quotaLimit.RequestsMemory != "" {
		conRequestMemory, err := resource.ParseQuantity(requestMemory)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Invalid resource: invalid request memory value %v, error %v", requestMemory, err))
		}
		containerResourceQuota[clusterclient.ResourceQuotaLimitFieldRequestsMemory] = conRequestMemory
	}
	return containerResourceQuota, nil
}
