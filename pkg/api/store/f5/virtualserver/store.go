package virtualserver

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
	vsName := convert.ToString(data[projectv3.VirtualServerFieldVirtualServerName])

	err := canUseVirtualServerName(apiContext, vsName)
	if err != nil {
		return nil, err
	}

	vsAddress := convert.ToString(data[projectv3.VirtualServerFieldVirtualServerAddress])

	vsHTTPPort, err := convert.ToNumber(data[projectv3.VirtualServerFieldVirtualServerHTTPPort])
	if err != nil {
		return nil, err
	}
	vsHTTPSPort, err := convert.ToNumber(data[projectv3.VirtualServerFieldVirtualServerHTTPSPort])
	if err != nil {
		return nil, err
	}

	err = canUseAddressAndPort(apiContext, vsAddress, vsHTTPPort, vsHTTPSPort)
	if err != nil {
		return nil, err
	}

	data, err = p.Store.Create(apiContext, schema, data)
	return data, err
}

func (p *Store) Update(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, id string) (map[string]interface{}, error) {

	updatedVSName := convert.ToString(data[projectv3.VirtualServerFieldVirtualServerName])

	existingVS, err := p.ByID(apiContext, schema, id)
	if err != nil {
		return nil, err
	}

	vsName := convert.ToString(existingVS[projectv3.VirtualServerFieldVirtualServerName])

	if !strings.EqualFold(updatedVSName, vsName) {
		p.mu.Lock()
		defer p.mu.Unlock()

		if err := canUseVirtualServerName(apiContext, updatedVSName); err != nil {
			return nil, err
		}
	}

	updatedVSAddress := convert.ToString(data[projectv3.VirtualServerFieldVirtualServerAddress])
	vsAddress := convert.ToString(existingVS[projectv3.VirtualServerFieldVirtualServerAddress])

	updatedVSHTTPPort, err := convert.ToNumber(data[projectv3.VirtualServerFieldVirtualServerHTTPPort])
	if err != nil {
		return nil, err
	}

	vsHTTPPort, err := convert.ToNumber(existingVS[projectv3.VirtualServerFieldVirtualServerHTTPPort])
	if err != nil {
		return nil, err
	}

	updatedVSHTTPSPort, err := convert.ToNumber(data[projectv3.VirtualServerFieldVirtualServerHTTPSPort])
	if err != nil {
		return nil, err
	}
	vsHTTPSPort, err := convert.ToNumber(existingVS[projectv3.VirtualServerFieldVirtualServerHTTPSPort])
	if err != nil {
		return nil, err
	}

	if !strings.EqualFold(updatedVSAddress, vsAddress) ||
		(updatedVSHTTPPort != vsHTTPPort) ||
		(updatedVSHTTPSPort != vsHTTPSPort) {
		p.mu.Lock()
		defer p.mu.Unlock()

		if err := canUseAddressAndPort(apiContext, updatedVSAddress, updatedVSHTTPPort, updatedVSHTTPSPort); err != nil {
			return nil, err
		}
	}

	data, err = p.Store.Update(apiContext, schema, data, id)
	return data, err
}

func canUseVirtualServerName(apiContext *types.APIContext, vsName string) error {
	if vsName == "" {
		return nil
	}

	var vslist []projectv3.VirtualServer
	conditions := []*types.QueryCondition{
		types.NewConditionFromString(projectv3.VirtualServerFieldVirtualServerName, types.ModifierEQ, []string{vsName}...),
	}

	if err := access.List(apiContext, apiContext.Version, projectv3.VirtualServerType, &types.QueryOptions{Conditions: conditions}, &vslist); err != nil {
		return err
	}

	if len(vslist) > 0 {
		return httperror.NewFieldAPIError(httperror.NotUnique, projectv3.VirtualServerFieldVirtualServerName, "")
	}

	return nil
}

func canUseAddressAndPort(apiContext *types.APIContext, address string, httpPort, httpsPort int64) error {
	var vslist []projectv3.VirtualServer
	conditions := []*types.QueryCondition{
		types.NewConditionFromString(projectv3.VirtualServerFieldVirtualServerAddress, types.ModifierEQ, []string{address}...),
	}

	if err := access.List(apiContext, apiContext.Version, projectv3.VirtualServerType, &types.QueryOptions{Conditions: conditions}, &vslist); err != nil {
		return err
	}

	if len(vslist) > 0 {
		hasSamePort := false
		for _, vs := range vslist {
			if vs.VirtualServerHTTPPort == httpPort {
				hasSamePort = true
			}
			if vs.VirtualServerHTTPSPort == httpsPort {
				hasSamePort = true
			}
		}
		if hasSamePort {
			return httperror.NewFieldAPIError(httperror.NotUnique, "virtualServer address and port", "")
		}
	}

	return nil
}
