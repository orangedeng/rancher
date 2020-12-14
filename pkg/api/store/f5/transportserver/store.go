package transportserver

import (
	"strings"
	"sync"

	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/types/config"

	projectv3 "github.com/rancher/types/client/project/v3"
)

func Wrap(store types.Store, context *config.ScaledContext) types.Store {
	modify := &Store{
		Store: store,
	}
	modify.mu = sync.Mutex{}
	return modify
}

type Store struct {
	types.Store
	mu sync.Mutex
}

func (p *Store) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {
	tsName := convert.ToString(data[projectv3.TransportServerFieldVirtualServerName])

	err := canUseTransportServerName(apiContext, tsName)
	if err != nil {
		return nil, err
	}

	tsAddress := convert.ToString(data[projectv3.TransportServerFieldVirtualServerPort])

	tsPort, err := convert.ToNumber(data[projectv3.TransportServerFieldVirtualServerPort])
	if err != nil {
		return nil, err
	}

	err = canUseAddressAndPort(apiContext, tsAddress, tsPort)
	if err != nil {
		return nil, err
	}

	data, err = p.Store.Create(apiContext, schema, data)
	return data, err
}

func (p *Store) Update(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, id string) (map[string]interface{}, error) {

	updatedVSName := convert.ToString(data[projectv3.TransportServerFieldVirtualServerName])

	existingTS, err := p.ByID(apiContext, schema, id)
	if err != nil {
		return nil, err
	}

	vsName := convert.ToString(existingTS[projectv3.TransportServerFieldVirtualServerName])

	if !strings.EqualFold(updatedVSName, vsName) {
		p.mu.Lock()
		defer p.mu.Unlock()

		if err := canUseTransportServerName(apiContext, updatedVSName); err != nil {
			return nil, err
		}
	}

	updatedVSAddress := convert.ToString(data[projectv3.TransportServerFieldVirtualServerAddress])
	vsAddress := convert.ToString(existingTS[projectv3.TransportServerFieldVirtualServerAddress])

	updatedPort, err := convert.ToNumber(data[projectv3.TransportServerFieldVirtualServerPort])
	if err != nil {
		return nil, err
	}

	port, err := convert.ToNumber(existingTS[projectv3.TransportServerFieldVirtualServerPort])
	if err != nil {
		return nil, err
	}

	if !strings.EqualFold(updatedVSAddress, vsAddress) ||
		(updatedPort != port) {
		p.mu.Lock()
		defer p.mu.Unlock()

		if err := canUseAddressAndPort(apiContext, updatedVSAddress, updatedPort); err != nil {
			return nil, err
		}
	}

	data, err = p.Store.Update(apiContext, schema, data, id)
	return data, err
}

func canUseTransportServerName(apiContext *types.APIContext, vsName string) error {
	if vsName == "" {
		return nil
	}

	var tslist []projectv3.TransportServer
	conditions := []*types.QueryCondition{
		types.NewConditionFromString(projectv3.TransportServerFieldVirtualServerName, types.ModifierEQ, []string{vsName}...),
	}

	if err := access.List(apiContext, apiContext.Version, projectv3.TransportServerType, &types.QueryOptions{Conditions: conditions}, &tslist); err != nil {
		return err
	}

	if len(tslist) > 0 {
		return httperror.NewFieldAPIError(httperror.NotUnique, projectv3.TransportServerFieldVirtualServerName, "")
	}

	return nil
}

func canUseAddressAndPort(apiContext *types.APIContext, address string, port int64) error {
	var tslist []projectv3.TransportServer
	conditions := []*types.QueryCondition{
		types.NewConditionFromString(projectv3.VirtualServerFieldVirtualServerAddress, types.ModifierEQ, []string{address}...),
	}

	if err := access.List(apiContext, apiContext.Version, projectv3.VirtualServerType, &types.QueryOptions{Conditions: conditions}, &tslist); err != nil {
		return err
	}

	if len(tslist) > 0 {
		hasSamePort := false
		for _, ts := range tslist {
			if ts.VirtualServerPort == port {
				hasSamePort = true
			}
		}
		if hasSamePort {
			return httperror.NewFieldAPIError(httperror.NotUnique, "transportserver address and port", "")
		}
	}

	return nil
}
