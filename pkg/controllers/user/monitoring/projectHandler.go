package monitoring

import (
	"fmt"
	"reflect"

	"github.com/pkg/errors"
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

func getProjectTag(project *mgmtv3.Project, clusterName string) string {
	return fmt.Sprintf("%s(%s) of Cluster %s(%s)", project.Name, project.Spec.DisplayName, project.Spec.ClusterName, clusterName)
}
