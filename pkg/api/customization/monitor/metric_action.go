package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/prometheus/prometheus/promql/parser"
	"github.com/rancher/norman/parse"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/prometheus-auth/pkg/prom"
	"github.com/rancher/rancher/pkg/clustermanager"
	"github.com/rancher/rancher/pkg/controllers/user/alert/configsyncer"
	monitorutil "github.com/rancher/rancher/pkg/monitoring"
	"github.com/rancher/rancher/pkg/ref"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config/dialer"
)

func NewMetricHandler(dialerFactory dialer.Factory, clustermanager *clustermanager.Manager) *MetricHandler {
	return &MetricHandler{
		dialerFactory:  dialerFactory,
		clustermanager: clustermanager,
	}
}

type MetricHandler struct {
	dialerFactory  dialer.Factory
	clustermanager *clustermanager.Manager
}

func (h *MetricHandler) Action(actionName string, action *types.Action, apiContext *types.APIContext) error {
	var clusterName string
	svcName, svcNamespace, svcPort := monitorutil.ClusterPrometheusEndpoint()

	userID := apiContext.Request.Header.Get("Impersonate-User")
	if userID == "" {
		return fmt.Errorf("can't find user")
	}

	groups := apiContext.Request.Header["Impersonate-Group"]

	switch actionName {
	case querycluster, queryproject:
		var comm v3.CommonQueryMetricInput
		var err error

		if actionName == querycluster {
			var queryMetricInput v3.QueryClusterMetricInput
			actionInput, err := parse.ReadBody(apiContext.Request)
			if err != nil {
				return err
			}

			if err = convert.ToObj(actionInput, &queryMetricInput); err != nil {
				return err
			}

			if clusterName = queryMetricInput.ClusterName; clusterName == "" {
				return fmt.Errorf("clusterName is empty")
			}

			comm = queryMetricInput.CommonQueryMetricInput
		} else {
			var queryMetricInput v3.QueryProjectMetricInput
			actionInput, err := parse.ReadBody(apiContext.Request)
			if err != nil {
				return err
			}
			if err = convert.ToObj(actionInput, &queryMetricInput); err != nil {
				return err
			}

			if clusterName, _ = ref.Parse(queryMetricInput.ProjectName); clusterName == "" {
				return fmt.Errorf("clusterName is empty")
			}

			clusterContext, err := h.clustermanager.UserContext(clusterName)
			if err != nil {
				return err
			}

			projectNs, err := configsyncer.GetProjectNamespace(clusterContext.Core.Namespaces("").Controller().Lister())
			if err != nil {
				return err
			}

			nsSet := projectNs[queryMetricInput.ProjectName]
			expr, err := parser.ParseExpr(queryMetricInput.CommonQueryMetricInput.Expr)
			if err != nil {
				return fmt.Errorf("failed to parse raw expression %s to prometheus expression, %v", queryMetricInput.CommonQueryMetricInput.Expr, err)
			}

			hjkExpr := prom.ModifyExpression(expr, nsSet)
			comm = queryMetricInput.CommonQueryMetricInput
			comm.Expr = hjkExpr
		}

		start, end, step, err := parseTimeParams(comm.From, comm.To, comm.Interval)
		if err != nil {
			return err
		}

		userContext, err := h.clustermanager.UserContext(clusterName)
		if err != nil {
			return fmt.Errorf("get usercontext failed, %v", err)
		}

		reqContext, cancel := context.WithTimeout(context.Background(), prometheusReqTimeout)
		defer cancel()

		prometheusQuery, err := NewPrometheusQuery(reqContext, clusterName, userID, svcNamespace, svcName, svcPort, groups, h.dialerFactory, userContext)
		if err != nil {
			return err
		}

		query := InitPromQuery("", start, end, step, comm.Expr, "", false)
		seriesSlice, err := prometheusQuery.QueryRange(query)
		if err != nil {
			return err
		}

		if seriesSlice == nil {
			apiContext.WriteResponse(http.StatusNoContent, nil)
			return nil
		}

		data := map[string]interface{}{
			"type":   "queryMetricOutput",
			"series": seriesSlice,
		}

		res, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal query stats result failed, %v", err)
		}
		apiContext.Response.Write(res)

	case listclustermetricname, listprojectmetricname:
		if actionName == listclustermetricname {
			var input v3.ClusterMetricNamesInput
			actionInput, err := parse.ReadBody(apiContext.Request)
			if err != nil {
				return err
			}
			if err = convert.ToObj(actionInput, &input); err != nil {
				return err
			}

			if clusterName = input.ClusterName; clusterName == "" {
				return fmt.Errorf("clusterName is empty")
			}
		} else if actionName == listprojectmetricname {
			// project metric names need to merge cluster level and project level prometheus labels name list
			var input v3.ProjectMetricNamesInput
			actionInput, err := parse.ReadBody(apiContext.Request)
			if err != nil {
				return err
			}
			if err = convert.ToObj(actionInput, &input); err != nil {
				return err
			}

			if clusterName, _ = ref.Parse(input.ProjectName); clusterName == "" {
				return fmt.Errorf("clusterName is empty")
			}

		}

		userContext, err := h.clustermanager.UserContext(clusterName)
		if err != nil {
			return fmt.Errorf("get usercontext failed, %v", err)
		}

		reqContext, cancel := context.WithTimeout(context.Background(), prometheusReqTimeout)
		defer cancel()

		prometheusQuery, err := NewPrometheusQuery(reqContext, clusterName, userID, svcNamespace, svcName, svcPort, groups, h.dialerFactory, userContext)
		if err != nil {
			return err
		}

		names, err := prometheusQuery.GetLabelValues("__name__")
		if err != nil {
			return err
		}
		data := map[string]interface{}{
			"type":  "metricNamesOutput",
			"names": names,
		}
		apiContext.WriteResponse(http.StatusOK, data)
	}
	return nil

}
