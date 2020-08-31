package watcher

import (
	"context"
	"strings"
	"time"

	"github.com/rancher/rancher/pkg/controllers/user/alert/common"
	"github.com/rancher/rancher/pkg/controllers/user/alert/manager"
	"github.com/rancher/rancher/pkg/ticker"
	v1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SysComponentWatcher struct {
	componentStatuses       v1.ComponentStatusInterface
	clusterAlertRuleLister  v3.ClusterAlertRuleLister
	clusterAlertGroupLister v3.ClusterAlertGroupLister
	alertManager            *manager.AlertManager
	clusterName             string
	clusterLister           v3.ClusterLister
}

func StartSysComponentWatcher(ctx context.Context, cluster *config.UserContext, manager *manager.AlertManager) {

	s := &SysComponentWatcher{
		componentStatuses:       cluster.Core.ComponentStatuses(""),
		clusterAlertRuleLister:  cluster.Management.Management.ClusterAlertRules(cluster.ClusterName).Controller().Lister(),
		clusterAlertGroupLister: cluster.Management.Management.ClusterAlertGroups(cluster.ClusterName).Controller().Lister(),
		alertManager:            manager,
		clusterName:             cluster.ClusterName,
		clusterLister:           cluster.Management.Management.Clusters("").Controller().Lister(),
	}
	go s.watch(ctx, syncInterval)
}

func (w *SysComponentWatcher) watch(ctx context.Context, interval time.Duration) {
	for range ticker.Context(ctx, interval) {
		err := w.watchRule()
		if err != nil {
			logrus.Infof("Failed to watch system component, error: %v", err)
		}
	}
}

func (w *SysComponentWatcher) watchRule() error {
	if w.alertManager.IsDeploy == false {
		return nil
	}

	groupsMap, err := getClusterAlertGroupsMap(w.clusterAlertGroupLister)
	if err != nil {
		return err
	}

	clusterAlerts, err := w.clusterAlertRuleLister.List("", labels.NewSelector())
	if err != nil {
		return err
	}

	statuses, err := w.componentStatuses.List(metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, rule := range clusterAlerts {
		if rule.Status.AlertState == "inactive" || rule.Spec.SystemServiceRule == nil {
			continue
		}

		if group, ok := groupsMap[rule.Spec.GroupName]; !ok || len(group.Spec.Recipients) == 0 {
			continue
		}

		if rule.Spec.SystemServiceRule != nil {
			w.checkComponentHealthy(statuses, rule)
		}
	}
	return nil
}

func (w *SysComponentWatcher) checkComponentHealthy(statuses *v1.ComponentStatusList, alert *v3.ClusterAlertRule) {
	for _, cs := range statuses.Items {
		if strings.HasPrefix(cs.Name, alert.Spec.SystemServiceRule.Condition) {
			for _, cond := range cs.Conditions {
				if cond.Type == corev1.ComponentHealthy {
					if cond.Status == corev1.ConditionFalse {
						ruleID := common.GetRuleID(alert.Spec.GroupName, alert.Name)

						clusterDisplayName := common.GetClusterDisplayName(w.clusterName, w.clusterLister)

						labels := map[string]string{}
						annotations := map[string]string{}
						common.SetExtraAlertData(labels, annotations, alert.Spec.CommonRuleField.ExtraAlertDatas, nil, nil)
						common.SetBasicAlertData(labels, ruleID, alert.Spec.GroupName, "systemService", alert.Spec.DisplayName, alert.Spec.Severity, clusterDisplayName)

						labels["component_name"] = alert.Spec.SystemServiceRule.Condition

						if cond.Message != "" {
							labels["logs"] = cond.Message
						}

						if err := w.alertManager.SendAlert(labels, annotations); err != nil {
							logrus.Errorf("Failed to send alert: %v", err)
						}
						return
					}
				}
			}
		}
	}

}
