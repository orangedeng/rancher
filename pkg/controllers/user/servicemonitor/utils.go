package servicemonitor

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	util "github.com/rancher/rancher/pkg/controllers/user/workload"
	rmonitoringv1 "github.com/rancher/types/apis/monitoring.coreos.com/v1"
	v3 "github.com/rancher/types/apis/project.cattle.io/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	relabelType = "relabel"
)

func filterRancherLabels(l map[string]string) labels.Set {
	rtn := map[string]string{}
	for k, v := range l {
		if !strings.Contains(k, "cattle.io/") {
			rtn[k] = v
		}
	}
	return labels.Set(rtn)
}

func getWorkloadOwnerReference(w *util.Workload) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: w.APIVersion,
		Kind:       w.Kind,
		Name:       w.Name,
		UID:        w.UUID,
		Controller: &controller,
	}
}

func getMetricsFromWorkload(w *util.Workload) ([]v3.WorkloadMetric, error) {
	data, ok := w.TemplateSpec.Annotations[metricsAnnotation]
	if !ok {
		return nil, nil
	}
	var metrics []v3.WorkloadMetric
	if err := json.Unmarshal([]byte(data), &metrics); err != nil {
		return nil, err
	}
	return metrics, nil
}

func getServiceMonitorFromWorkload(w *util.Workload) (*monitoringv1.ServiceMonitor, error) {
	metrics, err := getMetricsFromWorkload(w)
	if err != nil {
		return nil, err
	}

	if len(metrics) == 0 {
		return nil, nil
	}

	workloadTargetAnnotation, err := util.IDAnnotationToString(w.Key)
	if err != nil {
		return nil, err
	}

	rtn := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{getWorkloadOwnerReference(w)},
			Namespace:       w.Namespace,
			Annotations: map[string]string{
				util.WorkloadAnnotation: workloadTargetAnnotation,
			},
			Name: w.Name,
		},
		Spec: monitoringv1.ServiceMonitorSpec{},
	}

	for _, metric := range metrics {
		portName := fmt.Sprintf("%s%d", "metrics", metric.Port)
		intstrPort := intstr.FromInt(int(metric.Port))
		endpoint := monitoringv1.Endpoint{
			Port:       portName,
			TargetPort: &intstrPort,
			Path:       metric.Path,
			Scheme:     metric.Schema,
			TLSConfig: &monitoringv1.TLSConfig{
				InsecureSkipVerify: true,
			},
		}
		if endpoint.Path == "" {
			endpoint.Path = "/metrics"
		}

		if len(metric.WorkloadMetricRelabelConfig) != 0 {
			var metricRelabel []*monitoringv1.RelabelConfig
			var relabel []*monitoringv1.RelabelConfig
			for _, v := range metric.WorkloadMetricRelabelConfig {
				if v.Regex != "" {
					rc := &monitoringv1.RelabelConfig{
						Regex:        v.Regex,
						Action:       v.Action,
						TargetLabel:  v.TargetLabel,
						SourceLabels: v.SourceLabels,
						Replacement:  v.Replacement,
					}

					if v.RelabelType == relabelType {
						relabel = append(relabel, rc)
					} else {
						metricRelabel = append(metricRelabel, rc)
					}
				}
			}

			if len(metricRelabel) != 0 {
				endpoint.MetricRelabelConfigs = metricRelabel
			}

			if len(relabel) != 0 {
				endpoint.RelabelConfigs = relabel
			}
		}

		rtn.Spec.Endpoints = append(rtn.Spec.Endpoints, endpoint)
	}
	return rtn, nil
}

func getWorkloadFromOwners(namespace string, owners []metav1.OwnerReference, lister rmonitoringv1.ServiceMonitorLister) (*monitoringv1.ServiceMonitor, error) {
	for _, owner := range owners {
		if !*owner.Controller || owner.Kind != "ServiceMonitor" {
			continue
		}
		return lister.Get(namespace, owner.Name)
	}
	return nil, nil
}

func areServiceMonitorEqual(a, b *monitoringv1.ServiceMonitor) bool {
	if len(a.Spec.Endpoints) != len(b.Spec.Endpoints) {
		return false
	}

	sort.Sort(EndpointSorter(a.Spec.Endpoints))
	sort.Sort(EndpointSorter(b.Spec.Endpoints))
	for i := 0; i < len(a.Spec.Endpoints); i++ {
		aEndpoint := a.Spec.Endpoints[i]
		bEndpoint := b.Spec.Endpoints[i]
		if aEndpoint.Port != bEndpoint.Port ||
			aEndpoint.Path != bEndpoint.Path ||
			aEndpoint.Scheme != bEndpoint.Scheme {
			return false
		}

		if len(aEndpoint.MetricRelabelConfigs) != len(bEndpoint.MetricRelabelConfigs) {
			return false
		}

		sort.Sort(RelabelConfigSorter(aEndpoint.MetricRelabelConfigs))
		sort.Sort(RelabelConfigSorter(bEndpoint.MetricRelabelConfigs))

		for j := 0; j < len(bEndpoint.MetricRelabelConfigs); j++ {
			aRelabel := aEndpoint.MetricRelabelConfigs[j]
			bRelabel := bEndpoint.MetricRelabelConfigs[j]

			sort.Strings(aRelabel.SourceLabels)
			sort.Strings(bRelabel.SourceLabels)

			if aRelabel.Regex != bRelabel.Regex || aRelabel.Action != bRelabel.Action || aRelabel.TargetLabel != bRelabel.TargetLabel ||
				aRelabel.Replacement != bRelabel.Replacement || !reflect.DeepEqual(aRelabel.SourceLabels, bRelabel.SourceLabels) {
				return false
			}
		}
	}
	for _, annotation := range []string{util.WorkloadAnnotation, servicesAnnotation} {
		adata := a.Annotations[annotation]
		bdata := b.Annotations[annotation]
		if adata == bdata && adata == "" {
			continue
		}
		var aarray, barray []string
		if err := json.Unmarshal([]byte(adata), &aarray); err != nil {
			return false
		}
		if err := json.Unmarshal([]byte(bdata), &barray); err != nil {
			return false
		}
		sort.Strings(aarray)
		sort.Strings(barray)
		if !reflect.DeepEqual(aarray, barray) {
			return false
		}
	}

	return true
}

type EndpointSorter []monitoringv1.Endpoint

func (e EndpointSorter) Len() int {
	return len(e)
}

func (e EndpointSorter) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

func (e EndpointSorter) Less(i, j int) bool {
	return getEndpointString(e[i]) < getEndpointString(e[j])
}

type RelabelConfigSorter []*monitoringv1.RelabelConfig

func (e RelabelConfigSorter) Len() int {
	return len(e)
}

func (e RelabelConfigSorter) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

func (e RelabelConfigSorter) Less(i, j int) bool {
	return getRelabelConfigString(e[i]) < getRelabelConfigString(e[j])
}

func getRelabelConfigString(e *monitoringv1.RelabelConfig) string {
	return fmt.Sprintf("%s%s", e.Regex, e.Action)
}

func getEndpointString(e monitoringv1.Endpoint) string {
	return fmt.Sprintf("%s%s%s", e.Scheme, e.Port, e.Path)
}

func getStringSliceFromAnnotation(obj metav1.ObjectMeta, key string) []string {
	annotaiton, ok := obj.Annotations[key]
	if !ok {
		return []string{}
	}
	var rtn []string
	json.Unmarshal([]byte(annotaiton), &rtn)
	return rtn
}

func GetServicePortsFromEndpoint(endpoints []monitoringv1.Endpoint) []corev1.ServicePort {
	PortMap := map[string]bool{}
	var rtn []corev1.ServicePort
	for _, endpoint := range endpoints {
		if _, ok := PortMap[endpoint.Port]; ok {
			continue
		}
		rtn = append(rtn, corev1.ServicePort{
			Name:       endpoint.Port,
			Port:       endpoint.TargetPort.IntVal,
			TargetPort: *endpoint.TargetPort,
		})
		PortMap[endpoint.Port] = true
	}
	return rtn
}
