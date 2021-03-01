package cluster

import (
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/f5cis"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (a ActionHandler) viewF5CIS(actionName string, action *types.Action, apiContext *types.APIContext) error {
	cluster, err := a.ClusterClient.Get(apiContext.ID, v1.GetOptions{})
	if err != nil {
		return httperror.WrapAPIError(err, httperror.NotFound, "none existent Cluster")
	}
	if cluster.DeletionTimestamp != nil {
		return httperror.NewAPIError(httperror.InvalidType, "deleting Cluster")
	}

	if !cluster.Spec.EnableF5CIS {
		return httperror.NewAPIError(httperror.InvalidState, "disabling F5 CIS")
	}

	// need to support `map[string]string` as entry value type in norman Builder.convertMap
	answers, valuesYaml, extraAnswers, version := f5cis.GetF5CISAppAnswersAndVersion(cluster.Annotations)
	encodeAnswers, err := convert.EncodeToMap(answers)
	if err != nil {
		return httperror.WrapAPIError(err, httperror.ServerError, "failed to parse response")
	}

	encodeExtraAnswers, err := convert.EncodeToMap(extraAnswers)
	if err != nil {
		return httperror.WrapAPIError(err, httperror.ServerError, "failed to parse response")
	}
	resp := map[string]interface{}{
		"valuesYaml":   valuesYaml,
		"answers":      encodeAnswers,
		"extraAnswers": encodeExtraAnswers,
		"type":         "f5CISOutput",
	}
	if version != "" {
		resp["version"] = version
	}

	apiContext.WriteResponse(http.StatusOK, resp)
	return nil
}

func (a ActionHandler) editF5CIS(actionName string, action *types.Action, apiContext *types.APIContext) error {
	cluster, err := a.ClusterClient.Get(apiContext.ID, v1.GetOptions{})
	if err != nil {
		return httperror.WrapAPIError(err, httperror.NotFound, "none existent Cluster")
	}
	if cluster.DeletionTimestamp != nil {
		return httperror.NewAPIError(httperror.InvalidType, "deleting Cluster")
	}

	if !cluster.Spec.EnableF5CIS {
		return httperror.NewAPIError(httperror.InvalidState, "disabling F5 CIS")
	}

	data, err := ioutil.ReadAll(apiContext.Request.Body)
	if err != nil {
		return httperror.WrapAPIError(err, httperror.InvalidBodyContent, "unable to read request content")
	}
	var input v3.F5CISInput
	if err = json.Unmarshal(data, &input); err != nil {
		return httperror.WrapAPIError(err, httperror.InvalidBodyContent, "failed to parse request content")
	}

	cluster = cluster.DeepCopy()
	cluster.Annotations = f5cis.AppendAppOverwritingAnswers(cluster.Annotations, string(data))

	_, err = a.ClusterClient.Update(cluster)
	if err != nil {
		return httperror.WrapAPIError(err, httperror.ServerError, "failed to upgrade monitoring")
	}

	apiContext.WriteResponse(http.StatusNoContent, map[string]interface{}{})
	return nil
}

func (a ActionHandler) enableF5CIS(actionName string, action *types.Action, apiContext *types.APIContext) error {
	cluster, err := a.ClusterClient.Get(apiContext.ID, v1.GetOptions{})
	if err != nil {
		return httperror.WrapAPIError(err, httperror.NotFound, "none existent Cluster")
	}
	if cluster.DeletionTimestamp != nil {
		return httperror.NewAPIError(httperror.InvalidType, "deleting Cluster")
	}

	if cluster.Spec.EnableF5CIS {
		apiContext.WriteResponse(http.StatusNoContent, map[string]interface{}{})
		return nil
	}

	data, err := ioutil.ReadAll(apiContext.Request.Body)
	if err != nil {
		return httperror.WrapAPIError(err, httperror.InvalidBodyContent, "unable to read request content")
	}
	var input v3.F5CISInput
	if err = json.Unmarshal(data, &input); err != nil {
		return httperror.WrapAPIError(err, httperror.InvalidBodyContent, "failed to parse request content")
	}

	cluster = cluster.DeepCopy()
	cluster.Spec.EnableF5CIS = true
	cluster.Annotations = f5cis.AppendAppOverwritingAnswers(cluster.Annotations, string(data))

	_, err = a.ClusterClient.Update(cluster)
	if err != nil {
		return httperror.WrapAPIError(err, httperror.ServerError, "failed to enable monitoring")
	}

	apiContext.WriteResponse(http.StatusNoContent, map[string]interface{}{})
	return nil
}

func (a ActionHandler) disableF5CIS(actionName string, action *types.Action, apiContext *types.APIContext) error {
	cluster, err := a.ClusterClient.Get(apiContext.ID, v1.GetOptions{})
	if err != nil {
		return httperror.WrapAPIError(err, httperror.NotFound, "none existent Cluster")
	}
	if cluster.DeletionTimestamp != nil {
		return httperror.NewAPIError(httperror.InvalidType, "deleting Cluster")
	}

	if !cluster.Spec.EnableF5CIS {
		apiContext.WriteResponse(http.StatusNoContent, map[string]interface{}{})
		return nil
	}

	cluster = cluster.DeepCopy()
	cluster.Spec.EnableF5CIS = false

	_, err = a.ClusterClient.Update(cluster)
	if err != nil {
		return httperror.WrapAPIError(err, httperror.ServerError, "failed to disable F5 CIS")
	}

	apiContext.WriteResponse(http.StatusNoContent, map[string]interface{}{})
	return nil
}
