package globalmonitoring

import (
	"testing"

	"github.com/rancher/rancher/pkg/monitoring"
	"github.com/rancher/rancher/pkg/settings"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/apis/management.cattle.io/v3/fakes"
	pv3 "github.com/rancher/types/apis/project.cattle.io/v3"
	pfakes "github.com/rancher/types/apis/project.cattle.io/v3/fakes"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func newMockClusterHandler(clusters map[string]*v3.Cluster, apps map[string]*pv3.App) clusterHandler {
	ch := clusterHandler{
		clusterClient: &fakes.ClusterInterfaceMock{
			UpdateFunc: func(in1 *v3.Cluster) (*v3.Cluster, error) {
				clusters[in1.Name] = in1
				return in1, nil
			},
			GetFunc: func(name string, opts metav1.GetOptions) (*v3.Cluster, error) {
				return clusters[name], nil
			},
		},
		appLister: &pfakes.AppListerMock{
			ListFunc: func(namespace string, selector labels.Selector) ([]*pv3.App, error) {
				var items []*pv3.App
				for _, a := range apps {
					items = append(items, a)
				}
				return items, nil
			},
			GetFunc: func(namespace string, name string) (*pv3.App, error) {
				return apps[name], nil
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
		projectLister: &fakes.ProjectListerMock{
			ListFunc: func(namespace string, selector labels.Selector) ([]*v3.Project, error) {
				projects := []*v3.Project{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mock-project",
						},
					},
				}
				return projects, nil
			},
		},
	}
	return ch
}

func TestEnableClusterMonitoring(t *testing.T) {
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
			Status: v3.ClusterStatus{
				Conditions: []v3.ClusterCondition{
					{
						Type:   "MonitoringEnabled",
						Status: v1.ConditionTrue,
					},
				},
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

		"global-monitoring": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "global-monitoring",
			},
			Spec: pv3.AppSpec{
				Answers: map[string]string{
					rancherHostKey:     "test.example.com",
					clusterIDAnswerKey: "",
				},
			},
		},
	}

	ch := newMockClusterHandler(clusters, apps)

	cluster := v3.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster1",
		},
		Spec: v3.ClusterSpec{
			ClusterSpecBase: v3.ClusterSpecBase{
				EnableClusterMonitoring: true,
			},
		},
		Status: v3.ClusterStatus{
			Conditions: []v3.ClusterCondition{
				{
					Type:   "MonitoringEnabled",
					Status: v1.ConditionTrue,
				},
			},
		},
	}
	_, err := ch.sync("cluster1", &cluster)
	assert.Empty(err)
	updatedApp, err := ch.appClient.Get(globalMonitoringAppName, metav1.GetOptions{})
	assert.Empty(err)
	assert.Equal("cluster1", updatedApp.Spec.Answers[clusterIDAnswerKey])

	updatedCluster, err := ch.clusterClient.Get("cluster1", metav1.GetOptions{})
	updatedClusterMonitoringAnswers, _ := monitoring.GetOverwroteAppAnswersAndVersion(updatedCluster.Annotations)
	assert.Equal("true", updatedClusterMonitoringAnswers[ThanosEnabledAnswerKey])
}

func TestDisableClusterMonitoring(t *testing.T) {
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
			Status: v3.ClusterStatus{
				Conditions: []v3.ClusterCondition{
					{
						Type:   "MonitoringEnabled",
						Status: v1.ConditionTrue,
					},
				},
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

		"global-monitoring": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "global-monitoring",
			},
			Spec: pv3.AppSpec{
				Answers: map[string]string{
					rancherHostKey:     "test.example.com",
					clusterIDAnswerKey: "cluster1",
				},
			},
		},
	}

	ch := newMockClusterHandler(clusters, apps)

	cluster := v3.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster1",
		},
		Spec: v3.ClusterSpec{
			ClusterSpecBase: v3.ClusterSpecBase{
				EnableClusterMonitoring: false,
			},
		},
		Status: v3.ClusterStatus{
			Conditions: []v3.ClusterCondition{
				{
					Type:   "MonitoringEnabled",
					Status: v1.ConditionTrue,
				},
			},
		},
	}
	_, err := ch.sync("cluster1", &cluster)
	assert.Empty(err)
	updatedApp, err := ch.appClient.Get(globalMonitoringAppName, metav1.GetOptions{})
	assert.Empty(err)
	assert.Equal("", updatedApp.Spec.Answers[clusterIDAnswerKey])
}
