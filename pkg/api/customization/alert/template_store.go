package alert

import (
	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	client "github.com/rancher/types/client/management/v3"
)

func WrapStore(store types.Store) types.Store {
	storeWrapped := &Store{
		Store: store,
	}
	return storeWrapped
}

type Store struct {
	types.Store
}

func (s *Store) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {

	conditions := []*types.QueryCondition{
		types.NewConditionFromString(client.NotificationTemplateFieldClusterID, types.ModifierEQ, data["clusterId"].(string)),
	}

	var notificationTemplates []*v3.NotificationTemplate
	if err := access.List(apiContext, &schema.Version, client.NotificationTemplateType, &types.QueryOptions{Conditions: conditions}, &notificationTemplates); err != nil {
		return nil, err
	}

	if len(notificationTemplates) > 0 {
		return nil, httperror.NewAPIError(httperror.NotFound, "custom notification template already exist")
	}

	return s.Store.Create(apiContext, schema, data)
}
