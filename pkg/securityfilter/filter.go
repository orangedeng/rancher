package securityfilter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/rancher/norman/parse"
	"github.com/rancher/norman/parse/builder"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/definition"
	"github.com/rancher/norman/types/slice"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	pandariav3 "github.com/rancher/types/apis/mgt.pandaria.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	typesrbacv1 "github.com/rancher/types/apis/rbac.authorization.k8s.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	membershipBindingOwner = "memberhsip-binding-owner"
)

type SecurityFilter struct {
	sensitiveFilterClient pandariav3.SensitiveFilterInterface
	crLister              typesrbacv1.ClusterRoleLister
	crbLister             typesrbacv1.ClusterRoleBindingLister
	prtbLister            v3.ProjectRoleTemplateBindingLister
	crtbLister            v3.ClusterRoleTemplateBindingLister
	rtLister              v3.RoleTemplateLister
	grtbLister            v3.GlobalRoleBindingLister
	globalRoleLister      v3.GlobalRoleLister
}

func NewSecurityFilter(context *config.ScaledContext) types.PandariaResponseControl {
	filterInterface := context.PandariaManagement.SensitiveFilters("")
	return &SecurityFilter{
		sensitiveFilterClient: filterInterface,
		crLister:              context.RBAC.ClusterRoles("").Controller().Lister(),
		crbLister:             context.RBAC.ClusterRoleBindings("").Controller().Lister(),
		prtbLister:            context.Management.ProjectRoleTemplateBindings("").Controller().Lister(),
		crtbLister:            context.Management.ClusterRoleTemplateBindings("").Controller().Lister(),
		rtLister:              context.Management.RoleTemplates("").Controller().Lister(),
		grtbLister:            context.Management.GlobalRoleBindings("").Controller().Lister(),
		globalRoleLister:      context.Management.GlobalRoles("").Controller().Lister(),
	}
}

func (s *SecurityFilter) Filter(apiContext *types.APIContext, schema *types.Schema, code int, obj interface{}) (int, interface{}) {
	if apiContext.Version == nil || apiContext == nil || apiContext.Schema == nil {
		logrus.Debugf("SecurityFilter: got empty request context: %++v", *apiContext)
		return code, obj
	}

	// skip error code
	if code >= http.StatusMultipleChoices {
		return code, obj
	}
	// Is requested resource has filter config
	filterRules := s.getRequestedResourceFilterRules(apiContext, schema)
	if len(filterRules) > 0 {
		logrus.Debugf("SecurityFilter: get filter rules %++v for schema %s", filterRules, schema.ID)
		return s.filter(apiContext, schema, filterRules, code, obj)
	}

	return code, obj
}

func (s *SecurityFilter) getRequestedResourceFilterRules(apiContext *types.APIContext, schema *types.Schema) []pandariav3.Filter {
	filterList, err := s.sensitiveFilterClient.List(metav1.ListOptions{})
	if err != nil {
		logrus.Errorf("SecurityFilter: got error when listing sensitiveFilter objects, %v", err)
		return nil
	}

	filterRules := []pandariav3.Filter{}
	for _, f := range filterList.Items {
		rules := f.Filters
		for _, rule := range rules {
			if rule.NonResourceURLs != nil {
				// we don't need to check request method because all links/actions generate by get method
				if NonResourceURLMatches(&rule.PolicyRule, strings.ToLower(apiContext.Request.URL.RequestURI())) {
					filterRules = append(filterRules, rule)
				}
			} else {
				if VerbMatches(&rule.PolicyRule, strings.ToLower(apiContext.Method)) &&
					APIGroupMatches(&rule.PolicyRule, apiContext.Version.Group) &&
					ResourceMatches(&rule.PolicyRule, strings.ToLower(schema.CodeNamePlural)) {
					filterRules = append(filterRules, rule)
				}
			}
		}
	}

	return filterRules
}

func (s *SecurityFilter) filter(apiContext *types.APIContext, schema *types.Schema, filterRules []pandariav3.Filter, code int, obj interface{}) (int, interface{}) {
	attr := NewRequestAttributes(apiContext, strings.ToLower(schema.CodeNamePlural))
	userID := attr.User
	group := attr.Group
	// get all user roles
	// 1. get global roles
	globalRoles, err := s.filterUserGlobalRoles(attr)
	if err != nil {
		logrus.Errorf("SecurityFilter: Failed to get user global role bindings, got error: %v", err)
	}
	// 2. get crtb roles
	clusterRoleTemplateBindingList := []*v3.ClusterRoleTemplateBinding{}
	crtbList, err := s.crtbLister.List("", labels.Everything())
	if err != nil {
		logrus.Errorf("SecurityFilter: Failed to get cluster role template binding, got error: %v", err)
	}
	for _, v := range crtbList {
		if v.UserName == userID || v.GroupPrincipalName == group {
			clusterRoleTemplateBindingList = append(clusterRoleTemplateBindingList, v)
		}
	}
	// 3. get prtb roles
	projectRoleTemplateBindingList := []*v3.ProjectRoleTemplateBinding{}
	prtbList, err := s.prtbLister.List("", labels.Everything())
	if err != nil {
		logrus.Errorf("SecurityFilter: Failed to get project role template binding, got error: %v", err)
	}
	for _, v := range prtbList {
		if v.UserName == userID || v.GroupPrincipalName == group {
			projectRoleTemplateBindingList = append(projectRoleTemplateBindingList, v)
		}
	}

	// 4. get all role templates
	roleTemplates, err := s.rtLister.List("", labels.Everything())
	if err != nil {
		logrus.Errorf("SecurityFilter: get roletemplates error: %v", err)
	}

	// handle response data
	obj = s.responseDataFilter(apiContext, schema, attr, obj, filterRules, globalRoles, clusterRoleTemplateBindingList, projectRoleTemplateBindingList, roleTemplates)

	return code, obj
}

func (s *SecurityFilter) responseDataFilter(apiContext *types.APIContext, schema *types.Schema, attr *RequestAttributes, obj interface{},
	filterRules []pandariav3.Filter, globalRoles []string, clusterRoleTemplateBindingList []*v3.ClusterRoleTemplateBinding,
	projectRoleTemplateBindingList []*v3.ProjectRoleTemplateBinding, roleTemplates []*v3.RoleTemplate) interface{} {

	b := builder.NewBuilder(apiContext)

	switch v := obj.(type) {
	case []interface{}:
		logrus.Infof("SecurityFilter: interface slice type for schema %v", schema.ID)
		return s.interfaceSliceFilter(b, apiContext, attr, schema, v, globalRoles, clusterRoleTemplateBindingList, projectRoleTemplateBindingList, filterRules, roleTemplates)
	case map[string]interface{}:
		accessRoles := s.filterUserRoles(schema, attr, v, globalRoles, clusterRoleTemplateBindingList, projectRoleTemplateBindingList)
		if !isSchemaNeedFilter(accessRoles, filterRules, roleTemplates) {
			return obj
		}
		return s.resourceFilter(b, apiContext, schema, v, accessRoles, filterRules, roleTemplates)
	case []map[string]interface{}:
		return s.mapSliceFilter(b, apiContext, attr, schema, v, globalRoles, clusterRoleTemplateBindingList, projectRoleTemplateBindingList, filterRules, roleTemplates)
	default:
		logrus.Errorf("SecurityFilter: unknown response data type: %v", v)
	}

	return obj
}

func (s *SecurityFilter) mapSliceFilter(b *builder.Builder, apiContext *types.APIContext, attr *RequestAttributes,
	schema *types.Schema, input []map[string]interface{},
	globalRoles []string, clusterRoleTemplateBindingList []*v3.ClusterRoleTemplateBinding,
	projectRoleTemplateBindingList []*v3.ProjectRoleTemplateBinding, filterRules []pandariav3.Filter,
	roleTemplates []*v3.RoleTemplate) *types.GenericCollection {
	collection := newCollection(apiContext)
	for _, value := range input {
		accessRoles := s.filterUserRoles(schema, attr, value, globalRoles, clusterRoleTemplateBindingList, projectRoleTemplateBindingList)
		if !isSchemaNeedFilter(accessRoles, filterRules, roleTemplates) {
			newObj := convertResource(b, apiContext, value, nil)
			if newObj != nil {
				collection.Data = append(collection.Data, newObj)
			}
			continue
		}

		converted := s.resourceFilter(b, apiContext, schema, value, accessRoles, filterRules, roleTemplates)
		if converted != nil {
			collection.Data = append(collection.Data, converted)
		}
	}

	if apiContext.Schema.CollectionFormatter != nil {
		apiContext.Schema.CollectionFormatter(apiContext, collection)
	}

	return collection
}

func (s *SecurityFilter) resourceFilter(b *builder.Builder, apiContext *types.APIContext,
	schema *types.Schema, v map[string]interface{}, accessRoles []string,
	filterRules []pandariav3.Filter, roleTemplates []*v3.RoleTemplate) *types.RawResource {
	filterFields, filterURLs := computeFilterRules(accessRoles, filterRules, roleTemplates)
	logrus.Debugf("SecurityFilter: Fields %v need to be removed, url %v need to be removed for schema %v", filterFields, filterURLs, schema.ID)
	newObj := convertResource(b, apiContext, v, filterURLs)
	values := newObj.Values
	// remove fields
	newData, err := responseValueFilter(values, filterFields)
	if err != nil {
		return newObj
	}
	newObj.Values = newData
	return newObj
}

func (s *SecurityFilter) interfaceSliceFilter(b *builder.Builder, apiContext *types.APIContext, attr *RequestAttributes,
	schema *types.Schema, input []interface{}, globalRoles []string,
	clusterRoleTemplateBindingList []*v3.ClusterRoleTemplateBinding,
	projectRoleTemplateBindingList []*v3.ProjectRoleTemplateBinding, filterRules []pandariav3.Filter,
	roleTemplates []*v3.RoleTemplate) *types.GenericCollection {
	collection := newCollection(apiContext)
	for _, value := range input {
		switch v := value.(type) {
		case map[string]interface{}:
			accessRoles := s.filterUserRoles(schema, attr, v, globalRoles, clusterRoleTemplateBindingList, projectRoleTemplateBindingList)
			if !isSchemaNeedFilter(accessRoles, filterRules, roleTemplates) {
				newObj := convertResource(b, apiContext, v, nil)
				if newObj != nil {
					collection.Data = append(collection.Data, newObj)
				}
				continue
			}

			converted := s.resourceFilter(b, apiContext, schema, v, accessRoles, filterRules, roleTemplates)
			if converted != nil {
				collection.Data = append(collection.Data, converted)
			}
		default:
			collection.Data = append(collection.Data, v)
		}
	}

	if apiContext.Schema.CollectionFormatter != nil {
		apiContext.Schema.CollectionFormatter(apiContext, collection)
	}

	return collection
}

func (s *SecurityFilter) filterUserRoles(schema *types.Schema, attr *RequestAttributes, v map[string]interface{},
	globalRoles []string, clusterRoleTemplateBindingList []*v3.ClusterRoleTemplateBinding,
	projectRoleTemplateBindingList []*v3.ProjectRoleTemplateBinding) []string {
	fields := schema.ResourceFields
	clusterID := ""
	projectID := ""
	if strings.ToLower(schema.ID) == "cluster" {
		if _, ok := v["id"]; ok {
			clusterID = v["id"].(string)
		}
	} else if strings.ToLower(schema.ID) == "project" {
		if _, ok := v["id"]; ok {
			projectID = v["id"].(string)
		}
	} else {
		for name, field := range fields {
			if field.Type == "reference[cluster]" {
				if _, ok := v[name]; ok {
					clusterID = v[name].(string)
				}
			}
			if field.Type == "reference[project]" {
				if _, ok := v[name]; ok {
					projectID = v[name].(string)
				}
			}
		}
	}
	if clusterID == "" && projectID != "" {
		ids := strings.Split(projectID, ":")
		if len(ids) == 2 {
			clusterID = ids[0]
		}
	}

	accessRoles := []string{}
	if len(globalRoles) > 0 {
		accessRoles = append(accessRoles, globalRoles...)
	}
	if clusterID != "" {
		for _, crtb := range clusterRoleTemplateBindingList {
			if crtb.ClusterName == clusterID {
				accessRoles = append(accessRoles, s.checkRoleTemplateRules(crtb.RoleTemplateName, attr, accessRoles)...)

				// get clusterrolebinding control by rbac controller
				defaultCRB, err := s.getDefaultClusterRolebindings(attr, string(crtb.UID), crtb.RoleTemplateName)
				if err != nil {
					logrus.Errorf("SecurityFilter: got error when get cluster role bindings by label %v: %v", string(crtb.UID), err)
					continue
				}
				accessRoles = append(accessRoles, defaultCRB...)
			}
		}
	}

	for _, prtb := range projectRoleTemplateBindingList {
		isCurrentPRTB := false
		if projectID == "" {
			// if project id is empty, get all project roles by cluster id
			ss := strings.Split(prtb.ProjectName, ":")
			if len(ss) == 2 && clusterID == ss[0] {
				isCurrentPRTB = true
			}
		} else if prtb.ProjectName == projectID {
			isCurrentPRTB = true
		}
		if isCurrentPRTB {
			accessRoles = append(accessRoles, s.checkRoleTemplateRules(prtb.RoleTemplateName, attr, accessRoles)...)

			// get default clusterrolebinding create by rbac controller
			defaultCRB, err := s.getDefaultClusterRolebindings(attr, string(prtb.UID), prtb.RoleTemplateName)
			if err != nil {
				logrus.Errorf("SecurityFilter: got error when get cluster role bindings by label %v: %v", string(prtb.UID), err)
				continue
			}
			accessRoles = append(accessRoles, defaultCRB...)
		}
	}

	accessRoles = unique(accessRoles)

	return accessRoles
}

func (s *SecurityFilter) checkRoleTemplateRules(roleTemplateName string, attr *RequestAttributes, accessRoles []string) []string {
	rt, err := s.rtLister.Get("", roleTemplateName)
	if err != nil {
		logrus.Errorf("SecurityFilter: got error when get role template by name %v: %v", roleTemplateName, err)
		return accessRoles
	}
	if attr.RulesAllow(rt.Rules...) {
		accessRoles = append(accessRoles, roleTemplateName)
	}

	if rt.RoleTemplateNames != nil && len(rt.RoleTemplateNames) > 0 {
		for _, parentRole := range rt.RoleTemplateNames {
			accessRoles = append(accessRoles, s.checkRoleTemplateRules(parentRole, attr, accessRoles)...)
		}
	}

	return accessRoles
}

// getDefaultClusterRolebindings will get specified clusterrolebinding which generated by rbac controller(prtb_handler, crtb_handler)
func (s *SecurityFilter) getDefaultClusterRolebindings(attr *RequestAttributes, uid, roleTemplateName string) ([]string, error) {
	accessRoles := []string{}
	clusterRoleBindings, err := s.crbLister.List("", labels.Set(map[string]string{uid: membershipBindingOwner}).AsSelector())
	if err != nil {
		return nil, err
	}
	for _, clusterRoleBinding := range clusterRoleBindings {
		clusterRoleName := clusterRoleBinding.RoleRef.Name
		clusterRole, err := s.crLister.Get("", clusterRoleName)
		if err != nil {
			logrus.Errorf("SecurityFilter: got error when get cluster role by name %v: %v", clusterRoleName, err)
			continue
		}
		if attr.RulesAllow(clusterRole.Rules...) {
			accessRoles = append(accessRoles, roleTemplateName)
			break
		}
	}
	return accessRoles, nil
}

func (s *SecurityFilter) filterUserGlobalRoles(attr *RequestAttributes) ([]string, error) {
	globalRoles := []string{}
	grtbList, err := s.grtbLister.List("", labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, v := range grtbList {
		if v.UserName == attr.User || v.GroupPrincipalName == attr.Group {
			// filter global roles without permission to access specified schema
			gr, err := s.globalRoleLister.Get("", v.GlobalRoleName)
			if err != nil {
				logrus.Errorf("SecurityFilter: got error when get global role by name %v: %v", v.GlobalRoleName, err)
				continue
			}
			if attr.RulesAllow(gr.Rules...) {
				globalRoles = append(globalRoles, v.GlobalRoleName)
			}
		}
	}
	return globalRoles, nil
}

func computeFilterRules(accessRoles []string, filterRules []pandariav3.Filter, roleTemplates []*v3.RoleTemplate) ([]string, []pandariav3.Filter) {
	// filter fileds that need to control
	controlFields := map[string]int{}
	controlUrls := map[string]int{}
	for _, r := range accessRoles {
		roleFilterFields := []string{}
		roleFilterUrls := []string{}
		for _, rule := range filterRules {
			filterRoles := rule.Roles
			// get specified scope of roles
			if rule.RoleScope != "" && roleTemplates != nil {
				for _, rt := range roleTemplates {
					if rt.Context == rule.RoleScope && !rt.Hidden {
						filterRoles = append(filterRoles, rt.Name)
					}
				}
			}
			for _, role := range filterRoles {
				if r == role {
					if rule.NonResourceURLs != nil {
						roleFilterUrls = unique(append(roleFilterUrls, rule.NonResourceURLs...))
					} else {
						roleFilterFields = unique(append(roleFilterFields, rule.Fields...))
					}
					break
				}
			}
		}
		for _, field := range roleFilterFields {
			if _, ok := controlFields[field]; !ok {
				controlFields[field] = 1
			} else {
				controlFields[field] = controlFields[field] + 1
			}
		}
		for _, url := range roleFilterUrls {
			if _, ok := controlUrls[url]; !ok {
				controlUrls[url] = 1
			} else {
				controlUrls[url] = controlUrls[url] + 1
			}
		}
	}

	filterFields := []string{}
	for field, count := range controlFields {
		if count == len(accessRoles) {
			filterFields = append(filterFields, field)
		}
	}

	filterUrls := []pandariav3.Filter{}
	for url, count := range controlUrls {
		if count == len(accessRoles) {
			for _, rule := range filterRules {
				if slice.ContainsString(rule.NonResourceURLs, url) {
					r := pandariav3.Filter{}
					r.NonResourceURLs = []string{url}
					r.Verbs = rule.Verbs
					filterUrls = append(filterUrls, r)
				}
			}
		}
	}

	return filterFields, filterUrls
}

func isSchemaNeedFilter(accessRoles []string, filterRules []pandariav3.Filter, roleTemplates []*v3.RoleTemplate) bool {
	for _, r := range accessRoles {
		isNeedFilter := false
		for _, rule := range filterRules {
			filterRoles := rule.Roles
			// get specified scope of roles
			if rule.RoleScope != "" && roleTemplates != nil {
				for _, rt := range roleTemplates {
					if rt.Context == rule.RoleScope && !rt.Hidden {
						filterRoles = append(filterRoles, rt.Name)
					}
				}
			}
			if slice.ContainsString(filterRoles, r) {
				isNeedFilter = true
				break
			}
		}
		if !isNeedFilter {
			return false
		}
	}
	return true
}

func responseValueFilter(value map[string]interface{}, fields []string) (map[string]interface{}, error) {
	if value == nil {
		return value, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		logrus.Errorf("SecurityFilter: marshal mapInterface type object error: %v", err)
		return nil, err
	}
	data = filterData(fields, data)
	newData := map[string]interface{}{}
	err = json.Unmarshal(data, &newData)
	if err != nil {
		logrus.Errorf("SecurityFilter: Unmarshal mapInterface type object [%v] error: %v", string(data), err)
		return nil, err
	}
	return newData, nil
}

func filterData(fields []string, data []byte) []byte {
	ops := []map[string]interface{}{}
	for _, field := range fields {
		f := strings.ReplaceAll(field, ".", "/")
		if gjson.Get(string(data), field).Exists() {
			op := map[string]interface{}{
				"op":   "remove",
				"path": fmt.Sprintf("/%s", f),
			}
			ops = append(ops, op)
		}
	}

	logrus.Debugf("SecurityFilter: fields operation %v", ops)
	patchOp, err := json.Marshal(ops)
	patchObj, err := jsonpatch.DecodePatch(patchOp)
	if err != nil {
		return data
	}
	bytes, err := patchObj.Apply(data)
	if err != nil {
		return data
	}

	return bytes
}

func convertResource(b *builder.Builder, context *types.APIContext, input map[string]interface{}, urls []pandariav3.Filter) *types.RawResource {
	schema := context.Schemas.Schema(context.Version, definition.GetFullType(input))
	if schema == nil {
		return nil
	}
	op := builder.List
	if context.Method == http.MethodPost {
		op = builder.ListForCreate
	}
	data, err := b.Construct(schema, input, op)
	if err != nil {
		logrus.Errorf("Failed to construct object on output: %v", err)
		return nil
	}

	rawResource := &types.RawResource{
		ID:          toString(input["id"]),
		Type:        schema.ID,
		Schema:      schema,
		Links:       map[string]string{},
		Actions:     map[string]string{},
		Values:      data,
		ActionLinks: context.Request.Header.Get("X-API-Action-Links") != "",
	}

	addLinks(b, schema, context, input, rawResource)

	if schema.Formatter != nil {
		schema.Formatter(context, rawResource)
	}

	links := filterLinks(rawResource.Links, urls, strings.ToLower(context.Method))
	rawResource.Links = links

	actions := filterActions(rawResource.Actions, urls, strings.ToLower(context.Method))
	rawResource.Actions = actions

	return rawResource
}

func toString(val interface{}) string {
	if val == nil {
		return ""
	}
	return fmt.Sprint(val)
}

func newCollection(apiContext *types.APIContext) *types.GenericCollection {
	result := &types.GenericCollection{
		Collection: types.Collection{
			Type:         "collection",
			ResourceType: apiContext.Type,
			CreateTypes:  map[string]string{},
			Links: map[string]string{
				"self": apiContext.URLBuilder.Current(),
			},
			Actions: map[string]string{},
		},
		Data: []interface{}{},
	}

	if apiContext.Method == http.MethodGet {
		if apiContext.AccessControl.CanCreate(apiContext, apiContext.Schema) == nil {
			result.CreateTypes[apiContext.Schema.ID] = apiContext.URLBuilder.Collection(apiContext.Schema, apiContext.Version)
		}
	}

	opts := parse.QueryOptions(apiContext, apiContext.Schema)
	result.Sort = &opts.Sort
	result.Sort.Reverse = apiContext.URLBuilder.ReverseSort(result.Sort.Order)
	result.Sort.Links = map[string]string{}
	result.Pagination = opts.Pagination
	result.Filters = map[string][]types.Condition{}

	for _, cond := range opts.Conditions {
		filters := result.Filters[cond.Field]
		result.Filters[cond.Field] = append(filters, cond.ToCondition())
	}

	for name := range apiContext.Schema.CollectionFilters {
		if _, ok := result.Filters[name]; !ok {
			result.Filters[name] = nil
		}
	}

	for queryField := range apiContext.Schema.CollectionFilters {
		field, ok := apiContext.Schema.ResourceFields[queryField]
		if ok && (field.Type == "string" || field.Type == "enum") {
			result.Sort.Links[queryField] = apiContext.URLBuilder.Sort(queryField)
		}
	}

	if result.Pagination != nil && result.Pagination.Partial {
		if result.Pagination.Next != "" {
			result.Pagination.Next = apiContext.URLBuilder.Marker(result.Pagination.Next)
		}
		if result.Pagination.Previous != "" {
			result.Pagination.Previous = apiContext.URLBuilder.Marker(result.Pagination.Previous)
		}
		if result.Pagination.First != "" {
			result.Pagination.First = apiContext.URLBuilder.Marker(result.Pagination.First)
		}
		if result.Pagination.Last != "" {
			result.Pagination.Last = apiContext.URLBuilder.Marker(result.Pagination.Last)
		}
	}

	return result
}

func addLinks(b *builder.Builder, schema *types.Schema, context *types.APIContext, input map[string]interface{}, rawResource *types.RawResource) {
	if rawResource.ID == "" {
		return
	}

	self := context.URLBuilder.ResourceLink(rawResource)
	rawResource.Links["self"] = self
	if context.AccessControl.CanUpdate(context, input, schema) == nil {
		rawResource.Links["update"] = self
	}
	if context.AccessControl.CanDelete(context, input, schema) == nil {
		rawResource.Links["remove"] = self
	}

	subContextVersion := context.Schemas.SubContextVersionForSchema(schema)
	for _, backRef := range context.Schemas.References(schema) {
		if backRef.Schema.CanList(context) != nil {
			continue
		}

		if subContextVersion == nil {
			rawResource.Links[backRef.Schema.PluralName] = context.URLBuilder.FilterLink(backRef.Schema, backRef.FieldName, rawResource.ID)
		} else {
			rawResource.Links[backRef.Schema.PluralName] = context.URLBuilder.SubContextCollection(schema, rawResource.ID, backRef.Schema)
		}
	}

	if subContextVersion != nil {
		for _, subSchema := range context.Schemas.SchemasForVersion(*subContextVersion) {
			if subSchema.CanList(context) == nil {
				rawResource.Links[subSchema.PluralName] = context.URLBuilder.SubContextCollection(schema, rawResource.ID, subSchema)
			}
		}
	}
}

func filterLinks(links map[string]string, urlFilters []pandariav3.Filter, verb string) map[string]string {
	if urlFilters == nil {
		return links
	}
	for _, filter := range urlFilters {
		if len(filter.NonResourceURLs) == 0 {
			continue
		}
		filterURL := filter.NonResourceURLs[0]
		for key, path := range links {
			linksURL, err := url.ParseRequestURI(path)
			if err != nil {
				logrus.Errorf("SecurityFilter: parse url of links %s error: %v", path, err)
				continue
			}
			requestURI := strings.ToLower(linksURL.RequestURI())
			if IsLinksURLFit(requestURI, filterURL) {
				if key == "update" && (slice.ContainsString(filter.Verbs, "update") || slice.ContainsString(filter.Verbs, "patch")) {
					delete(links, key)
				} else if key == "remove" && slice.ContainsString(filter.Verbs, "delete") {
					delete(links, key)
				} else if VerbMatches(&filter.PolicyRule, verb) {
					delete(links, key)
				}
			}
			if IsLinksSubURLFit(requestURI, filterURL) && VerbMatches(&filter.PolicyRule, verb) {
				delete(links, key)
			}
		}
	}

	return links
}

func filterActions(actions map[string]string, urlFilters []pandariav3.Filter, verb string) map[string]string {
	if urlFilters == nil {
		return actions
	}
	for _, filterRule := range urlFilters {
		if len(filterRule.NonResourceURLs) == 0 {
			continue
		}
		filterURL := filterRule.NonResourceURLs[0]
		for key, path := range actions {
			linksURL, err := url.ParseRequestURI(path)
			if err != nil {
				logrus.Errorf("SecurityFilter: parse url of links %s error: %v", path, err)
				continue
			}
			if IsActionURLFit(linksURL.RequestURI(), filterURL) && VerbMatches(&filterRule.PolicyRule, verb) {
				delete(actions, key)
			}
		}
	}

	return actions
}

func unique(s []string) []string {
	keys := make(map[string]bool)
	list := []string{}
	for _, entry := range s {
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}
