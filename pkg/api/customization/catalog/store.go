package catalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/rancher/norman/store/proxy"
	"github.com/rancher/norman/store/transform"
	"github.com/rancher/norman/types"
	cacheStore "github.com/rancher/rancher/pkg/api/store/cache"
	"github.com/rancher/rancher/pkg/catalog/manager"
	catUtil "github.com/rancher/rancher/pkg/catalog/utils"
	hcommon "github.com/rancher/rancher/pkg/controllers/user/helm/common"
	"github.com/rancher/rancher/pkg/settings"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	managementschema "github.com/rancher/types/apis/management.cattle.io/v3/schema"
	client "github.com/rancher/types/client/management/v3"
	"github.com/rancher/types/config"
)

type templateStore struct {
	types.Store
	CatalogTemplateVersionLister v3.CatalogTemplateVersionLister
	CatalogManager               manager.CatalogManager
}

func GetTemplateStore(ctx context.Context, managementContext *config.ScaledContext) types.Store {
	ts := templateStore{
		CatalogTemplateVersionLister: managementContext.Management.CatalogTemplateVersions("").Controller().Lister(),
		CatalogManager:               managementContext.CatalogManager,
	}

	resSetting := settings.ManagementCacheResource.Get()
	schemaIDs := strings.Split(resSetting, ",")
	var wrapCacheStore bool = false
	for _, id := range schemaIDs {
		if strings.EqualFold(id, client.TemplateType) {
			wrapCacheStore = true
			break
		}
	}

	baseStore := proxy.NewProxyStore(ctx, managementContext.ClientGetter,
		config.ManagementStorageContext,
		[]string{"apis"},
		"management.cattle.io",
		"v3",
		"CatalogTemplate",
		"catalogtemplates")

	if wrapCacheStore && strings.EqualFold(settings.EnableManagementAPICache.Get(), "true") {
		baseStore = cacheStore.Wrap(baseStore, managementContext, "CatalogTemplates")
	}

	s := &transform.Store{
		Store: baseStore,
		Transformer: func(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, opt *types.QueryOptions) (map[string]interface{}, error) {
			data[client.CatalogTemplateFieldVersionLinks] = ts.extractVersionLinks(apiContext, data)
			return data, nil
		},
	}

	ts.Store = s

	return ts
}

func (t *templateStore) extractVersionLinks(apiContext *types.APIContext, resource map[string]interface{}) map[string]interface{} {
	schema := apiContext.Schemas.Schema(&managementschema.Version, client.TemplateVersionType)
	r := map[string]interface{}{}
	versionMap, ok := resource[client.CatalogTemplateFieldVersions].([]interface{})
	if ok {
		for _, version := range versionMap {
			revision := ""
			if v, ok := version.(map[string]interface{})["revision"].(int64); ok {
				revision = strconv.FormatInt(v, 10)
			}
			versionString := version.(map[string]interface{})["version"].(string)
			versionID := fmt.Sprintf("%v-%v", resource["id"], versionString)
			if revision != "" {
				versionID = fmt.Sprintf("%v-%v", resource["id"], revision)
			}
			if t.isTemplateVersionCompatible(apiContext.Query.Get("clusterName"), version.(map[string]interface{})["externalId"].(string)) {
				r[versionString] = apiContext.URLBuilder.ResourceLinkByID(schema, versionID)
			}
		}
	}
	return r
}

// templateVersionForRancherVersion indicates if a templateVersion works with the rancher server version
// In the error case it will always return true - if a template is actually invalid for that rancher version
// API validation will handle the rejection
func (t *templateStore) isTemplateVersionCompatible(clusterName, externalID string) bool {
	rancherVersion := settings.ServerVersion.Get()

	if !catUtil.ReleaseServerVersion(rancherVersion) {
		return true
	}

	templateVersionID, namespace, err := hcommon.ParseExternalID(externalID)
	if err != nil {
		return true
	}

	template, err := t.CatalogTemplateVersionLister.Get(namespace, templateVersionID)
	if err != nil {
		return true
	}

	err = t.CatalogManager.ValidateRancherVersion(template)
	if err != nil {
		return false
	}

	if clusterName != "" {
		if err := t.CatalogManager.ValidateKubeVersion(template, clusterName); err != nil {
			return false
		}
	}

	return true
}
