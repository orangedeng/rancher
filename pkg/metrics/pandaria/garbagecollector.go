package pandaria

import (
	"reflect"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
)

var (
	targetEtcdBackupMetricsForCluster = []interface{}{
		isBackupFailed,
	}
)

type pandariaMetricGarbageCollector struct {
	clusterLister v3.ClusterLister
}

func (gc *pandariaMetricGarbageCollector) pandariaMetricGarbageCollection() {
	logrus.Debugf("[pandaria-metrics-garbage-collector] Start")

	observedResourceNames := map[string]bool{}
	observedLabelsMap := map[string]map[interface{}][]map[string]string{}
	// get all clusters
	clusters, err := gc.clusterLister.List("", labels.Everything())
	if err != nil {
		logrus.Errorf("[pandaria-metrics-garbage-collector] failed to list clusters: %s", err)
		return
	}

	for _, cluster := range clusters {
		if !shouldBackup(cluster) {
			continue
		}
		if _, ok := observedResourceNames[cluster.Name]; !ok {
			observedResourceNames[cluster.Name] = true
		}
	}

	buildObservedLabelMaps(targetEtcdBackupMetricsForCluster, "cluster", observedLabelsMap)

	removedCount := removeMetricsForDeletedResource(observedLabelsMap, observedResourceNames)

	logrus.Debugf("[pandaria-metrics-garbage-collector] Finished - removed %d items", removedCount)
}

func shouldBackup(cluster *v3.Cluster) bool {
	// not an rke cluster, we do nothing
	if cluster.Spec.RancherKubernetesEngineConfig == nil {
		return false
	}
	if !isBackupSet(cluster.Spec.RancherKubernetesEngineConfig) {
		// no backend backup config
		return false
	}
	return true
}

func isBackupSet(rkeConfig *v3.RancherKubernetesEngineConfig) bool {
	return rkeConfig != nil && // rke cluster
		rkeConfig.Services.Etcd.BackupConfig != nil // backupConfig is set
}

func buildObservedLabelMaps(collectors []interface{}, targetLabel string, observedLabels map[string]map[interface{}][]map[string]string) int {
	// Example of the map structure of observedLabels:
	// {
	//   "c-fz6fq": {
	//      collectorA: [ {"cluster": "c-fz6fq", "owner": "x.x.x.x"}, ],
	//      collectorB: [ {"cluster": "c-fz6fq", "owner": "x.x.x.x"}, ],
	//   },
	//   "<targetLabel value>": {
	//   }
	// }
	count := 0
	for _, collector := range collectors {
		metricChan := make(chan prometheus.Metric)
		metricFrame := &dto.Metric{}
		go func() { collector.(prometheus.Collector).Collect(metricChan); close(metricChan) }()
		for metric := range metricChan {
			metric.Write(metricFrame)
			for _, label := range metricFrame.Label {
				if label.GetName() == targetLabel {
					// Initialize data structure
					if observedLabels[label.GetValue()] == nil {
						newCollectorMap := map[interface{}][]map[string]string{}
						newLabelList := []map[string]string{}
						observedLabels[label.GetValue()] = newCollectorMap
						observedLabels[label.GetValue()][collector] = newLabelList
					}
					metricLabelMap := metrcisLabelToMap(metricFrame)
					newLabels := appendIfLabelIsNotInList(metricLabelMap, observedLabels[label.GetValue()][collector])
					observedLabels[label.GetValue()][collector] = newLabels
					count++
				}
			}
		}
	}
	return count
}

func removeMetricsForDeletedResource(observedMetrics map[string]map[interface{}][]map[string]string, observedResources map[string]bool) int {
	removedCount := 0
	for m, collectors := range observedMetrics {
		// resource still exists
		if _, ok := observedResources[m]; ok {
			continue
		}
		logrus.Infof("[pandaria-metrics-garbage-collector] remove metrics related to %s", m)
		// resource doesn't exist, delete all related metrics
		for collector, labels := range collectors {
			for _, label := range labels {
				switch v := collector.(type) {
				case *prometheus.CounterVec:
					if v.Delete(label) {
						removedCount++
					} else {
						logrus.Errorf("[pandaria-metrics-garbage-collector] failed to delete %T metrics related to %s: %v", v, m, label)
					}
				case *prometheus.GaugeVec:
					if v.Delete(label) {
						removedCount++
					} else {
						logrus.Errorf("[pandaria-metrics-garbage-collector] failed to delete %T metrics related to %s: %v", v, m, label)
					}
				default:
					logrus.Errorf("[pandaria-metrics-garbage-collector] saw unknown Metric definition %T", v)
				}
			}

		}
	}
	return removedCount
}

func appendIfLabelIsNotInList(targetLabel map[string]string, labelList []map[string]string) []map[string]string {
	found := false
	for _, label := range labelList {
		if reflect.DeepEqual(targetLabel, label) {
			found = true
			break
		}
	}
	if !found {
		labelList = append(labelList, targetLabel)
	}
	return labelList
}

func metrcisLabelToMap(m *dto.Metric) map[string]string {
	result := map[string]string{}
	for _, label := range m.Label {
		result[label.GetName()] = label.GetValue()
	}
	return result
}
