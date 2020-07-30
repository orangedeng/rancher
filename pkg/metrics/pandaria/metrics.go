package pandaria

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rancher/rancher/pkg/ticker"
	"github.com/rancher/types/config"
)

var (
	pandariaPrometheusMetrics = false

	gcInterval = time.Duration(60 * time.Second)

	isBackupFailed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: "pandaria_etcd",
			Name:      "is_backup_failed",
			Help:      "Whether or not backup failed. 1 is failed, 0 is success, label by cluster id and name",
		},
		[]string{"cluster", "name"},
	)
)

func Register(ctx context.Context, scaledContext *config.ScaledContext) {
	pandariaPrometheusMetrics = true

	// regist etcd backup metrics
	prometheus.MustRegister(isBackupFailed)

	// metrics garbage collector
	gc := pandariaMetricGarbageCollector{
		clusterLister: scaledContext.Management.Clusters("").Controller().Lister(),
	}

	go func(ctx context.Context) {
		for range ticker.Context(ctx, gcInterval) {
			gc.pandariaMetricGarbageCollection()
		}
	}(ctx)
}

func SetEtcdBackupFailed(clusterID, clusterName string) {
	if pandariaPrometheusMetrics {
		isBackupFailed.With(prometheus.Labels{
			"cluster": clusterID,
			"name":    clusterName,
		}).Set(1)
	}
}

func SetEtcdBackupSuccess(clusterID, clusterName string) {
	if pandariaPrometheusMetrics {
		isBackupFailed.With(prometheus.Labels{
			"cluster": clusterID,
			"name":    clusterName,
		}).Set(0)
	}
}
