package f5cis

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/catalog/manager"
	cutils "github.com/rancher/rancher/pkg/catalog/utils"
	ns "github.com/rancher/rancher/pkg/namespace"
	"github.com/rancher/rancher/pkg/ref"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AppLevel string

var (
	APIVersion = types.APIVersion{
		Version: "v1",
		Group:   "cis.f5.com",
		Path:    "/v3/project",
	}
)

const (
	SystemLevel  AppLevel = "system"
	ClusterLevel AppLevel = "cluster"
	ProjectLevel AppLevel = "project"
)

const (
	cattleNamespaceName      = "cattle-f5"
	rancherF5CISTemplateName = "system-library-rancher-f5cis"
	f5CISTemplateName        = "rancher-f5cis"
)

const (
	cattleOverwriteF5CISAppAnswersAnnotationKey = "field.cattle.io/overwriteF5CISAppAnswers"

	//CattleMonitoringLabelKey The label info of Namespace
	cattleF5CISLabelKey = "f5cis.cattle.io"

	// The label info of App, RoleBinding
	appNameLabelKey            = cattleF5CISLabelKey + "/appName"
	appTargetNamespaceLabelKey = cattleF5CISLabelKey + "/appTargetNamespace"
	appProjectIDLabelKey       = cattleF5CISLabelKey + "/projectID"
	appClusterIDLabelKey       = cattleF5CISLabelKey + "/clusterID"

	// The names of App
	clusterLevelAppName = "cluster-f5cis"
)

func ClusterF5CISInfo() (appName, appTargetNamespace string) {
	return clusterLevelAppName, cattleNamespaceName
}

func OwnedAppListOptions(clusterID, appName, appTargetNamespace string) metav1.ListOptions {
	return metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s, %s=%s, %s=%s", appClusterIDLabelKey, clusterID, appNameLabelKey, appName, appTargetNamespaceLabelKey, appTargetNamespace),
	}
}

func OwnedLabels(appName, appTargetNamespace, appProjectName string) map[string]string {
	clusterID, projectID := ref.Parse(appProjectName)

	return map[string]string{
		appNameLabelKey:            appName,
		appTargetNamespaceLabelKey: appTargetNamespace,
		appProjectIDLabelKey:       projectID,
		appClusterIDLabelKey:       clusterID,
	}
}

func GetF5CISCatalogID(version string, catalogTemplateLister mgmtv3.CatalogTemplateLister, catalogManager manager.CatalogManager, clusterName string) (string, error) {
	if version == "" {
		template, err := catalogTemplateLister.Get(ns.GlobalNamespace, rancherF5CISTemplateName)
		if err != nil {
			return "", err
		}

		templateVersion, err := catalogManager.LatestAvailableTemplateVersion(template, clusterName)
		if err != nil {
			return "", err
		}
		version = templateVersion.Version
	}
	return fmt.Sprintf(cutils.CatalogExternalIDFormat, cutils.SystemLibraryName, f5CISTemplateName, version), nil
}

func GetF5CISAppAnswersAndVersion(annotations map[string]string) (map[string]string, string, map[string]string, string) {
	overwritingAppAnswers := annotations[cattleOverwriteF5CISAppAnswersAnnotationKey]
	if len(overwritingAppAnswers) != 0 {
		var appOverwriteInput mgmtv3.F5CISInput
		err := json.Unmarshal([]byte(overwritingAppAnswers), &appOverwriteInput)
		if err == nil {
			if appOverwriteInput.Answers == nil {
				appOverwriteInput.Answers = make(map[string]string)
			}
			if appOverwriteInput.ExtraAnswers == nil {
				appOverwriteInput.ExtraAnswers = make(map[string]string)
			}
			return appOverwriteInput.Answers, appOverwriteInput.ValuesYaml, appOverwriteInput.ExtraAnswers, appOverwriteInput.Version
		}
		logrus.Errorf("failed to parse app overwrite input from %q, %v", overwritingAppAnswers, err)
	}

	return map[string]string{}, "", map[string]string{}, ""
}

func AppendAppOverwritingAnswers(toAnnotations map[string]string, appOverwriteAnswers string) map[string]string {
	if len(strings.TrimSpace(appOverwriteAnswers)) != 0 {
		if toAnnotations == nil {
			toAnnotations = make(map[string]string, 2)
		}

		toAnnotations[cattleOverwriteF5CISAppAnswersAnnotationKey] = appOverwriteAnswers
	}

	return toAnnotations
}

func GetF5CISAppAnswersAndCatalogID(annotations map[string]string,
	catalogTemplateLister mgmtv3.CatalogTemplateLister,
	catalogManager manager.CatalogManager,
	clusterName string) (map[string]string, string, string, error) {
	overwriteAnswers, valuesYaml, _, version := GetF5CISAppAnswersAndVersion(annotations)

	catalogID, err := GetF5CISCatalogID(version, catalogTemplateLister, catalogManager, clusterName)
	return overwriteAnswers, valuesYaml, catalogID, err
}
