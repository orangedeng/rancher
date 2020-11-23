package watcher

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/rancher/rancher/pkg/controllers/user/alert/common"
	"github.com/rancher/rancher/pkg/controllers/user/alert/manager"
	"github.com/rancher/rancher/pkg/controllers/user/workload"
	nodeHelper "github.com/rancher/rancher/pkg/node"
	v1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	// Kubernetes uses list/watch to update cache, it uses watch timeout to avoid situations of hanging watchers. Watchers that do not
	// receive any events within the timeout window will be stopped, the timeout is a random time between 5-10 minutes.
	// If the ListAndWatch has been terminated, it will not immediately restart, but wait for a backoff time provided by the ExponentialBackoffManager.
	// After the ListAndWatch started next time, lister will load all event and update the cache to a new version,
	// at the time lister update the cache version for all events, we will receive historical events in this handler,
	// ignoreEventPeriod use for ignoring events that lastTimestamp is two minutes ago, then prevent the user to receive old events.
	ignoreEventPeriod = 2 * time.Minute
)

type EventWatcher struct {
	eventLister             v1.EventLister
	clusterAlertRuleLister  v3.ClusterAlertRuleLister
	clusterAlertGroupLister v3.ClusterAlertGroupLister
	alertManager            *manager.AlertManager
	clusterName             string
	clusterLister           v3.ClusterLister
	workloadFetcher         workloadFetcher
	podLister               v1.PodLister
	machineLister           v3.NodeLister
}

func StartEventWatcher(ctx context.Context, cluster *config.UserContext, manager *manager.AlertManager) {
	events := cluster.Core.Events("")
	workloadFetcher := workloadFetcher{
		workloadController: workload.NewWorkloadController(ctx, cluster.UserOnlyContext(), nil),
	}

	eventWatcher := &EventWatcher{
		eventLister:             events.Controller().Lister(),
		clusterAlertRuleLister:  cluster.Management.Management.ClusterAlertRules(cluster.ClusterName).Controller().Lister(),
		clusterAlertGroupLister: cluster.Management.Management.ClusterAlertGroups(cluster.ClusterName).Controller().Lister(),
		alertManager:            manager,
		clusterName:             cluster.ClusterName,
		clusterLister:           cluster.Management.Management.Clusters("").Controller().Lister(),
		workloadFetcher:         workloadFetcher,
		podLister:               cluster.Core.Pods(metav1.NamespaceAll).Controller().Lister(),
		machineLister:           cluster.Management.Management.Nodes(cluster.ClusterName).Controller().Lister(),
	}

	events.AddHandler(ctx, "cluster-event-alert-watcher", eventWatcher.Sync)
}

func (l *EventWatcher) Sync(key string, obj *corev1.Event) (runtime.Object, error) {
	if l.alertManager.IsDeploy == false {
		return nil, nil
	}

	if obj == nil {
		return nil, nil
	}

	if time.Now().Sub(obj.LastTimestamp.Time) > ignoreEventPeriod {
		return obj, nil
	}

	groupsMap, err := getClusterAlertGroupsMap(l.clusterAlertGroupLister)
	if err != nil {
		return nil, err
	}

	clusterAlerts, err := l.clusterAlertRuleLister.List("", labels.NewSelector())
	if err != nil {
		return nil, err
	}

	machines, err := l.machineLister.List("", labels.NewSelector())
	if err != nil {
		logrus.Errorf("Failed to get machines: %v", err)
	}

	for _, alert := range clusterAlerts {
		if alert.Status.AlertState == "inactive" || alert.Status.AlertState == "muted" || alert.Spec.EventRule == nil {
			continue
		}

		if group, ok := groupsMap[alert.Spec.GroupName]; !ok || len(group.Spec.Recipients) == 0 {
			continue
		}

		if alert.Spec.EventRule.EventType == obj.Type && alert.Spec.EventRule.ResourceKind == obj.InvolvedObject.Kind {
			ruleID := common.GetRuleID(alert.Spec.GroupName, alert.Name)

			clusterDisplayName := common.GetClusterDisplayName(l.clusterName, l.clusterLister)

			labels := map[string]string{}
			annotations := map[string]string{}
			common.SetExtraAlertData(labels, annotations, alert.Spec.CommonRuleField.ExtraAlertDatas, nil, nil)
			common.SetBasicAlertData(labels, ruleID, alert.Spec.GroupName, "event", alert.Spec.DisplayName, alert.Spec.Severity, clusterDisplayName)
			labels["event_type"] = alert.Spec.EventRule.EventType
			labels["resource_kind"] = alert.Spec.EventRule.ResourceKind
			labels["target_name"] = obj.InvolvedObject.Name
			labels["target_namespace"] = obj.InvolvedObject.Namespace
			labels["event_count"] = strconv.Itoa(int(obj.Count))
			labels["event_message"] = obj.Message
			labels["event_firstseen"] = fmt.Sprintf("%s", obj.FirstTimestamp)
			labels["event_lastseen"] = fmt.Sprintf("%s", obj.LastTimestamp)

			if alert.Spec.EventRule.ResourceKind == "Node" {
				machine := nodeHelper.GetNodeByNodeName(machines, obj.InvolvedObject.Name)
				if machine != nil {
					common.SetNodeAlertData(labels, machine)
				}
			}

			if alert.Spec.EventRule.ResourceKind == "Pod" {
				pod, err := l.podLister.Get(obj.InvolvedObject.Namespace, obj.InvolvedObject.Name)
				if err != nil {
					errors.Wrapf(err, "failed to get pod %s:%s", obj.InvolvedObject.Namespace, obj.InvolvedObject.Name)
				}

				var workloadName string
				if pod != nil {
					if len(pod.OwnerReferences) == 0 {
						workloadName = pod.Name
					} else {
						ownerRef := pod.OwnerReferences[0]
						name := ownerRef.Name
						kind := ownerRef.Kind

						workloadName, err = l.getWorkloadInfo(obj.InvolvedObject.Namespace, name, kind)
						if err != nil {
							errors.Wrap(err, "failed to fetch workload info")
						}
					}
					labels["pod_ip"] = pod.Status.PodIP
				}

				if workloadName != "" {
					labels["workload_name"] = workloadName
				}
			}

			if alert.Spec.EventRule.ResourceKind == "Deployment" || alert.Spec.EventRule.ResourceKind == "StatefulSet" || alert.Spec.EventRule.ResourceKind == "DaemonSet" {
				workloadName, err := l.getWorkloadInfo(obj.InvolvedObject.Namespace, obj.InvolvedObject.Name, alert.Spec.EventRule.ResourceKind)
				if err != nil {
					errors.Wrap(err, "failed to fetch workload info")
				}

				if workloadName != "" {
					labels["workload_name"] = workloadName
				}

			}

			if err := l.alertManager.SendAlert(labels, annotations); err != nil {
				logrus.Errorf("Failed to send alert: %v", err)
			}
		}

	}

	return nil, nil
}

func (l *EventWatcher) getWorkloadInfo(namespace, name, kind string) (string, error) {

	workloadName, err := l.workloadFetcher.getWorkloadName(namespace, name, kind)
	if err != nil {
		return "", errors.Wrap(err, "Failed to get workload info for alert")
	}
	return workloadName, nil
}

type workloadFetcher struct {
	workloadController workload.CommonController
}

func (w *workloadFetcher) getWorkloadName(namespace, name, kind string) (string, error) {
	if kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "CronJob" {
		return name, nil
	}

	workloadID := fmt.Sprintf("%s:%s:%s", kind, namespace, name)
	workload, err := w.workloadController.GetByWorkloadID(workloadID)
	if err != nil {
		return "", errors.Wrapf(err, "get workload %s failed", workloadID)
	}

	allRef := workload.OwnerReferences
	if len(allRef) == 0 {
		return name, nil
	}

	ref := allRef[0]
	refName := ref.Name
	refKind := ref.Kind

	if kind == "Job" && refKind != "CronJob" {
		return name, nil
	}

	refWorkloadID := fmt.Sprintf("%s:%s:%s", refKind, namespace, refName)
	refWorkload, err := w.workloadController.GetByWorkloadID(refWorkloadID)
	if err != nil {
		return "", errors.Wrapf(err, "get workload %s failed", workloadID)
	}

	return w.getWorkloadName(refWorkload.Namespace, refWorkload.Name, refWorkload.Kind)
}

func getClusterAlertGroupsMap(clusterAlertGroupLister v3.ClusterAlertGroupLister) (map[string]*v3.ClusterAlertGroup, error) {
	allGroups, err := clusterAlertGroupLister.List("", labels.NewSelector())
	if err != nil {
		return nil, err
	}

	groupMap := map[string]*v3.ClusterAlertGroup{}
	for _, v := range allGroups {
		groupID := common.GetGroupID(v.Namespace, v.Name)
		groupMap[groupID] = v
	}
	return groupMap, nil
}