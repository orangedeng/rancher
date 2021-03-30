package nsserviceaccount

import (
	"context"
	"reflect"

	rv1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	macvlanAnnotationName = "macvlan.pandaria.io/plugin"
	macvlanCanalSAName    = "canal"
	macvlanFlannelSAName  = "flannel"
)

type pandaSvcAccountHandler struct {
	serviceAccounts rv1.ServiceAccountInterface
	clusterName     string
	clusterLister   v3.ClusterLister
	clusters        v3.ClusterInterface
}

func RegisterPanda(ctx context.Context, cluster *config.UserContext) {
	logrus.Debugf("Registering pandaSvcAccountHandler for updating cluster networking flags via canal/flannel service account of kube-system namespaces")
	psh := &pandaSvcAccountHandler{
		serviceAccounts: cluster.Core.ServiceAccounts(""),
		clusterName:     cluster.ClusterName,
		clusterLister:   cluster.Management.Management.Clusters("").Controller().Lister(),
		clusters:        cluster.Management.Management.Clusters(""),
	}
	cluster.Core.ServiceAccounts("kube-system").AddHandler(ctx, "pandaSvcAccountHandler", psh.Sync)
}

func (psh *pandaSvcAccountHandler) Sync(key string, sa *corev1.ServiceAccount) (runtime.Object, error) {
	if sa == nil || sa.DeletionTimestamp != nil {
		return nil, nil
	}
	logrus.Debugf("pandaSvcAccountHandler: Sync service account: key=%v", key)
	if err := psh.handleMacvlanSA(sa); err != nil {
		logrus.Errorf("pandaSvcAccountHandler: Sync error handling service account key=%v, err=%v", key, err)
	}

	return nil, nil
}

func (psh *pandaSvcAccountHandler) handleMacvlanSA(sa *corev1.ServiceAccount) error {
	if sa.Name == macvlanCanalSAName || sa.Name == macvlanFlannelSAName {
		if sa.Annotations != nil {
			if pluginName, ok := sa.Annotations[macvlanAnnotationName]; ok {
				return psh.updateClusterNetwork(pluginName)
			}
		}
	}

	return nil
}

func (psh *pandaSvcAccountHandler) updateClusterNetwork(plugin string) error {
	oldCluster, err := psh.clusterLister.Get("", psh.clusterName)
	if err != nil {
		return err
	}
	newCluster := oldCluster.DeepCopy()
	if newCluster.Annotations != nil {
		newCluster.Annotations[macvlanAnnotationName] = plugin
	}

	if !reflect.DeepEqual(oldCluster, newCluster) {
		if _, err := psh.clusters.Update(newCluster); err != nil {
			return errors.Wrapf(err, "[updateClusterNetwork] Failed to update cluster [%s]", newCluster.Name)
		}
	}

	return nil
}
