package watcher

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/rancher/norman/controller"
	"github.com/rancher/rancher/pkg/controllers/user/alert/common"
	"github.com/rancher/rancher/pkg/controllers/user/alert/manager"
	"github.com/rancher/rancher/pkg/controllers/user/workload"
	nodeHelper "github.com/rancher/rancher/pkg/node"
	"github.com/rancher/rancher/pkg/ticker"
	v1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

type PodWatcher struct {
	podLister               v1.PodLister
	alertManager            *manager.AlertManager
	projectAlertPolicies    v3.ProjectAlertRuleInterface
	projectAlertRuleLister  v3.ProjectAlertRuleLister
	clusterName             string
	podRestartTrack         sync.Map
	clusterLister           v3.ClusterLister
	projectLister           v3.ProjectLister
	workloadFetcher         workloadFetcher
	projectAlertGroupLister v3.ProjectAlertGroupLister
	machineLister           v3.NodeLister
}

type restartTrack struct {
	Count int32
	Time  time.Time
}

func StartPodWatcher(ctx context.Context, cluster *config.UserContext, manager *manager.AlertManager) {
	projectAlertPolicies := cluster.Management.Management.ProjectAlertRules("")
	workloadFetcher := workloadFetcher{
		workloadController: workload.NewWorkloadController(ctx, cluster.UserOnlyContext(), nil),
	}

	podWatcher := &PodWatcher{
		podLister:               cluster.Core.Pods("").Controller().Lister(),
		projectAlertPolicies:    projectAlertPolicies,
		projectAlertRuleLister:  projectAlertPolicies.Controller().Lister(),
		alertManager:            manager,
		clusterName:             cluster.ClusterName,
		podRestartTrack:         sync.Map{},
		clusterLister:           cluster.Management.Management.Clusters("").Controller().Lister(),
		projectLister:           cluster.Management.Management.Projects(cluster.ClusterName).Controller().Lister(),
		workloadFetcher:         workloadFetcher,
		projectAlertGroupLister: cluster.Management.Management.ProjectAlertGroups("").Controller().Lister(),
		machineLister:           cluster.Management.Management.Nodes(cluster.ClusterName).Controller().Lister(),
	}

	projectAlertLifecycle := &ProjectAlertLifecycle{
		podWatcher: podWatcher,
	}
	projectAlertPolicies.AddClusterScopedLifecycle(ctx, "pod-target-alert-watcher", cluster.ClusterName, projectAlertLifecycle)

	go podWatcher.watch(ctx, syncInterval)
}

func (w *PodWatcher) watch(ctx context.Context, interval time.Duration) {
	for range ticker.Context(ctx, interval) {
		err := w.watchRule()
		if err != nil {
			logrus.Infof("Failed to watch pod, error: %v", err)
		}
	}
}

type ProjectAlertLifecycle struct {
	podWatcher *PodWatcher
}

func (l *ProjectAlertLifecycle) Create(obj *v3.ProjectAlertRule) (runtime.Object, error) {
	return obj, nil
}

func (l *ProjectAlertLifecycle) Updated(obj *v3.ProjectAlertRule) (runtime.Object, error) {
	return obj, nil
}

func (l *ProjectAlertLifecycle) Remove(obj *v3.ProjectAlertRule) (runtime.Object, error) {
	l.podWatcher.podRestartTrack.Delete(obj.Namespace + ":" + obj.Name)
	return obj, nil
}

func (w *PodWatcher) watchRule() error {
	if w.alertManager.IsDeploy == false {
		return nil
	}

	projectAlerts, err := w.projectAlertRuleLister.List("", labels.NewSelector())
	if err != nil {
		return err
	}

	pAlerts := []*v3.ProjectAlertRule{}
	for _, alert := range projectAlerts {
		if controller.ObjectInCluster(w.clusterName, alert) {
			pAlerts = append(pAlerts, alert)
		}
	}

	groupsMap, err := getProjectAlertGroupsMap(w.clusterName, w.projectAlertGroupLister)
	if err != nil {
		return err
	}

	for _, alert := range pAlerts {
		if alert.Status.AlertState == "inactive" || alert.Spec.PodRule == nil {
			continue
		}

		if group, ok := groupsMap[alert.Spec.GroupName]; !ok || len(group.Spec.Recipients) == 0 {
			continue
		}

		parts := strings.Split(alert.Spec.PodRule.PodName, ":")
		if len(parts) < 2 {
			//TODO: for invalid format pod
			if err = w.projectAlertPolicies.DeleteNamespaced(alert.Namespace, alert.Name, &metav1.DeleteOptions{}); err != nil {
				return err
			}
			continue
		}

		ns := parts[0]
		podID := parts[1]
		newPod, err := w.podLister.Get(ns, podID)
		if err != nil {
			//TODO: what to do when pod not found
			if kerrors.IsNotFound(err) || newPod == nil {
				if err = w.projectAlertPolicies.DeleteNamespaced(alert.Namespace, alert.Name, &metav1.DeleteOptions{}); err != nil {
					return err
				}
			}
			logrus.Debugf("Failed to get pod %s: %v", podID, err)

			continue
		}

		switch alert.Spec.PodRule.Condition {
		case "notrunning":
			w.checkPodRunning(newPod, alert)
		case "notscheduled":
			w.checkPodScheduled(newPod, alert)
		case "restarts":
			w.checkPodRestarts(newPod, alert)
		}
	}

	return nil
}

func (w *PodWatcher) checkPodRestarts(pod *corev1.Pod, alert *v3.ProjectAlertRule) {

	for _, containerStatus := range pod.Status.ContainerStatuses {
		curCount := containerStatus.RestartCount
		preCount := w.getRestartTimeFromTrack(alert, curCount)

		if curCount-preCount >= int32(alert.Spec.PodRule.RestartTimes) {
			ruleID := common.GetRuleID(alert.Spec.GroupName, alert.Name)

			details := ""
			if containerStatus.State.Waiting != nil {
				details = containerStatus.State.Waiting.Message
			}

			clusterDisplayName := common.GetClusterDisplayName(w.clusterName, w.clusterLister)
			projectDisplayName := common.GetProjectDisplayName(alert.Spec.ProjectName, w.projectLister)

			labels := map[string]string{}
			annotations := map[string]string{}
			common.SetExtraAlertData(labels, annotations, alert.Spec.CommonRuleField.ExtraAlertDatas, pod.Labels, pod.Annotations)
			common.SetBasicAlertData(labels, ruleID, alert.Spec.GroupName, "podRestarts", alert.Spec.DisplayName, alert.Spec.Severity, clusterDisplayName)

			labels["project_name"] = projectDisplayName
			labels["namespace"] = pod.Namespace
			labels["pod_name"] = pod.Name
			labels["container_name"] = containerStatus.Name
			labels["restart_times"] = strconv.Itoa(alert.Spec.PodRule.RestartTimes)
			labels["restart_interval"] = strconv.Itoa(alert.Spec.PodRule.RestartIntervalSeconds)
			labels["pod_ip"] = pod.Status.PodIP

			if pod.Spec.NodeName != "" {
				w.setNodeData(labels, pod.Spec.NodeName)
			}

			if details != "" {
				labels["logs"] = details
			}

			workloadName, err := w.getWorkloadInfo(pod)
			if err != nil {
				logrus.Warnf("Failed to get workload info for %s:%s %v", pod.Namespace, pod.Name, err)
			}
			if workloadName != "" {
				labels["workload_name"] = workloadName
			}

			if err := w.alertManager.SendAlert(labels, annotations); err != nil {
				logrus.Debugf("Error occurred while getting pod %s: %v", alert.Spec.PodRule.PodName, err)
			}
		}

		return
	}

}

func (w *PodWatcher) getRestartTimeFromTrack(alert *v3.ProjectAlertRule, curCount int32) int32 {
	name := alert.Name
	namespace := alert.Namespace
	now := time.Now()
	currentRestartTrack := restartTrack{Count: curCount, Time: now}
	currentRestartTrackArr := []restartTrack{currentRestartTrack}

	obj, loaded := w.podRestartTrack.LoadOrStore(namespace+":"+name, currentRestartTrackArr)
	if loaded {
		tracks := obj.([]restartTrack)
		for i, track := range tracks {
			if now.Sub(track.Time).Seconds() < float64(alert.Spec.PodRule.RestartIntervalSeconds) {
				tracks = tracks[i:]
				tracks = append(tracks, currentRestartTrack)
				w.podRestartTrack.Store(namespace+":"+name, tracks)
				return track.Count
			}
		}
	}

	return curCount
}

func (w *PodWatcher) checkPodRunning(pod *corev1.Pod, alert *v3.ProjectAlertRule) {
	if !w.checkPodScheduled(pod, alert) {
		return
	}

	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.State.Running == nil {
			ruleID := common.GetRuleID(alert.Spec.GroupName, alert.Name)

			//TODO: need to consider all the cases
			details := ""
			if containerStatus.State.Waiting != nil {
				details = containerStatus.State.Waiting.Message
			}

			if containerStatus.State.Terminated != nil {
				details = containerStatus.State.Terminated.Message
			}

			clusterDisplayName := common.GetClusterDisplayName(w.clusterName, w.clusterLister)
			projectDisplayName := common.GetProjectDisplayName(alert.Spec.ProjectName, w.projectLister)

			labels := map[string]string{}
			annotations := map[string]string{}
			common.SetExtraAlertData(labels, annotations, alert.Spec.CommonRuleField.ExtraAlertDatas, pod.Labels, pod.Annotations)
			common.SetBasicAlertData(labels, ruleID, alert.Spec.GroupName, "podNotRunning", alert.Spec.DisplayName, alert.Spec.Severity, clusterDisplayName)

			labels["namespace"] = pod.Namespace
			labels["project_name"] = projectDisplayName
			labels["pod_name"] = pod.Name
			labels["container_name"] = containerStatus.Name
			labels["pod_ip"] = pod.Status.PodIP

			if pod.Spec.NodeName != "" {
				w.setNodeData(labels, pod.Spec.NodeName)
			}

			if details != "" {
				labels["logs"] = details
			}

			workloadName, err := w.getWorkloadInfo(pod)
			if err != nil {
				logrus.Warnf("Failed to get workload info for %s:%s %v", pod.Namespace, pod.Name, err)
			}
			if workloadName != "" {
				labels["workload_name"] = workloadName
			}

			if err := w.alertManager.SendAlert(labels, annotations); err != nil {
				logrus.Debugf("Error occurred while send alert %s: %v", alert.Spec.PodRule.PodName, err)
			}
			return
		}
	}
}

func (w *PodWatcher) checkPodScheduled(pod *corev1.Pod, alert *v3.ProjectAlertRule) bool {

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			ruleID := common.GetRuleID(alert.Spec.GroupName, alert.Name)
			details := condition.Message

			clusterDisplayName := common.GetClusterDisplayName(w.clusterName, w.clusterLister)
			projectDisplayName := common.GetProjectDisplayName(alert.Spec.ProjectName, w.projectLister)

			labels := map[string]string{}
			annotations := map[string]string{}
			common.SetExtraAlertData(labels, annotations, alert.Spec.CommonRuleField.ExtraAlertDatas, pod.Labels, pod.Annotations)
			common.SetBasicAlertData(labels, ruleID, alert.Spec.GroupName, "podNotScheduled", alert.Spec.DisplayName, alert.Spec.Severity, clusterDisplayName)

			labels["namespace"] = pod.Namespace
			labels["project_name"] = projectDisplayName
			labels["pod_name"] = pod.Name
			labels["pod_ip"] = pod.Status.PodIP

			if pod.Spec.NodeName != "" {
				w.setNodeData(labels, pod.Spec.NodeName)
			}

			if details != "" {
				labels["logs"] = details
			}

			workloadName, err := w.getWorkloadInfo(pod)
			if err != nil {
				logrus.Warnf("Failed to get workload info for %s:%s %v", pod.Namespace, pod.Name, err)
			}
			if workloadName != "" {
				labels["workload_name"] = workloadName
			}

			if err := w.alertManager.SendAlert(labels, annotations); err != nil {
				logrus.Debugf("Error occurred while getting pod %s: %v", alert.Spec.PodRule.PodName, err)
			}
			return false
		}
	}

	return true

}

func (w *PodWatcher) getWorkloadInfo(pod *corev1.Pod) (string, error) {
	if len(pod.OwnerReferences) == 0 {
		return pod.Name, nil
	}
	ownerRef := pod.OwnerReferences[0]
	workloadName, err := w.workloadFetcher.getWorkloadName(pod.Namespace, ownerRef.Name, ownerRef.Kind)
	if err != nil {
		return "", errors.Wrap(err, "Failed to get workload info for alert")
	}
	return workloadName, nil
}

func getProjectAlertGroupsMap(clusterName string, projectAlertGroupLister v3.ProjectAlertGroupLister) (map[string]*v3.ProjectAlertGroup, error) {
	allGroups, err := projectAlertGroupLister.List("", labels.NewSelector())
	if err != nil {
		return nil, err
	}

	groupMap := map[string]*v3.ProjectAlertGroup{}
	for _, v := range allGroups {
		groupID := common.GetGroupID(v.Namespace, v.Name)
		if controller.ObjectInCluster(clusterName, v) {
			groupMap[groupID] = v
		}
	}
	return groupMap, nil
}

func (w *PodWatcher) setNodeData(alertLabels map[string]string, nodeName string) {
	machines, err := w.machineLister.List("", labels.NewSelector())
	if err != nil {
		logrus.Errorf("Failed to get machines: %v", err)
	}

	machine := nodeHelper.GetNodeByNodeName(machines, nodeName)
	if machine != nil {
		common.SetNodeAlertData(alertLabels, machine)
	}
}
