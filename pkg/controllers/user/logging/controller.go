package logging

import (
	"context"
	"strings"

	"github.com/rancher/rancher/pkg/controllers/user/logging/configsyncer"
	"github.com/rancher/rancher/pkg/controllers/user/logging/deployer"
	"github.com/rancher/rancher/pkg/controllers/user/logging/generator"
	"github.com/rancher/rancher/pkg/controllers/user/logging/watcher"
	workloadUtil "github.com/rancher/rancher/pkg/controllers/user/workload"
	"github.com/rancher/rancher/pkg/project"
	v1 "github.com/rancher/types/apis/core/v1"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func Register(ctx context.Context, cluster *config.UserContext) {

	clusterName := cluster.ClusterName
	secretManager := configsyncer.NewSecretManager(cluster)

	clusterLogging := cluster.Management.Management.ClusterLoggings(clusterName)
	projectLogging := cluster.Management.Management.ProjectLoggings(metav1.NamespaceAll)
	clusterClient := cluster.Management.Management.Clusters(metav1.NamespaceAll)
	node := cluster.Core.Nodes(metav1.NamespaceAll)

	deployer := deployer.NewDeployer(cluster, secretManager)
	clusterLogging.AddClusterScopedHandler(ctx, "cluster-logging-deployer", cluster.ClusterName, deployer.ClusterLoggingSync)
	projectLogging.AddClusterScopedHandler(ctx, "project-logging-deployer", cluster.ClusterName, deployer.ProjectLoggingSync)
	clusterClient.AddHandler(ctx, "cluster-trigger-logging-deployer-updator", deployer.ClusterSync)
	node.AddHandler(ctx, "node-syncer", deployer.NodeSync)

	workloadController := &WorkloadController{
		clusterLogging: cluster.Management.Management.ClusterLoggings(clusterName),
		projectLogging: cluster.Management.Management.ProjectLoggings(metav1.NamespaceAll),
		nsLister:       cluster.Core.Namespaces("").Controller().Lister(),
	}
	Controller := workloadUtil.NewWorkloadController(ctx, cluster.UserOnlyContext(), workloadController.sync)

	configSyncer := configsyncer.NewConfigSyncer(cluster, secretManager, Controller)
	clusterLogging.AddClusterScopedHandler(ctx, "cluster-logging-configsyncer", cluster.ClusterName, configSyncer.ClusterLoggingSync)
	projectLogging.AddClusterScopedHandler(ctx, "project-logging-configsyncer", cluster.ClusterName, configSyncer.ProjectLoggingSync)

	namespaces := cluster.Core.Namespaces(metav1.NamespaceAll)
	namespaces.AddClusterScopedHandler(ctx, "namespace-logging-configsysncer", cluster.ClusterName, configSyncer.NamespaceSync)

	watcher.StartEndpointWatcher(ctx, cluster)
}

type WorkloadController struct {
	clusterLogging mgmtv3.ClusterLoggingInterface
	projectLogging mgmtv3.ProjectLoggingInterface
	nsLister       v1.NamespaceLister
}

func (w *WorkloadController) sync(key string, obj *workloadUtil.Workload) error {
	if strings.EqualFold(obj.Kind, workloadUtil.ReplicationControllerType) || strings.EqualFold(obj.Kind, workloadUtil.ReplicaSetType) {
		return nil
	}

	if _, ok := obj.TemplateSpec.Annotations[generator.LoggingExcludeAnnotation]; !ok {
		return nil
	}

	clusterLoggings, err := w.clusterLogging.Controller().Lister().List("", labels.NewSelector())
	if err != nil {
		return err
	}

	if len(clusterLoggings) > 0 {
		w.clusterLogging.Controller().Enqueue(clusterLoggings[0].Namespace, clusterLoggings[0].Name)
	}

	projectLogging, err := w.GetProjectLogging(obj.Namespace)
	if err != nil {
		return err
	}

	if projectLogging != nil {
		w.projectLogging.Controller().Enqueue(projectLogging.Namespace, projectLogging.Name)
	}

	return nil
}

func (w *WorkloadController) GetProjectLogging(workloadNamespace string) (*mgmtv3.ProjectLogging, error) {
	namespace, err := w.nsLister.Get("", workloadNamespace)
	if err != nil {
		return nil, err
	}

	projectID, ok := namespace.Annotations[project.ProjectIDAnn]
	if !ok {
		return nil, nil
	}

	projectLoggings, err := w.projectLogging.Controller().Lister().List("", labels.NewSelector())
	if err != nil {
		return nil, err
	}

	for _, projectLogging := range projectLoggings {
		if projectLogging.Spec.ProjectName == projectID {
			return projectLogging, nil
		}
	}

	return nil, nil
}
