package cache

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

type Store struct {
	types.Store
	Mgmt               *config.ScaledContext
	ResourceMethodName string
}

func Wrap(store types.Store, mgmt *config.ScaledContext, resMethod string) types.Store {
	return &Store{
		Store:              store,
		Mgmt:               mgmt,
		ResourceMethodName: resMethod,
	}

}

func (s *Store) List(apiContext *types.APIContext, schema *types.Schema, opt *types.QueryOptions) ([]map[string]interface{}, error) {
	if strings.EqualFold(settings.EnableManagementAPICache.Get(), "true") {
		resultList := unstructured.UnstructuredList{}

		if opt == nil || opt.Namespaces == nil {
			ns := getNamespace(apiContext, opt)
			resultItems, err := s.listResourceDataFromCache(ns, schema)
			if err != nil {
				return nil, err
			}
			resultList.Items = resultItems
		} else {
			var (
				errGroup errgroup.Group
				mux      sync.Mutex
			)

			allNS := opt.Namespaces
			for _, ns := range allNS {
				nsCopy := ns
				errGroup.Go(func() error {
					resultItems, err := s.listResourceDataFromCache(nsCopy, schema)
					if err != nil {
						return err
					}
					mux.Lock()
					resultList.Items = append(resultList.Items, resultItems...)
					mux.Unlock()
					return nil
				})
			}
			if err := errGroup.Wait(); err != nil {
				return nil, err
			}
		}

		var result []map[string]interface{}
		for _, obj := range resultList.Items {
			result = append(result, s.fromInternal(apiContext, schema, obj.Object))
		}

		authContext := map[string]string{
			"apiGroup": schema.Version.Group,
			"resource": strings.ToLower(schema.PluralName),
		}

		filterData := apiContext.AccessControl.FilterList(apiContext, schema, result, authContext)
		return filterData, nil
	}
	return s.Store.List(apiContext, schema, opt)
}

func (s *Store) listResourceDataFromCache(ns string, schema *types.Schema) ([]unstructured.Unstructured, error) {
	var nsInput []reflect.Value
	start := time.Now()
	nsInput = append(nsInput, reflect.ValueOf(ns))
	resourceMethodName := schema.CodeNamePlural
	if s.ResourceMethodName != "" {
		resourceMethodName = s.ResourceMethodName
	}

	resourceMethod := reflect.ValueOf(s.Mgmt.Management).MethodByName(resourceMethodName).Call(nsInput)
	if len(resourceMethod) != 1 {
		return nil, fmt.Errorf("cache store get resource method failed")
	}

	controllerMethod := resourceMethod[0].MethodByName("Controller").Call(nil)
	if len(controllerMethod) != 1 {
		return nil, fmt.Errorf("cache store get %s controller method failed", schema.CodeNamePlural)
	}

	listMethod := controllerMethod[0].MethodByName("Lister").Call(nil)
	if len(listMethod) != 1 {
		return nil, fmt.Errorf("cache store get %s list method failed", schema.CodeNamePlural)
	}

	var listInputs []reflect.Value
	listInputs = append(listInputs, reflect.ValueOf(""))
	listInputs = append(listInputs, reflect.ValueOf(labels.NewSelector()))

	methodResult := listMethod[0].MethodByName("List").Call(listInputs)
	if len(methodResult) != 2 {
		return nil, fmt.Errorf("cache store list %s data failed", schema.CodeNamePlural)
	}

	returnData := methodResult[0]
	returnErr := methodResult[1]

	if !returnErr.IsNil() {
		return nil, fmt.Errorf("cache store list data error")
	}

	resultItems := []unstructured.Unstructured{}

	for i := 0; i < returnData.Len(); i++ {
		v := returnData.Index(i)
		data, err := json.Marshal(v.Interface())
		if err != nil {
			return nil, err
		}

		result := unstructured.Unstructured{}
		unstructured.UnstructuredJSONScheme.Decode(data, nil, &result)
		resultItems = append(resultItems, result)
	}
	logrus.Tracef("LIST Cache: %v, %v", time.Now().Sub(start), schema.PluralName)
	return resultItems, nil
}

func (s *Store) fromInternal(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) map[string]interface{} {
	if apiContext.Option("export") == "true" {
		delete(data, "status")
	}
	if schema.Mapper != nil {
		schema.Mapper.FromInternal(data)
	}

	return data
}

func getNamespace(apiContext *types.APIContext, opt *types.QueryOptions) string {
	if val, ok := apiContext.SubContext["namespaces"]; ok {
		return convert.ToString(val)
	}

	for _, condition := range opt.Conditions {
		mod := condition.ToCondition().Modifier
		if condition.Field == "namespaceId" && condition.Value != "" && mod == types.ModifierEQ {
			return condition.Value
		}
		if condition.Field == "namespace" && condition.Value != "" && mod == types.ModifierEQ {
			return condition.Value
		}
	}

	return ""
}
