package f5cis

import (
	"fmt"
	"reflect"

	"github.com/pkg/errors"
	"github.com/rancher/rancher/pkg/app/utils"
	"github.com/rancher/rancher/pkg/catalog/manager"
	"github.com/rancher/rancher/pkg/f5cis"
	"github.com/rancher/rancher/pkg/ref"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	v3 "github.com/rancher/types/apis/project.cattle.io/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	creatorIDAnno = "field.cattle.io/creatorId"
)

type clusterHandler struct {
	clusterName          string
	catalogManager       manager.CatalogManager
	cattleClustersClient mgmtv3.ClusterInterface
	app                  *appHandler
}

func (ch *clusterHandler) sync(key string, cluster *mgmtv3.Cluster) (runtime.Object, error) {
	if cluster == nil || cluster.DeletionTimestamp != nil || cluster.Name != ch.clusterName {
		return cluster, nil
	}

	if !mgmtv3.ClusterConditionAgentDeployed.IsTrue(cluster) {
		return cluster, nil
	}

	clusterTag := getClusterTag(cluster)
	src := cluster
	cpy := src.DeepCopy()

	err := ch.doSync(cpy)
	if !reflect.DeepEqual(cpy, src) {
		updated, updateErr := ch.cattleClustersClient.Update(cpy)
		if updateErr != nil {
			return updated, errors.Wrapf(updateErr, "failed to update Cluster %s", clusterTag)
		}

		cpy = updated
	}

	return cpy, err
}

func (ch *clusterHandler) doSync(cluster *mgmtv3.Cluster) error {
	appName, appTargetNamespace := f5cis.ClusterF5CISInfo()

	if cluster.Spec.EnableF5CIS {
		appProjectName, err := ch.ensureAppProjectName(cluster.Name, appTargetNamespace)
		if err != nil {
			mgmtv3.ClusterConditionF5CISEnabled.Unknown(cluster)
			mgmtv3.ClusterConditionF5CISEnabled.Message(cluster, err.Error())
			return errors.Wrap(err, "failed to ensure f5cis project name")
		}

		err = ch.deployApp(appName, appTargetNamespace, appProjectName, cluster)
		if err != nil {
			mgmtv3.ClusterConditionF5CISEnabled.Unknown(cluster)
			mgmtv3.ClusterConditionF5CISEnabled.Message(cluster, err.Error())
			return errors.Wrap(err, "failed to deploy f5cis")
		}

		mgmtv3.ClusterConditionF5CISEnabled.True(cluster)
		mgmtv3.ClusterConditionF5CISEnabled.Message(cluster, "")
	} else if enabledStatus := mgmtv3.ClusterConditionF5CISEnabled.GetStatus(cluster); enabledStatus != "" && enabledStatus != "False" {
		if err := ch.app.withdrawApp(cluster.Name, appName, appTargetNamespace); err != nil {
			mgmtv3.ClusterConditionF5CISEnabled.Unknown(cluster)
			mgmtv3.ClusterConditionF5CISEnabled.Message(cluster, err.Error())
			return errors.Wrap(err, "failed to withdraw f5cis")
		}

		mgmtv3.ClusterConditionF5CISEnabled.False(cluster)
		mgmtv3.ClusterConditionF5CISEnabled.Message(cluster, "")
	}

	return nil
}

func (ch *clusterHandler) ensureAppProjectName(clusterID, appTargetNamespace string) (string, error) {
	creator, err := ch.app.systemAccountManager.GetSystemUser(ch.clusterName)
	if err != nil {
		return "", err
	}

	appDeployProjectID, err := utils.GetSystemProjectID(clusterID, ch.app.projectLister)
	if err != nil {
		return "", err
	}

	appProjectName, err := utils.EnsureAppProjectName(ch.app.agentNamespaceClient, appDeployProjectID, clusterID, appTargetNamespace, creator.Name)
	if err != nil {
		return "", err
	}

	return appProjectName, nil
}

func (ch *clusterHandler) deployApp(appName, appTargetNamespace string, appProjectName string, cluster *mgmtv3.Cluster) error {
	_, appDeployProjectID := ref.Parse(appProjectName)

	creator, err := ch.app.systemAccountManager.GetSystemUser(ch.clusterName)
	if err != nil {
		return err
	}

	mustAppAnswers := map[string]string{}

	appAnswers, _, appCatalogID, err := f5cis.GetF5CISAppAnswersAndCatalogID(cluster.Annotations, ch.app.catalogTemplateLister, ch.catalogManager, ch.clusterName)
	if err != nil {
		return err
	}

	// cannot overwrite mustAppAnswers
	for mustKey, mustVal := range mustAppAnswers {
		appAnswers[mustKey] = mustVal
	}

	app := &v3.App{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{creatorIDAnno: creator.Name},
			Labels:      f5cis.OwnedLabels(appName, appTargetNamespace, appProjectName),
			Name:        appName,
			Namespace:   appDeployProjectID,
		},
		Spec: v3.AppSpec{
			Answers:         appAnswers,
			Description:     "Rancher Cluster F5 CIS",
			ExternalID:      appCatalogID,
			ProjectName:     appProjectName,
			TargetNamespace: appTargetNamespace,
		},
	}

	_, err = utils.DeployApp(ch.app.cattleAppClient, appDeployProjectID, app, false)
	if err != nil {
		return err
	}

	return nil
}

func getClusterTag(cluster *mgmtv3.Cluster) string {
	return fmt.Sprintf("%s(%s)", cluster.Name, cluster.Spec.DisplayName)
}
