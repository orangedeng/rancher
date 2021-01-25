package monitoring

import (
	"fmt"
	"reflect"

	"github.com/pkg/errors"
	"github.com/rancher/rancher/pkg/app/utils"
	"github.com/rancher/rancher/pkg/catalog/manager"
	"github.com/rancher/rancher/pkg/monitoring"
	"github.com/rancher/rancher/pkg/ref"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	prtbBySA = "monitoring.project.cattle.io/prtb-by-sa"
)

type projectHandler struct {
	clusterName         string
	clusterLister       mgmtv3.ClusterLister
	catalogManager      manager.CatalogManager
	cattleProjectClient mgmtv3.ProjectInterface
	app                 *appHandler
}

func (ph *projectHandler) sync(key string, project *mgmtv3.Project) (runtime.Object, error) {
	if project == nil || project.DeletionTimestamp != nil ||
		project.Spec.ClusterName != ph.clusterName {
		return project, nil
	}

	clusterID := project.Spec.ClusterName
	cluster, err := ph.clusterLister.Get("", clusterID)
	if err != nil {
		return project, errors.Wrapf(err, "failed to find Cluster %s", clusterID)
	}

	clusterName := cluster.Spec.DisplayName
	projectTag := getProjectTag(project, clusterName)
	src := project
	cpy := src.DeepCopy()

	err = ph.doSync(cpy, clusterName)
	if !reflect.DeepEqual(cpy, src) {
		updated, updateErr := ph.cattleProjectClient.Update(cpy)
		if updateErr != nil {
			return project, errors.Wrapf(updateErr, "failed to update Project %s", projectTag)
		}

		cpy = updated
	}

	if err != nil {
		err = errors.Wrapf(err, "unable to sync Project %s", projectTag)
	}

	return cpy, err
}

func (ph *projectHandler) doSync(project *mgmtv3.Project, clusterName string) error {
	if !mgmtv3.NamespaceBackedResource.IsTrue(project) && !mgmtv3.ProjectConditionInitialRolesPopulated.IsTrue(project) {
		return nil
	}
	_, err := mgmtv3.ProjectConditionMetricExpressionDeployed.DoUntilTrue(project, func() (runtime.Object, error) {
		projectName := fmt.Sprintf("%s:%s", project.Spec.ClusterName, project.Name)

		for _, graph := range preDefinedProjectGraph {
			newObj := graph.DeepCopy()
			newObj.Namespace = project.Name
			newObj.Spec.ProjectName = projectName
			if _, err := ph.app.cattleProjectGraphClient.Create(newObj); err != nil && !apierrors.IsAlreadyExists(err) {
				return project, err
			}
		}

		return project, nil
	})
	if err != nil {
		return errors.Wrap(err, "failed to apply metric expression")
	}

	return nil
}

func (ph *projectHandler) ensureAppProjectName(appTargetNamespace string, project *mgmtv3.Project) (string, error) {
	creator, err := ph.app.systemAccountManager.GetProjectSystemUser(project.Name)
	if err != nil {
		return "", err
	}

	appProjectName, err := utils.EnsureAppProjectName(ph.app.agentNamespaceClient, project.Name, project.Spec.ClusterName, appTargetNamespace, creator.Name)
	if err != nil {
		return "", err
	}

	return appProjectName, nil
}

func (ph *projectHandler) deployApp(appName, appTargetNamespace string, appProjectName string, project *mgmtv3.Project, clusterName string) error {
	appDeployProjectID := project.Name
	clusterPrometheusSvcName, clusterPrometheusSvcNamespaces, clusterPrometheusPort := monitoring.ClusterPrometheusEndpoint()
	clusterAlertManagerSvcName, clusterAlertManagerSvcNamespaces, clusterAlertManagerPort := monitoring.ClusterAlertManagerEndpoint()
	optionalAppAnswers := map[string]string{
		"grafana.persistence.enabled": "false",

		"prometheus.persistence.enabled": "false",

		"prometheus.sync.mode": "federate",
	}

	mustAppAnswers := map[string]string{
		"operator.enabled": "false",

		"exporter-coredns.enabled": "false",

		"exporter-kube-controller-manager.enabled": "false",

		"exporter-kube-dns.enabled": "false",

		"exporter-kube-scheduler.enabled": "false",

		"exporter-kube-state.enabled": "false",

		"exporter-kubelets.enabled": "false",

		"exporter-kubernetes.enabled": "false",

		"exporter-node.enabled": "false",

		"exporter-fluentd.enabled": "false",

		"grafana.enabled":  "true",
		"grafana.level":    "project",
		"grafana.apiGroup": monitoring.APIVersion.Group,

		"prometheus.enabled":                       "true",
		"prometheus.level":                         "project",
		"prometheus.apiGroup":                      monitoring.APIVersion.Group,
		"prometheus.serviceAccountNameOverride":    appName,
		"prometheus.project.alertManagerTarget":    fmt.Sprintf("%s.%s:%s", clusterAlertManagerSvcName, clusterAlertManagerSvcNamespaces, clusterAlertManagerPort),
		"prometheus.project.projectDisplayName":    project.Spec.DisplayName,
		"prometheus.project.clusterDisplayName":    clusterName,
		"prometheus.cluster.alertManagerNamespace": clusterAlertManagerSvcNamespaces,
	}

	appAnswers, appCatalogID, err := monitoring.OverwriteAppAnswersAndCatalogID(optionalAppAnswers, project.Annotations, ph.app.catalogTemplateLister, ph.catalogManager, ph.clusterName)
	if err != nil {
		return err
	}

	// cannot overwrite mustAppAnswers
	for mustKey, mustVal := range mustAppAnswers {
		appAnswers[mustKey] = mustVal
	}

	// complete sync target & path
	if appAnswers["prometheus.sync.mode"] == "federate" {
		appAnswers["prometheus.sync.target"] = fmt.Sprintf("%s.%s:%s", clusterPrometheusSvcName, clusterPrometheusSvcNamespaces, clusterPrometheusPort)
		appAnswers["prometheus.sync.path"] = "/federate"
	} else {
		appAnswers["prometheus.sync.target"] = fmt.Sprintf("http://%s.%s:%s", clusterPrometheusSvcName, clusterPrometheusSvcNamespaces, clusterPrometheusPort)
		appAnswers["prometheus.sync.path"] = "/api/v1/read"
	}

	creator, err := ph.app.systemAccountManager.GetProjectSystemUser(project.Name)
	if err != nil {
		return err
	}

	app := &v3.App{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{creatorIDAnno: creator.Name},
			Labels:      monitoring.OwnedLabels(appName, appTargetNamespace, appProjectName, monitoring.ProjectLevel),
			Name:        appName,
			Namespace:   appDeployProjectID,
		},
		Spec: v3.AppSpec{
			Answers:         appAnswers,
			Description:     "Rancher Project Monitoring",
			ExternalID:      appCatalogID,
			ProjectName:     appProjectName,
			TargetNamespace: appTargetNamespace,
		},
	}

	deployed, err := utils.DeployApp(ph.app.cattleAppClient, appDeployProjectID, app, false)
	if err != nil {
		return err
	}

	return ph.deployAppRoles(deployed)
}

func (ph *projectHandler) detectAppComponentsWhileInstall(appName, appTargetNamespace string, project *mgmtv3.Project) error {
	if project.Status.MonitoringStatus == nil {
		project.Status.MonitoringStatus = &mgmtv3.MonitoringStatus{
			Conditions: []mgmtv3.MonitoringCondition{
				{Type: mgmtv3.ClusterConditionType(ConditionPrometheusDeployed), Status: k8scorev1.ConditionFalse},
				{Type: mgmtv3.ClusterConditionType(ConditionGrafanaDeployed), Status: k8scorev1.ConditionFalse},
			},
		}
	}
	monitoringStatus := project.Status.MonitoringStatus

	checkers := make([]func() error, 0, len(monitoringStatus.Conditions))
	if !ConditionGrafanaDeployed.IsTrue(monitoringStatus) {
		checkers = append(checkers, func() error {
			return isGrafanaDeployed(ph.app.agentDeploymentClient, appTargetNamespace, appName, monitoringStatus, project.Spec.ClusterName)
		})
	}
	if !ConditionPrometheusDeployed.IsTrue(monitoringStatus) {
		checkers = append(checkers, func() error {
			return isPrometheusDeployed(ph.app.agentStatefulSetClient, appTargetNamespace, appName, monitoringStatus)
		})
	}

	if len(checkers) == 0 {
		return nil
	}

	err := stream(checkers...)
	if err != nil {
		time.Sleep(5 * time.Second)
	}

	return err
}

func (ph *projectHandler) detectAppComponentsWhileUninstall(appName, appTargetNamespace string, project *mgmtv3.Project) error {
	if project.Status.MonitoringStatus == nil {
		return nil
	}
	monitoringStatus := project.Status.MonitoringStatus

	checkers := make([]func() error, 0, len(monitoringStatus.Conditions))
	if !ConditionGrafanaDeployed.IsFalse(monitoringStatus) {
		checkers = append(checkers, func() error {
			return isGrafanaWithdrew(ph.app.agentDeploymentClient, appTargetNamespace, appName, monitoringStatus)
		})
	}
	if !ConditionPrometheusDeployed.IsFalse(monitoringStatus) {
		checkers = append(checkers, func() error {
			return isPrometheusWithdrew(ph.app.agentStatefulSetClient, appTargetNamespace, appName, monitoringStatus)
		})
	}

	if len(checkers) == 0 {
		return nil
	}

	err := stream(checkers...)
	if err != nil {
		time.Sleep(5 * time.Second)
	}

	return err
}

func getProjectTag(project *mgmtv3.Project, clusterName string) string {
	return fmt.Sprintf("%s(%s) of Cluster %s(%s)", project.Name, project.Spec.DisplayName, project.Spec.ClusterName, clusterName)
}
