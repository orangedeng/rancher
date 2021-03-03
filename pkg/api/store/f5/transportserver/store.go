package transportserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/norman/types/values"
	"github.com/rancher/types/config"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	managementv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	projectv3 "github.com/rancher/types/client/project/v3"
)

const (
	poolMemberTypeAnnotation   = "f5.cattle.io/poolmembertype"
	clusterF5AnswersAnnotation = "field.cattle.io/overwriteF5CISAppAnswers"
)

func Wrap(store types.Store, context *config.ScaledContext) types.Store {
	modify := &Store{
		Store: store,
	}
	modify.mu = sync.Mutex{}
	modify.clusters = context.Management.Clusters("")
	return modify
}

type Store struct {
	types.Store
	mu       sync.Mutex
	clusters managementv3.ClusterInterface
}

func (p *Store) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {
	tsName := convert.ToString(data[projectv3.TransportServerFieldVirtualServerName])

	url := apiContext.Request.URL.String()

	clusterID := getClusterID(url)
	if clusterID == "" {
		return nil, fmt.Errorf("Get ClusterID from URL error")
	}

	err := p.setF5PoolMemberType(clusterID, data)
	if err != nil {
		return nil, err
	}

	err = canUseTransportServerName(apiContext, tsName)
	if err != nil {
		return nil, err
	}

	tsAddress := convert.ToString(data[projectv3.TransportServerFieldVirtualServerAddress])

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

	if err := access.List(apiContext, apiContext.Version, projectv3.TransportServerType, &types.QueryOptions{Conditions: conditions}, &tslist); err != nil {
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

func getClusterID(url string) string {
	splits := strings.Split(url, "/")
	projectID := splits[3]
	clusterID := strings.Split(projectID, ":")[0]
	return clusterID
}

func (p *Store) setF5PoolMemberType(clusterID string, data map[string]interface{}) error {
	cluster, err := p.clusters.Get(clusterID, v1.GetOptions{})
	if err != nil {
		return err
	}

	answers, ok := cluster.Annotations[clusterF5AnswersAnnotation]
	if !ok {
		return fmt.Errorf("Get cluster F5 app answers failed")
	}

	var input managementv3.F5CISInput
	if err = json.Unmarshal(([]byte)(answers), &input); err != nil {
		return httperror.WrapAPIError(err, httperror.ServerError, "failed to parse cluster f5 answers")
	}

	poolMemberType, ok := input.Answers["network.poolMemberType"]

	if !ok {
		poolMemberType = "cluster"
	}

	values.PutValue(data, poolMemberType, "annotations", poolMemberTypeAnnotation)
	return nil
}
