package globalmonitoring

import (
	"strings"

	"github.com/rancher/norman/types/slice"
	"github.com/rancher/rancher/pkg/project"
	"github.com/rancher/rancher/pkg/settings"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	v3 "github.com/rancher/types/apis/project.cattle.io/v3"
	"github.com/sirupsen/logrus"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	globalMonitoringAppName    = "global-monitoring"
	globalDataNamespace        = "cattle-global-data"
	globalMonitoringSecretName = "global-monitoring"
	clusterIDAnswerKey         = "clusterIds"
)

type clusterHandler struct {
	clusterClient mgmtv3.ClusterInterface
	appLister     v3.AppLister
	appClient     v3.AppInterface
	projectLister mgmtv3.ProjectLister
}

func (ch *clusterHandler) sync(key string, cluster *mgmtv3.Cluster) (runtime.Object, error) {
	if cluster == nil || cluster.DeletionTimestamp != nil {
		return cluster, nil
	}

	globalMonitoringClusterID := settings.GlobalMonitoringClusterID.Get()
	if globalMonitoringClusterID == "" {
		return cluster, nil
	}
	systemProject, err := project.GetSystemProject(globalMonitoringClusterID, ch.projectLister)
	if err != nil {
		return cluster, err
	}
	app, err := ch.appLister.Get(systemProject.Name, globalMonitoringAppName)
	if k8serrors.IsNotFound(err) {
		//global monitoring is not enabled
		return cluster, nil
	} else if err != nil {
		return cluster, err
	}

	if app.Spec.Answers == nil || app.Spec.Answers[rancherHostKey] == "" {
		//app is not initialized
		return cluster, nil
	}
	var appliedClusterIDs []string
	appliedClusterIDAnswer := app.Spec.Answers[clusterIDAnswerKey]
	if appliedClusterIDAnswer != "" {
		appliedClusterIDs = strings.Split(appliedClusterIDAnswer, ":")
	}
	if cluster.Spec.EnableClusterMonitoring {
		if !slice.ContainsString(appliedClusterIDs, cluster.Name) {
			toUpdateClusterIDs := append(appliedClusterIDs, cluster.Name)
			toUpdateApp := app.DeepCopy()
			logrus.Debugf("cluster monitoring of %v enabled, updating global monitoring app answers", cluster.Name)
			toUpdateApp.Spec.Answers[clusterIDAnswerKey] = strings.Join(toUpdateClusterIDs, ":")
			if _, err := ch.appClient.Update(toUpdateApp); err != nil {
				return cluster, err
			}
		}
		if err := UpdateClusterMonitoringAnswers(ch.clusterClient, cluster, app); err != nil {
			return cluster, err
		}
	} else {
		if slice.ContainsString(appliedClusterIDs, cluster.Name) {
			toUpdateClusterIDs := removeElement(appliedClusterIDs, cluster.Name)
			toUpdateApp := app.DeepCopy()
			logrus.Debugf("cluster monitoring of %v enabled, updating global monitoring app answers", cluster.Name)
			toUpdateApp.Spec.Answers[clusterIDAnswerKey] = strings.Join(toUpdateClusterIDs, ":")
			if _, err := ch.appClient.Update(toUpdateApp); err != nil {
				return cluster, err
			}
		}
	}
	return cluster, nil

}

func removeElement(slice []string, item string) []string {
	for i, j := range slice {
		if j == item {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}
