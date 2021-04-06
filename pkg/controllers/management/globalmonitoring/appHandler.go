package globalmonitoring

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/rancher/rancher/pkg/controllers/management/rbac"
	"github.com/rancher/rancher/pkg/monitoring"
	"github.com/rancher/rancher/pkg/randomtoken"
	"github.com/rancher/rancher/pkg/ref"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/rancher/pkg/systemaccount"
	v1 "github.com/rancher/types/apis/core/v1"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	v3 "github.com/rancher/types/apis/project.cattle.io/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	ThanosEnabledAnswerKey       = "prometheus.thanos.enabled"
	rancherHostKey               = "rancherHost"
	objectConfigAnswerPrefix     = "thanos.objectConfig"
	prometheusThanosAnswerPrefix = "prometheus.thanos"
	authTokenKey                 = "token"
	apiTokenKey                  = "apiToken"
	systemAccountName            = "global-monitoring"
)

type appHandler struct {
	clusterClient        mgmtv3.ClusterInterface
	appClient            v3.AppInterface
	clusterLister        mgmtv3.ClusterLister
	secretLister         v1.SecretLister
	secretClient         v1.SecretInterface
	systemAccountManager *systemaccount.Manager
}

func (ah *appHandler) Create(obj *v3.App) (runtime.Object, error) {
	return obj, nil
}

func (ah *appHandler) Updated(obj *v3.App) (runtime.Object, error) {
	if !isGlobalMonitoringApp(obj) {
		return obj, nil
	}
	return ah.sync(obj)
}

func (ah *appHandler) Remove(obj *v3.App) (runtime.Object, error) {
	if !isGlobalMonitoringApp(obj) {
		return obj, nil
	}
	if err := ah.systemAccountManager.RemoveSystemAccount(systemAccountName); err != nil {
		return obj, err
	}
	return ah.sync(obj)
}

func (ah *appHandler) sync(app *v3.App) (runtime.Object, error) {
	if app == nil {
		return app, nil
	}

	if app.Spec.Answers == nil || app.Spec.Answers[rancherHostKey] == "" {
		return app, ah.initGlobalMonitoringApp(app)
	}

	if err := ah.UpdateAllClusterMonitoringThanosEnabled(app); err != nil {
		return app, err
	}
	return app, nil
}

func (ah *appHandler) initGlobalMonitoringApp(app *v3.App) error {
	clusters, err := ah.clusterLister.List("", labels.NewSelector())
	if err != nil {
		return err
	}
	var monitoringEnabledClusters []string
	for _, c := range clusters {
		if !c.Spec.EnableClusterMonitoring {
			continue
		}
		monitoringEnabledClusters = append(monitoringEnabledClusters, c.Name)
	}
	toUpdateApp := app.DeepCopy()
	if toUpdateApp.Spec.Answers == nil {
		toUpdateApp.Spec.Answers = make(map[string]string)
	}
	serverURL := settings.ServerURL.Get()
	u, err := url.Parse(serverURL)
	if err != nil {
		return err
	}

	if app.Annotations == nil || app.Annotations[rbac.CreatorIDAnn] == "" {
		return fmt.Errorf("unknown creator ID of app %q", app.Name)
	}
	token, err := ah.GetOrCreateGlobalMonitoringToken()
	if err != nil {
		return err
	}
	apiToken, err := ah.systemAccountManager.GetOrCreateSystemGlobalToken(systemAccountName, app.Annotations[rbac.CreatorIDAnn])
	if err != nil {
		return err
	}

	toUpdateApp.Spec.Answers[rancherHostKey] = u.Host
	toUpdateApp.Spec.Answers[clusterIDAnswerKey] = strings.Join(monitoringEnabledClusters, ":")
	toUpdateApp.Spec.Answers[authTokenKey] = token
	toUpdateApp.Spec.Answers[apiTokenKey] = apiToken
	_, err = ah.appClient.Update(toUpdateApp)
	return err
}

func (ah *appHandler) GetOrCreateGlobalMonitoringToken() (string, error) {
	secret, err := ah.secretLister.Get(globalDataNamespace, globalMonitoringSecretName)
	if err == nil {
		return string(secret.Data[authTokenKey]), nil
	} else if !apierrors.IsNotFound(err) {
		return "", err
	}

	token, err := randomtoken.Generate()
	if err != nil {
		return "", err
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      globalMonitoringSecretName,
			Namespace: globalDataNamespace,
		},
		StringData: map[string]string{
			authTokenKey: token,
		},
	}
	if _, err := ah.secretClient.Create(secret); err != nil {
		return "", err
	}
	return token, nil
}

func (ah *appHandler) UpdateAllClusterMonitoringThanosEnabled(app *v3.App) error {
	clusters, err := ah.clusterLister.List("", labels.NewSelector())
	if err != nil {
		return err
	}
	for _, c := range clusters {
		if !c.Spec.EnableClusterMonitoring {
			continue
		}
		if err := UpdateClusterMonitoringAnswers(ah.clusterClient, c, app); err != nil {
			return err
		}
	}
	return nil
}

func UpdateClusterMonitoringAnswers(clusterClient mgmtv3.ClusterInterface, cluster *mgmtv3.Cluster, app *v3.App) error {
	answers := map[string]string{}
	if app == nil || app.DeletionTimestamp != nil {
		answers[ThanosEnabledAnswerKey] = ""
	} else {
		answers[ThanosEnabledAnswerKey] = "true"

		for k, v := range app.Spec.Answers {
			if strings.HasPrefix(k, objectConfigAnswerPrefix) {
				answers["prometheus."+k] = v
			}
		}
	}

	toUpdateAnswers, toUpdateValuesYaml, toUpdateExtraAnswers, version := monitoring.GetOverwroteAppAnswersAndVersion(cluster.Annotations)

	if !syncThanosAnswers(toUpdateAnswers, answers) {
		return nil
	}

	data := map[string]interface{}{}
	data["answers"] = toUpdateAnswers
	data["valuesYaml"] = toUpdateValuesYaml
	data["extraAnswers"] = toUpdateExtraAnswers
	if version != "" {
		data["version"] = version
	}

	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	toUpdateCluster := cluster.DeepCopy()
	toUpdateCluster.Annotations = monitoring.AppendAppOverwritingAnswers(toUpdateCluster.Annotations, string(b))
	if _, err := clusterClient.Update(toUpdateCluster); err != nil {
		return err
	}

	return nil
}

func syncThanosAnswers(current map[string]string, expected map[string]string) (changed bool) {
	for k := range current {
		if _, ok := expected[k]; !ok && strings.HasPrefix(k, prometheusThanosAnswerPrefix) {
			delete(current, k)
			changed = true
		}
	}
	for k, v := range expected {
		if current[k] != v {
			changed = true
			current[k] = v
		}
	}
	return
}

func isGlobalMonitoringApp(app *v3.App) bool {
	if app == nil || (app.Name != globalMonitoringAppName) || (app.Spec.TargetNamespace != globalDataNamespace) {
		return false
	}
	clusterID, _ := ref.Parse(app.Spec.ProjectName)
	globalMonitoringClusterID := settings.GlobalMonitoringClusterID.Get()
	if clusterID != globalMonitoringClusterID {
		return false
	}
	return true
}
