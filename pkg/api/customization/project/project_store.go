package project

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/norman/types/values"
	"github.com/rancher/rancher/pkg/clustermanager"
	"github.com/rancher/rancher/pkg/resourcequota"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	mgmtschema "github.com/rancher/types/apis/management.cattle.io/v3/schema"
	mgmtclient "github.com/rancher/types/client/management/v3"
	"github.com/rancher/types/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const roleTemplatesRequired = "authz.management.cattle.io/creator-role-bindings"
const quotaField = "resourceQuota"
const namespaceQuotaField = "namespaceDefaultResourceQuota"

type projectStore struct {
	types.Store
	projectLister      v3.ProjectLister
	roleTemplateLister v3.RoleTemplateLister
	scaledContext      *config.ScaledContext
	clusterLister      v3.ClusterLister
}

func SetProjectStore(schema *types.Schema, mgmt *config.ScaledContext) {
	store := &projectStore{
		Store:              schema.Store,
		projectLister:      mgmt.Management.Projects("").Controller().Lister(),
		roleTemplateLister: mgmt.Management.RoleTemplates("").Controller().Lister(),
		scaledContext:      mgmt,
		clusterLister:      mgmt.Management.Clusters("").Controller().Lister(),
	}
	schema.Store = store
}

func (s *projectStore) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {
	annotation, err := s.createProjectAnnotation()
	if err != nil {
		return nil, err
	}

	if err := s.validateResourceQuota(apiContext, data, ""); err != nil {
		return nil, err
	}

	// PANDARIA
	if err := s.validateProjectDisplayName(apiContext, data, ""); err != nil {
		return nil, err
	}

	values.PutValue(data, annotation, "annotations", roleTemplatesRequired)

	return s.Store.Create(apiContext, schema, data)
}

func (s *projectStore) Update(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, id string) (map[string]interface{}, error) {
	if err := s.validateResourceQuota(apiContext, data, id); err != nil {
		return nil, err
	}

	// PANDARIA
	if err := s.validateProjectDisplayName(apiContext, data, id); err != nil {
		return nil, err
	}

	return s.Store.Update(apiContext, schema, data, id)
}

func (s *projectStore) Delete(apiContext *types.APIContext, schema *types.Schema, id string) (map[string]interface{}, error) {
	parts := strings.Split(id, ":")

	proj, err := s.projectLister.Get(parts[0], parts[len(parts)-1])
	if err != nil {
		return nil, err
	}
	if proj.Labels["authz.management.cattle.io/system-project"] == "true" {
		return nil, httperror.NewAPIError(httperror.MethodNotAllowed, "System Project cannot be deleted")
	}
	return s.Store.Delete(apiContext, schema, id)
}

func (s *projectStore) createProjectAnnotation() (string, error) {
	rt, err := s.roleTemplateLister.List("", labels.NewSelector())
	if err != nil {
		return "", err
	}

	annoMap := make(map[string][]string)

	for _, role := range rt {
		if role.ProjectCreatorDefault && !role.Locked {
			annoMap["required"] = append(annoMap["required"], role.Name)
		}
	}

	d, err := json.Marshal(annoMap)
	if err != nil {
		return "", err
	}

	return string(d), nil
}

func (s *projectStore) validateProjectDisplayName(apiContext *types.APIContext, data map[string]interface{}, id string) error {
	var name, namespace, project string

	// this is update logic.
	if id != "" {
		ss := strings.Split(id, ":")
		if len(ss) != 2 {
			return errors.New("invalid project id")
		}
		namespace = ss[0]
		project = ss[1]
	}

	if namespace == "" {
		if _, ok := data["namespaceId"]; ok {
			namespace = data["namespaceId"].(string)
		} else {
			if _, ok := data["clusterId"]; ok {
				namespace = data["clusterId"].(string)
			}
		}
	}

	if _, ok := data["name"]; ok {
		name = data["name"].(string)
	} else {
		p, err := s.projectLister.Get(namespace, project)
		if err != nil {
			return errors.New("can not find project's name")
		}
		name = p.Spec.DisplayName
	}

	projects, err := s.projectLister.List(namespace, labels.Everything())
	if err != nil {
		return err
	}

	if len(projects) < 1 {
		return nil
	}

	for _, p := range projects {
		if project != "" && project == p.Name {
			continue
		}
		if p.Spec.DisplayName == name {
			return httperror.NewAPIError(httperror.Conflict, "duplicate project name")
		}
	}

	return nil
}

func (s *projectStore) validateResourceQuota(apiContext *types.APIContext, data map[string]interface{}, id string) error {
	quotaO, quotaOk := data[quotaField]
	if quotaO == nil {
		quotaOk = false
	}
	nsQuotaO, namespaceQuotaOk := data[namespaceQuotaField]
	if nsQuotaO == nil {
		namespaceQuotaOk = false
	}
	if quotaOk != namespaceQuotaOk {
		if quotaOk {
			return httperror.NewFieldAPIError(httperror.MissingRequired, namespaceQuotaField, "")
		}
		return httperror.NewFieldAPIError(httperror.MissingRequired, quotaField, "")
	} else if !quotaOk {
		return nil
	}

	var nsQuota mgmtclient.NamespaceResourceQuota
	if err := convert.ToObj(nsQuotaO, &nsQuota); err != nil {
		return err
	}
	var projectQuota mgmtclient.ProjectResourceQuota
	if err := convert.ToObj(quotaO, &projectQuota); err != nil {
		return err
	}

	projectQuotaLimit, err := limitToLimit(projectQuota.Limit)
	if err != nil {
		return err
	}
	nsQuotaLimit, err := limitToLimit(nsQuota.Limit)
	if err != nil {
		return err
	}

	// limits in namespace default quota should include all limits defined in the project quota
	projectQuotaLimitMap, err := convert.EncodeToMap(projectQuotaLimit)
	if err != nil {
		return err
	}

	nsQuotaLimitMap, err := convert.EncodeToMap(nsQuotaLimit)
	if err != nil {
		return err
	}
	if len(nsQuotaLimitMap) != len(projectQuotaLimitMap) {
		return httperror.NewFieldAPIError(httperror.MissingRequired, namespaceQuotaField, fmt.Sprintf("does not have all fields defined on a %s", quotaField))
	}

	for k := range projectQuotaLimitMap {
		if _, ok := nsQuotaLimitMap[k]; !ok {
			return httperror.NewFieldAPIError(httperror.MissingRequired, namespaceQuotaField, fmt.Sprintf("misses %s defined on a %s", k, quotaField))
		}
	}
	return s.isQuotaFit(apiContext, nsQuotaLimit, projectQuotaLimit, id)
}

func (s *projectStore) isQuotaFit(apiContext *types.APIContext, nsQuotaLimit *v3.ResourceQuotaLimit,
	projectQuotaLimit *v3.ResourceQuotaLimit, id string) error {
	// check that namespace default quota is within project quota
	isFit, msg, err := resourcequota.IsQuotaFit(nsQuotaLimit, []*v3.ResourceQuotaLimit{}, projectQuotaLimit)
	if err != nil {
		return err
	}
	if !isFit {
		return httperror.NewFieldAPIError(httperror.MaxLimitExceeded, namespaceQuotaField, fmt.Sprintf("exceeds %s on fields: %s",
			quotaField, msg))
	}

	if id == "" {
		return nil
	}

	var project mgmtclient.Project
	if err := access.ByID(apiContext, &mgmtschema.Version, mgmtclient.ProjectType, id, &project); err != nil {
		return err
	}

	// check if fields were added or removed
	// and update project's namespaces accordingly
	defaultQuotaLimitMap, err := convert.EncodeToMap(nsQuotaLimit)
	if err != nil {
		return err
	}

	usedQuotaLimitMap := map[string]interface{}{}
	if project.ResourceQuota != nil && project.ResourceQuota.UsedLimit != nil {
		usedQuotaLimitMap, err = convert.EncodeToMap(project.ResourceQuota.UsedLimit)
		if err != nil {
			return err
		}
	}

	limitToAdd := map[string]interface{}{}
	limitToRemove := map[string]interface{}{}
	for key, value := range defaultQuotaLimitMap {
		if _, ok := usedQuotaLimitMap[key]; !ok {
			limitToAdd[key] = value
		} else {
			if key == resourcequota.StorageClassPVCQuotaKey ||
				key == resourcequota.StorageClassStorageQuotaKey {
				defaultSCQuotaMap, err := convert.EncodeToMap(value)
				if err != nil {
					return err
				}
				usedSCQuotaMap, err := convert.EncodeToMap(usedQuotaLimitMap[key])
				if err != nil {
					return err
				}
				for k := range defaultSCQuotaMap {
					if _, ok := usedSCQuotaMap[k]; !ok {
						limitToAdd[key] = value
					}
				}
			}
		}
	}

	for key, value := range usedQuotaLimitMap {
		if _, ok := defaultQuotaLimitMap[key]; !ok {
			limitToRemove[key] = value
		} else {
			if key == resourcequota.StorageClassPVCQuotaKey ||
				key == resourcequota.StorageClassStorageQuotaKey {
				defaultSCQuotaMap, err := convert.EncodeToMap(defaultQuotaLimitMap[key])
				if err != nil {
					return err
				}
				usedSCQuotaMap, err := convert.EncodeToMap(value)
				if err != nil {
					return err
				}
				for k := range usedSCQuotaMap {
					if _, ok := defaultSCQuotaMap[k]; !ok {
						limitToRemove[key] = value
					}
				}
			}
		}
	}

	// check that used quota is not bigger than the project quota
	for key := range limitToRemove {
		delete(usedQuotaLimitMap, key)
	}

	var usedLimitToCheck mgmtclient.ResourceQuotaLimit
	err = convert.ToObj(usedQuotaLimitMap, &usedLimitToCheck)
	if err != nil {
		return err
	}

	usedQuotaLimit, err := limitToLimit(&usedLimitToCheck)
	if err != nil {
		return err
	}
	isFit, msg, err = resourcequota.IsQuotaFit(usedQuotaLimit, []*v3.ResourceQuotaLimit{}, projectQuotaLimit)
	if err != nil {
		return err
	}
	if !isFit {
		return httperror.NewFieldAPIError(httperror.MaxLimitExceeded, quotaField, fmt.Sprintf("is below the used limit on fields: %s",
			msg))
	}

	if len(limitToAdd) == 0 && len(limitToRemove) == 0 {
		return nil
	}

	// check if default quota is enough to set on namespaces
	toAppend := &mgmtclient.ResourceQuotaLimit{}
	if err := mapstructure.Decode(limitToAdd, toAppend); err != nil {
		return err
	}
	converted, err := limitToLimit(toAppend)
	if err != nil {
		return err
	}
	mu := resourcequota.GetProjectLock(id)
	mu.Lock()
	defer mu.Unlock()

	namespacesCount, err := s.getNamespacesCount(apiContext, project)
	if err != nil {
		return err
	}
	var nsLimits []*v3.ResourceQuotaLimit
	for i := 0; i < namespacesCount; i++ {
		nsLimits = append(nsLimits, converted)
	}

	isFit, msg, err = resourcequota.IsQuotaFit(&v3.ResourceQuotaLimit{}, nsLimits, projectQuotaLimit)
	if err != nil {
		return err
	}
	if !isFit {
		return httperror.NewFieldAPIError(httperror.MaxLimitExceeded, namespaceQuotaField,
			fmt.Sprintf("exceeds project limit on fields %s when applied to all namespaces in a project",
				msg))
	}

	return nil
}

func (s *projectStore) getNamespacesCount(apiContext *types.APIContext, project mgmtclient.Project) (int, error) {
	cluster, err := s.clusterLister.Get("", project.ClusterID)
	if err != nil {
		return 0, err
	}

	kubeConfig, err := clustermanager.ToRESTConfig(cluster, s.scaledContext)
	if kubeConfig == nil || err != nil {
		return 0, err
	}

	clusterContext, err := config.NewUserContext(s.scaledContext, *kubeConfig, cluster.Name)
	if err != nil {
		return 0, err
	}
	namespaces, err := clusterContext.Core.Namespaces("").List(metav1.ListOptions{})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, n := range namespaces.Items {
		if n.Annotations == nil {
			continue
		}
		if n.Annotations["field.cattle.io/projectId"] == project.ID {
			count++
		}
	}

	return count, nil
}

func limitToLimit(from *mgmtclient.ResourceQuotaLimit) (*v3.ResourceQuotaLimit, error) {
	var to v3.ResourceQuotaLimit
	err := convert.ToObj(from, &to)
	return &to, err
}
