package globalmonitoring

import (
	"fmt"
	"testing"
	"time"

	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/controllers/management/rbac"
	"github.com/rancher/rancher/pkg/monitoring"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/rancher/pkg/systemaccount"
	cfakes "github.com/rancher/types/apis/core/v1/fakes"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/apis/management.cattle.io/v3/fakes"
	pv3 "github.com/rancher/types/apis/project.cattle.io/v3"
	pfakes "github.com/rancher/types/apis/project.cattle.io/v3/fakes"
	"github.com/rancher/types/config"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
)

type userManagerMock struct {
}

func (um userManagerMock) SetPrincipalOnCurrentUser(apiContext *types.APIContext, principal v3.Principal) (*v3.User, error) {
	return nil, nil
}
func (um userManagerMock) GetUser(apiContext *types.APIContext) string {
	return ""
}
func (um userManagerMock) EnsureToken(tokenName, description, kind, userName string) (string, error) {
	return "", nil
}
func (um userManagerMock) EnsureClusterToken(clusterName, tokenName, description, kind, userName string) (string, error) {
	return "", nil
}
func (um userManagerMock) EnsureUser(principalName, displayName string) (*v3.User, error) {
	return &v3.User{
		ObjectMeta: metav1.ObjectMeta{
			Name: "u-abc",
		},
	}, nil
}
func (um userManagerMock) CheckAccess(accessMode string, allowedPrincipalIDs []string, userPrincipalID string, groups []v3.Principal) (bool, error) {
	return false, nil
}
func (um userManagerMock) SetPrincipalOnCurrentUserByUserID(userID string, principal v3.Principal) (*v3.User, error) {
	return nil, nil
}
func (um userManagerMock) CreateNewUserClusterRoleBinding(userName string, userUID apitypes.UID) error {
	return nil
}
func (um userManagerMock) GetUserByPrincipalID(principalName string) (*v3.User, error) {
	return nil, nil
}

func (um userManagerMock) GetKubeconfigToken(clusterName, tokenName, description, kind, userName string) (*v3.Token, error) {
	return nil, nil
}

type managementInterfaceMock struct {
	*v3.Client
}

func (mi managementInterfaceMock) ClusterRoleTemplateBindings(namespace string) v3.ClusterRoleTemplateBindingInterface {
	return &fakes.ClusterRoleTemplateBindingInterfaceMock{}
}

func (mi managementInterfaceMock) GlobalRoleBindings(namespace string) v3.GlobalRoleBindingInterface {
	return &fakes.GlobalRoleBindingInterfaceMock{
		GetFunc: func(name string, opts metav1.GetOptions) (*v3.GlobalRoleBinding, error) {
			return nil, nil
		},
		ControllerFunc: func() v3.GlobalRoleBindingController {
			return &fakes.GlobalRoleBindingControllerMock{
				ListerFunc: func() v3.GlobalRoleBindingLister {
					return &fakes.GlobalRoleBindingListerMock{
						ListFunc: func(namespace string, selector labels.Selector) ([]*v3.GlobalRoleBinding, error) {
							return nil, nil
						},
					}
				},
			}
		},
	}
}

func (mi managementInterfaceMock) ClusterRegistrationTokens(namespace string) v3.ClusterRegistrationTokenInterface {
	return &fakes.ClusterRegistrationTokenInterfaceMock{}
}

func (mi managementInterfaceMock) ProjectRoleTemplateBindings(namespace string) v3.ProjectRoleTemplateBindingInterface {
	return &fakes.ProjectRoleTemplateBindingInterfaceMock{
		ControllerFunc: func() v3.ProjectRoleTemplateBindingController {
			return &fakes.ProjectRoleTemplateBindingControllerMock{
				ListerFunc: func() v3.ProjectRoleTemplateBindingLister {
					return &fakes.ProjectRoleTemplateBindingListerMock{
						ListFunc: func(namespace string, selector labels.Selector) ([]*v3.ProjectRoleTemplateBinding, error) {
							return nil, nil
						},
					}
				},
			}
		},
	}
}

func (mi managementInterfaceMock) Tokens(namespace string) v3.TokenInterface {
	return &fakes.TokenInterfaceMock{}
}

func (mi managementInterfaceMock) Users(namespace string) v3.UserInterface {
	return &fakes.UserInterfaceMock{}
}

func newMockAppHandler(clusters map[string]*v3.Cluster, apps map[string]*pv3.App) appHandler {
	managementContextMock := &config.ManagementContext{
		UserManager: userManagerMock{},
		Management:  managementInterfaceMock{},
	}

	ah := appHandler{
		clusterClient: &fakes.ClusterInterfaceMock{
			UpdateFunc: func(in1 *v3.Cluster) (*v3.Cluster, error) {
				clusters[in1.Name] = in1
				return in1, nil
			},
			GetFunc: func(name string, opts metav1.GetOptions) (*v3.Cluster, error) {
				return clusters[name], nil
			},
		},
		clusterLister: &fakes.ClusterListerMock{
			ListFunc: func(namespace string, selector labels.Selector) ([]*v3.Cluster, error) {
				var items []*v3.Cluster
				for _, c := range clusters {
					items = append(items, c)
				}
				return items, nil
			},
		},
		appClient: &pfakes.AppInterfaceMock{
			UpdateFunc: func(in1 *pv3.App) (*pv3.App, error) {
				apps[in1.Name] = in1
				return in1, nil
			},
			GetFunc: func(name string, opts metav1.GetOptions) (*pv3.App, error) {
				return apps[name], nil
			},
		},
		secretLister: &cfakes.SecretListerMock{
			GetFunc: func(namespace string, name string) (*v1.Secret, error) {
				return nil, errors.NewNotFound(schema.GroupResource{}, "")
			},
		},
		secretClient: &cfakes.SecretInterfaceMock{
			CreateFunc: func(in1 *v1.Secret) (*v1.Secret, error) {
				return nil, nil
			},
		},
		systemAccountManager: systemaccount.NewManager(managementContextMock),
	}
	return ah
}

func TestGlobalMonitoringAppInstall(t *testing.T) {
	settings.ServerURL.Set("https://test.example.com")
	settings.GlobalMonitoringClusterID.Set("cluster1")
	assert := assert.New(t)
	clusters := map[string]*v3.Cluster{
		"cluster1": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "cluster1",
				Annotations: map[string]string{
					"field.cattle.io/overwriteAppAnswers": "{\"answers\":{\"operator-init.enabled\":\"true\"},\"version\":\"0.0.5\"}",
				},
			},
			Spec: v3.ClusterSpec{
				ClusterSpecBase: v3.ClusterSpecBase{
					EnableClusterMonitoring: true,
				},
			},
		},
		"cluster2": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "cluster2",
			},
		},
	}
	apps := map[string]*pv3.App{
		"cluster-monitoring": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cluster-monitoring",
				Namespace: "cluster1",
			},
		},
	}

	ah := newMockAppHandler(clusters, apps)

	app := pv3.App{
		ObjectMeta: metav1.ObjectMeta{
			Name: globalMonitoringAppName,
			Annotations: map[string]string{
				rbac.CreatorIDAnn: "u-test",
			},
		},
		Spec: pv3.AppSpec{
			ProjectName:     "cluster1:project1",
			TargetNamespace: globalDataNamespace,
		},
	}
	_, err := ah.sync(&app)
	assert.Empty(err)
	updatedApp, err := ah.appClient.Get(globalMonitoringAppName, metav1.GetOptions{})
	assert.Empty(err)
	assert.Equal("test.example.com", updatedApp.Spec.Answers[rancherHostKey])
	assert.Equal("cluster1", updatedApp.Spec.Answers[clusterIDAnswerKey])

	_, err = ah.sync(updatedApp)
	assert.Empty(err)
	updatedCluster, err := ah.clusterClient.Get("cluster1", metav1.GetOptions{})
	updatedClusterMonitoringAnswers, _, _, _ := monitoring.GetOverwroteAppAnswersAndVersion(updatedCluster.Annotations)
	assert.Equal("true", updatedClusterMonitoringAnswers[ThanosEnabledAnswerKey])
}

func TestGlobalMonitoringAppRemove(t *testing.T) {
	settings.ServerURL.Set("https://test.example.com")
	settings.GlobalMonitoringClusterID.Set("cluster1")
	assert := assert.New(t)
	clusters := map[string]*v3.Cluster{
		"cluster1": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "cluster1",
				Annotations: map[string]string{
					"field.cattle.io/overwriteAppAnswers": "{\"answers\":{\"prometheus.thanos.enabled\":\"true\"},\"version\":\"0.0.5\"}",
				},
			},
			Spec: v3.ClusterSpec{
				ClusterSpecBase: v3.ClusterSpecBase{
					EnableClusterMonitoring: true,
				},
			},
		},
		"cluster2": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "cluster2",
			},
		},
	}
	apps := map[string]*pv3.App{
		"cluster-monitoring": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cluster-monitoring",
				Namespace: "cluster1",
			},
		},
	}

	ah := newMockAppHandler(clusters, apps)

	app := pv3.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:              globalMonitoringAppName,
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: pv3.AppSpec{
			TargetNamespace: globalDataNamespace,
			ProjectName:     "cluster1:project1",
			Answers: map[string]string{
				rancherHostKey:     "test.example.com",
				clusterIDAnswerKey: "cluster1",
			},
		},
	}
	_, err := ah.sync(&app)
	assert.Empty(err)
	updatedCluster, err := ah.clusterClient.Get("cluster1", metav1.GetOptions{})
	updatedClusterMonitoringAnswers, _, _, _ := monitoring.GetOverwroteAppAnswersAndVersion(updatedCluster.Annotations)
	assert.Equal("", updatedClusterMonitoringAnswers[ThanosEnabledAnswerKey])
}

func TestSyncThanosAnswers(t *testing.T) {
	assert := assert.New(t)
	testCases := []struct {
		name                  string
		currentAnswers        map[string]string
		expectedThanosAnswers map[string]string
		syncedAnswers         map[string]string
		changed               bool
	}{
		{
			name: "unchanged",
			currentAnswers: map[string]string{
				"exporter-node.enabled":                            "true",
				"prometheus.thanos.enabled":                        "true",
				"prometheus.thanos.objectConfig.type":              "S3",
				"prometheus.thanos.objectConfig.config.access_key": "test_key",
				"prometheus.thanos.objectConfig.config.secret_key": "test_key",
			},
			expectedThanosAnswers: map[string]string{
				"prometheus.thanos.enabled":                        "true",
				"prometheus.thanos.objectConfig.type":              "S3",
				"prometheus.thanos.objectConfig.config.access_key": "test_key",
				"prometheus.thanos.objectConfig.config.secret_key": "test_key",
			},
			syncedAnswers: map[string]string{
				"exporter-node.enabled":                            "true",
				"prometheus.thanos.enabled":                        "true",
				"prometheus.thanos.objectConfig.type":              "S3",
				"prometheus.thanos.objectConfig.config.access_key": "test_key",
				"prometheus.thanos.objectConfig.config.secret_key": "test_key",
			},
			changed: false,
		}, {
			name: "to disable",
			currentAnswers: map[string]string{
				"exporter-node.enabled":                            "true",
				"prometheus.thanos.enabled":                        "true",
				"prometheus.thanos.objectConfig.type":              "S3",
				"prometheus.thanos.objectConfig.config.access_key": "test_key",
				"prometheus.thanos.objectConfig.config.secret_key": "test_key",
			},
			expectedThanosAnswers: map[string]string{
				"prometheus.thanos.enabled":                        "false",
				"prometheus.thanos.objectConfig.type":              "S3",
				"prometheus.thanos.objectConfig.config.access_key": "test_key",
				"prometheus.thanos.objectConfig.config.secret_key": "test_key",
			},
			syncedAnswers: map[string]string{
				"exporter-node.enabled":                            "true",
				"prometheus.thanos.enabled":                        "false",
				"prometheus.thanos.objectConfig.type":              "S3",
				"prometheus.thanos.objectConfig.config.access_key": "test_key",
				"prometheus.thanos.objectConfig.config.secret_key": "test_key",
			},
			changed: true,
		}, {
			name: "update storage type",
			currentAnswers: map[string]string{
				"exporter-node.enabled":                            "true",
				"prometheus.thanos.enabled":                        "true",
				"prometheus.thanos.objectConfig.type":              "S3",
				"prometheus.thanos.objectConfig.config.access_key": "test_key",
				"prometheus.thanos.objectConfig.config.secret_key": "test_key",
			},
			expectedThanosAnswers: map[string]string{
				"prometheus.thanos.enabled":                               "true",
				"prometheus.thanos.objectConfig.type":                     "ALIYUNOSS",
				"prometheus.thanos.objectConfig.config.access_key_id":     "test_key_id",
				"prometheus.thanos.objectConfig.config.access_key_secret": "test_key_secret",
			},
			syncedAnswers: map[string]string{
				"exporter-node.enabled":                                   "true",
				"prometheus.thanos.enabled":                               "true",
				"prometheus.thanos.objectConfig.type":                     "ALIYUNOSS",
				"prometheus.thanos.objectConfig.config.access_key_id":     "test_key_id",
				"prometheus.thanos.objectConfig.config.access_key_secret": "test_key_secret",
			},
			changed: true,
		}, {
			name: "remove answer",
			currentAnswers: map[string]string{
				"exporter-node.enabled":                          "true",
				"prometheus.thanos.enabled":                      "true",
				"prometheus.thanos.objectConfig.type":            "S3",
				"prometheus.thanos.objectConfig.config.endpoint": "https://some-endpoint",
			},
			expectedThanosAnswers: map[string]string{
				"prometheus.thanos.enabled":           "true",
				"prometheus.thanos.objectConfig.type": "S3",
			},
			syncedAnswers: map[string]string{
				"exporter-node.enabled":               "true",
				"prometheus.thanos.enabled":           "true",
				"prometheus.thanos.objectConfig.type": "S3",
			},
			changed: true,
		},
	}

	for _, testCase := range testCases {
		toSync := testCase.currentAnswers
		changed := syncThanosAnswers(toSync, testCase.expectedThanosAnswers)
		assert.Equal(testCase.changed, changed, fmt.Sprintf("Failed in test case %q", testCase.name))
		assert.Equal(testCase.syncedAnswers, toSync, fmt.Sprintf("Failed in test case %q", testCase.name))
	}
}
