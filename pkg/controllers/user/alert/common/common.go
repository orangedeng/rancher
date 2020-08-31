package common

import (
	"fmt"

	nodeHelper "github.com/rancher/rancher/pkg/node"
	"github.com/rancher/rancher/pkg/ref"
	"github.com/rancher/rancher/pkg/settings"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
)

const (
	typeLabels      = "labels"
	typeAnnotations = "annotations"
)

func SetExtraAlertData(alertLabels, alertAnnotations map[string]string, extraAlertDatas []v3.ExtraAlertData, objLabels, objAnnotations map[string]string) {
	for _, extraAlertData := range extraAlertDatas {
		switch extraAlertData.SourceType {
		case typeLabels:
			if resourceValue, ok := objLabels[extraAlertData.SourceValue]; ok {
				setExpectValue(alertLabels, alertAnnotations, extraAlertData.TargetType, extraAlertData.TargetKey, resourceValue)
			}
		case typeAnnotations:
			if resourceValue, ok := objAnnotations[extraAlertData.SourceValue]; ok {
				setExpectValue(alertLabels, alertAnnotations, extraAlertData.TargetType, extraAlertData.TargetKey, resourceValue)
			}
		default:
			setExpectValue(alertLabels, alertAnnotations, extraAlertData.TargetType, extraAlertData.TargetKey, extraAlertData.SourceValue)
		}
	}
}

func setExpectValue(alertLabels, alertAnnotations map[string]string, targetType, targetKey, resourceValue string) {
	if targetType == typeLabels {
		alertLabels[targetKey] = resourceValue
	} else if targetType == typeAnnotations {
		alertAnnotations[targetKey] = resourceValue
	}
}

func SetBasicAlertData(alertLabels map[string]string, ruleID, groupID, alertType, alertName, serverity, clusterName string) {
	alertLabels["rule_id"] = ruleID
	alertLabels["group_id"] = groupID
	alertLabels["alert_type"] = alertType
	alertLabels["alert_name"] = alertName
	alertLabels["severity"] = serverity
	alertLabels["cluster_name"] = clusterName
	alertLabels["server_url"] = settings.ServerURL.Get()
}

func SetNodeAlertData(alertLabels map[string]string, machine *v3.Node) {
	alertLabels["node_name"] = nodeHelper.GetNodeName(machine)
	alertLabels["node_ip"] = nodeHelper.GetEndpointNodeIP(machine)
}

func GetRuleID(groupID string, ruleName string) string {
	return fmt.Sprintf("%s_%s", groupID, ruleName)
}

func GetGroupID(namespace, name string) string {
	return fmt.Sprintf("%s:%s", namespace, name)
}

func GetAlertManagerSecretName(appName string) string {
	return fmt.Sprintf("alertmanager-%s", appName)
}

func GetAlertManagerDaemonsetName(appName string) string {
	return fmt.Sprintf("alertmanager-%s", appName)
}

func formatProjectDisplayName(projectDisplayName, projectID string) string {
	return fmt.Sprintf("%s (ID: %s)", projectDisplayName, projectID)
}

func formatClusterDisplayName(clusterDisplayName, clusterID string) string {
	return fmt.Sprintf("%s (ID: %s)", clusterDisplayName, clusterID)
}

func GetClusterDisplayName(clusterName string, clusterLister v3.ClusterLister) string {
	cluster, err := clusterLister.Get("", clusterName)
	if err != nil {
		logrus.Warnf("Failed to get cluster for %s: %v", clusterName, err)
		return clusterName
	}

	return formatClusterDisplayName(cluster.Spec.DisplayName, clusterName)
}

func GetProjectDisplayName(projectID string, projectLister v3.ProjectLister) string {
	clusterName, projectName := ref.Parse(projectID)
	project, err := projectLister.Get(clusterName, projectName)
	if err != nil {
		logrus.Warnf("Failed to get project %s: %v", projectID, err)
		return projectID
	}

	return formatProjectDisplayName(project.Spec.DisplayName, projectID)
}
