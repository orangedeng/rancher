package crossclusterclone

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/norman/types/values"
	"github.com/rancher/rancher/pkg/clustermanager"
	v1 "github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/apis/project.cattle.io/v3/schema"
	client "github.com/rancher/types/client/project/v3"
	projectclient "github.com/rancher/types/client/project/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/credentialprovider"
)

type cloneStore struct {
	Store                           types.Store
	SecretStore                     types.Store
	NamespacedSecretStore           types.Store
	DockerCredentialStore           types.Store
	NamespacedDockerCredentialStore types.Store
	CertificateStore                types.Store
	NamespacedCertificateStore      types.Store
	WorkloadStore                   types.Store
	ConfigMapStore                  types.Store
	PersistentVolumeClaimStore      types.Store
	IngressStore                    types.Store
	MSecretClient                   v1.SecretInterface
	ClusterManager                  *clustermanager.Manager
}

func NewCrossClusterCloneStore(schemas *types.Schemas, mgmt *config.ScaledContext, cluster *clustermanager.Manager) {
	schema := schemas.Schema(&schema.Version, client.CloneAppType)
	secretSchema := schemas.Schema(&schema.Version, client.SecretType)
	nsSecretSchema := schemas.Schema(&schema.Version, client.NamespacedSecretType)
	dockerCredentialSchema := schemas.Schema(&schema.Version, client.DockerCredentialType)
	namespacedDockerCredentialSchema := schemas.Schema(&schema.Version, client.NamespacedDockerCredentialType)
	certificateSchema := schemas.Schema(&schema.Version, client.CertificateType)
	namespacedCertificateSchema := schemas.Schema(&schema.Version, client.NamespacedCertificateType)
	workloadSchema := schemas.Schema(&schema.Version, client.WorkloadType)
	configMapSchema := schemas.Schema(&schema.Version, client.ConfigMapType)
	pvcSchema := schemas.Schema(&schema.Version, client.PersistentVolumeClaimType)
	ingressSchema := schemas.Schema(&schema.Version, client.IngressType)
	s := &cloneStore{
		Store:                           schema.Store,
		SecretStore:                     secretSchema.Store,
		NamespacedSecretStore:           nsSecretSchema.Store,
		DockerCredentialStore:           dockerCredentialSchema.Store,
		NamespacedDockerCredentialStore: namespacedDockerCredentialSchema.Store,
		CertificateStore:                certificateSchema.Store,
		NamespacedCertificateStore:      namespacedCertificateSchema.Store,
		WorkloadStore:                   workloadSchema.Store,
		ConfigMapStore:                  configMapSchema.Store,
		PersistentVolumeClaimStore:      pvcSchema.Store,
		IngressStore:                    ingressSchema.Store,
		MSecretClient:                   mgmt.Core.Secrets(""),
		ClusterManager:                  cluster,
	}
	schema.Store = s
	v := Validator{
		ClusterManager: cluster,
	}
	schema.Validator = v.Validator
}

func (c *cloneStore) Context() types.StorageContext {
	return config.UserStorageContext
}

func (c *cloneStore) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {
	var err error
	var secretResult, nsSecretResult, dockerCredResult, nsDockerCredResult, certificateResult, nsCertificateResult, configMapResult, pvcResult, workloadResult, ingressResult []interface{}
	originContext := apiContext.SubContext["/v3/schemas/project"]

	defer func() {
		if err != nil {
			logrus.Errorf("Clone failed due to error: %v", err)
			rollbackResources := map[types.Store][]interface{}{
				c.SecretStore:                     secretResult,
				c.NamespacedSecretStore:           nsSecretResult,
				c.DockerCredentialStore:           dockerCredResult,
				c.NamespacedDockerCredentialStore: nsDockerCredResult,
				c.CertificateStore:                certificateResult,
				c.NamespacedCertificateStore:      nsCertificateResult,
				c.ConfigMapStore:                  configMapResult,
				c.PersistentVolumeClaimStore:      pvcResult,
				c.WorkloadStore:                   workloadResult,
				c.IngressStore:                    ingressResult,
			}
			rollbackResource(apiContext, schema, rollbackResources)
		}
		apiContext.SubContext = map[string]string{
			"/v3/schemas/project": originContext,
		}
	}()

	// get target project and namespace
	target := data["target"]
	cloneTarget := convert.ToMapInterface(target)
	project := convert.ToString(cloneTarget["project"])
	namespace := convert.ToString(cloneTarget["namespace"])

	// get source docker credential
	creds := convert.ToMapSlice(data["credentialList"])
	err = c.generateCredentialResource(apiContext, creds, projectclient.DockerCredentialType)
	if err != nil {
		return nil, err
	}

	// get source certificate credential
	certs := convert.ToMapSlice(data["certificateList"])
	err = c.generateCredentialResource(apiContext, certs, projectclient.CertificateType)
	if err != nil {
		return nil, err
	}

	apiContext.SubContext = map[string]string{
		"/v3/schemas/project": project,
	}

	// create related secrets
	secretList := convert.ToMapSlice(data["secretList"])
	projectSecrets, nsSecrets := separateNamespacedResources(secretList)
	secretResult, err = createRelatedResource(apiContext, projectSecrets, projectclient.SecretType, project, namespace)
	if err != nil {
		return nil, err
	}

	nsSecretResult, err = createRelatedResource(apiContext, nsSecrets, projectclient.NamespacedSecretType, project, namespace)
	if err != nil {
		return nil, err
	}

	projectCreds, nsCreds := separateNamespacedResources(creds)
	dockerCredResult, err = createRelatedResource(apiContext, projectCreds, projectclient.DockerCredentialType, project, namespace)
	if err != nil {
		return nil, err
	}

	nsDockerCredResult, err = createRelatedResource(apiContext, nsCreds, projectclient.NamespacedDockerCredentialType, project, namespace)
	if err != nil {
		return nil, err
	}

	// related certificate
	projectCerts, nsCerts := separateNamespacedResources(certs)
	certificateResult, err = createRelatedResource(apiContext, projectCerts, projectclient.CertificateType, project, namespace)
	if err != nil {
		return nil, err
	}

	nsCertificateResult, err = createRelatedResource(apiContext, nsCerts, projectclient.NamespacedCertificateType, project, namespace)
	if err != nil {
		return nil, err
	}

	// create related configmap
	configMaps := data["configMapList"]
	configMapList := convert.ToMapSlice(configMaps)
	configMapResult, err = createRelatedResource(apiContext, configMapList, projectclient.ConfigMapType, project, namespace)
	if err != nil {
		return nil, err
	}

	// create related pvc
	pvcs := data["pvcList"]
	pvcList := convert.ToMapSlice(pvcs)
	pvcResult, err = createRelatedResource(apiContext, pvcList, projectclient.PersistentVolumeClaimType, project, namespace)
	if err != nil {
		return nil, err
	}

	// create workload
	workload := convert.ToMapInterface(data["workload"])
	workload[client.AppFieldNamespaceId] = namespace
	workload[client.AppFieldProjectID] = project
	// update related pvc id
	if _, ok := workload["volumes"]; ok {
		volumes := convert.ToMapSlice(workload["volumes"])
		for _, v := range volumes {
			if _, ok := v["persistentVolumeClaim"]; ok {
				relatedVolume := values.GetValueN(convert.ToMapInterface(v["persistentVolumeClaim"]), "persistentVolumeClaimId")
				volumeID := convert.ToString(relatedVolume)
				var volumeName string
				vIDs := strings.Split(volumeID, ":")
				if len(vIDs) == 2 {
					volumeName = vIDs[1]
				} else {
					volumeName = vIDs[0]
				}
				for _, newPVC := range pvcResult {
					newDataMap, err := convert.EncodeToMap(newPVC)
					if err != nil {
						logrus.Errorf("Encode pvc %++v to map error: %v", newPVC, err)
						continue
					}
					if convert.ToString(newDataMap["name"]) == volumeName {
						values.PutValue(convert.ToMapInterface(v["persistentVolumeClaim"]), convert.ToString(newDataMap["id"]), "persistentVolumeClaimId")
						break
					}
				}
			}
		}
	}

	w := &projectclient.Workload{}
	err = access.Create(apiContext, &schema.Version, projectclient.WorkloadType, workload, w)
	if err != nil {
		return nil, err
	}
	workloadResult = append(workloadResult, convert.ToMapInterface(w))

	// create ingress
	ingressList := convert.ToMapSlice(data["ingressList"])
	// change ingress rules workloadId to new id
	for _, ing := range ingressList {
		rules := convert.ToMapSlice(ing["rules"])
		for _, rule := range rules {
			paths := convert.ToMapSlice(rule["paths"])
			for _, p := range paths {
				if _, ok := p["workloadIds"]; ok {
					p["workloadIds"] = []string{w.ID}
				}
			}
		}
	}
	ingressResult, err = createRelatedResource(apiContext, ingressList, projectclient.IngressType, project, namespace)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func createRelatedResource(apiContext *types.APIContext, data []map[string]interface{}, resourceType, project, namespace string) ([]interface{}, error) {
	result := []interface{}{}
	if len(data) > 0 {
		for _, r := range data {
			r[projectclient.AppFieldProjectID] = project
			if ns, ok := r[projectclient.AppFieldNamespaceId].(string); ok && ns != "" {
				r[projectclient.AppFieldNamespaceId] = namespace
			}
			var resource interface{}
			switch resourceType {
			case projectclient.SecretType:
				resource = &projectclient.Secret{}
			case projectclient.NamespacedSecretType:
				resource = &projectclient.NamespacedSecret{}
			case projectclient.CertificateType:
				resource = &projectclient.Certificate{}
			case projectclient.NamespacedCertificateType:
				resource = &projectclient.NamespacedCertificate{}
			case projectclient.DockerCredentialType:
				resource = &projectclient.DockerCredential{}
			case projectclient.NamespacedDockerCredentialType:
				resource = &projectclient.NamespacedDockerCredential{}
			case projectclient.ConfigMapType:
				resource = &projectclient.ConfigMap{}
			case projectclient.PersistentVolumeClaimType:
				resource = &projectclient.PersistentVolumeClaim{}
			case projectclient.IngressType:
				resource = &projectclient.Ingress{}
			}
			err := access.Create(apiContext, &schema.Version, resourceType, r, resource)
			if err != nil {
				return result, err
			}
			result = append(result, resource)
		}
	}
	return result, nil
}

func (c *cloneStore) generateCredentialResource(apiContext *types.APIContext, data []map[string]interface{}, resourceType string) error {
	if len(data) > 0 {
		switch resourceType {
		case projectclient.DockerCredentialType:
			for _, d := range data {
				registries := convert.ToMapInterface(d["registries"])
				for _, r := range registries {
					if pwdValue, ok := values.GetValue(convert.ToMapInterface(r), "password"); !ok || pwdValue.(string) == "" {
						secret, err := c.getOriginSecret(apiContext, convert.ToMapInterface(d))
						if err != nil {
							return err
						}
						if secret != nil {
							dockerConfigContent := secret.Data[corev1.DockerConfigJsonKey]
							dockerConfig := &credentialprovider.DockerConfigJson{}
							err = json.Unmarshal(dockerConfigContent, dockerConfig)
							if err != nil {
								logrus.Errorf("Convert docker config json key for secret %s/%s error: %v", secret.Namespace, secret.Name, err)
								continue
							}
							dockerConfigAuth := dockerConfig.Auths
							for _, auth := range dockerConfigAuth {
								if auth.Username == values.GetValueN(convert.ToMapInterface(r), "username") {
									values.PutValue(convert.ToMapInterface(r), auth.Password, "password")
								}
							}
						}
					}
				}
			}
		case projectclient.CertificateType:
			for _, d := range data {
				if key, ok := values.GetValue(d, "key"); !ok || key.(string) == "" {
					secret, err := c.getOriginSecret(apiContext, convert.ToMapInterface(d))
					if err != nil {
						return err
					}
					if secret != nil {
						privateKey := secret.Data[corev1.TLSPrivateKeyKey]
						values.PutValue(d, string(privateKey), "key")
					}
				}
			}
		}
	}

	return nil
}

func rollbackResource(apiContext *types.APIContext, schema *types.Schema, rollbackResources map[types.Store][]interface{}) {
	for s, results := range rollbackResources {
		for _, r := range results {
			result, err := convert.EncodeToMap(r)
			if err != nil {
				logrus.Errorf("Rollback resource %++v error: %v", r, err)
				continue
			}
			logrus.Infof("Rollback Clone resource %v, %v", convert.ToString(result["type"]), convert.ToString(result["id"]))
			_, err = s.Delete(apiContext, schema, convert.ToString(result["id"]))
			if err != nil && !apierrors.IsNotFound(err) {
				logrus.Errorf("Rollback resource %v:%v error: %v", convert.ToString(result["type"]), convert.ToString(result["id"]), err)
				continue
			}
		}
	}
}

func separateNamespacedResources(data []map[string]interface{}) ([]map[string]interface{}, []map[string]interface{}) {
	projectResources := []map[string]interface{}{}
	namespacedResources := []map[string]interface{}{}
	for _, r := range data {
		if ns, ok := r[client.AppFieldNamespaceId].(string); ok && ns != "" {
			namespacedResources = append(namespacedResources, r)
		} else {
			projectResources = append(projectResources, r)
		}
	}
	return projectResources, namespacedResources
}

func (c *cloneStore) getOriginSecret(apiContext *types.APIContext, data map[string]interface{}) (*corev1.Secret, error) {
	if namespaceID, ok := data["namespaceId"].(string); ok && namespaceID != "" {
		clusterName := c.ClusterManager.ClusterName(apiContext)
		if clusterName == "" {
			return nil, httperror.NewAPIError(httperror.ServerError, fmt.Sprintf("Cluster name empty"))
		}
		clusterContext, err := c.ClusterManager.UserContext(clusterName)
		if err != nil {
			return nil, httperror.NewAPIError(httperror.ServerError, fmt.Sprintf("Error getting cluster context"))
		}
		clusterSecretClient := clusterContext.Core.Secrets("")
		secret, err := clusterSecretClient.GetNamespaced(namespaceID, convert.ToString(data["name"]), metav1.GetOptions{})
		if err != nil {
			logrus.Errorf("Get secret %v:%v from cluster %s error: %v", namespaceID, convert.ToString(data["name"]), clusterName, err)
			return nil, nil
		}
		return secret, nil
	}

	if fieldProjectID, ok := data["projectId"].(string); ok {
		// get project secret
		projectID := strings.Split(fieldProjectID, ":")
		if len(projectID) == 2 {
			projectNamespace := projectID[1]
			secret, err := c.MSecretClient.GetNamespaced(projectNamespace, convert.ToString(data["name"]), metav1.GetOptions{})
			if err != nil {
				logrus.Errorf("CrossClusterClone: trying to get secrets %v:%v got error: %v", projectNamespace, convert.ToString(data["name"]), err)
				return nil, nil
			}
			return secret, nil
		}
	}
	return nil, nil
}

func (c *cloneStore) ByID(apiContext *types.APIContext, schema *types.Schema, id string) (map[string]interface{}, error) {
	return nil, nil
}

func (c *cloneStore) Update(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, id string) (map[string]interface{}, error) {
	return nil, nil
}

func (c *cloneStore) List(apiContext *types.APIContext, schema *types.Schema, opt *types.QueryOptions) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *cloneStore) Delete(apiContext *types.APIContext, schema *types.Schema, id string) (map[string]interface{}, error) {
	return nil, nil
}

func (c *cloneStore) Watch(apiContext *types.APIContext, schema *types.Schema, opt *types.QueryOptions) (chan map[string]interface{}, error) {
	return nil, nil
}
