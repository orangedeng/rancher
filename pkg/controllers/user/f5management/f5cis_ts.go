package f5management

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	f5cisv1 "github.com/F5Networks/k8s-bigip-ctlr/config/apis/cis/v1"
	util "github.com/rancher/rancher/pkg/controllers/user/workload"
	v1 "github.com/rancher/types/apis/core/v1"
	"github.com/sirupsen/logrus"
)

func (c *Controller) syncTansportServer(key string, obj *f5cisv1.TransportServer) (runtime.Object, error) {

	if obj == nil || obj.DeletionTimestamp != nil {
		return nil, nil
	}

	targets := obj.Annotations[f5TargetAnnotation]

	if len(targets) == 0 {
		return nil, nil
	}

	tar := map[string]string{}
	err := json.Unmarshal(([]byte)(targets), &tar)
	if err != nil {
		logrus.Errorf("Unmarshal F5 targes string error: %v", err)
		return nil, nil
	}

	expectedServices, err := generateExpectedF5Services(tar)
	if err != nil {
		return nil, err
	}

	existingServices, err := getF5TransportServerRelatedServices(c.serviceLister, obj, expectedServices)
	if err != nil {
		return nil, err
	}

	for _, service := range existingServices {
		shouldDelete, toUpdate := updateOrDeleteForTS(obj, service, expectedServices)
		if shouldDelete {
			if err := c.services.DeleteNamespaced(obj.Namespace, service.Name, &metav1.DeleteOptions{}); err != nil {
				return nil, err
			}
			continue
		}
		if toUpdate != nil {
			if _, err := c.services.Update(toUpdate); err != nil {
				return nil, err
			}
		}
		// don't create the services which already exist
		delete(expectedServices, service.Name)
	}

	for _, f5Service := range expectedServices {
		toCreate := f5Service.generateNewServiceForTS(obj, corev1.ServiceTypeClusterIP)

		logrus.Infof("Creating %s service %s for f5 virtualserver %s, port %d", f5Service.serviceName, toCreate.Spec.Type, key, f5Service.servicePort)
		if _, err := c.services.Create(toCreate); err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func updateOrDeleteForTS(obj *f5cisv1.TransportServer, service *corev1.Service, expectedServices map[string]f5Service) (bool, *corev1.Service) {
	shouldDelete := false
	var toUpdate *corev1.Service
	s, ok := expectedServices[service.Name]
	if ok {
		if service.Annotations == nil {
			service.Annotations = map[string]string{}
		}

		if service.Annotations[util.WorkloadAnnotation] != s.workloadIDs && s.workloadIDs != "" {
			toUpdate = service.DeepCopy()
			toUpdate.Annotations[util.WorkloadAnnotation] = s.workloadIDs
		}

	} else {
		if IsServiceOwnedByF5TransportServer(obj, service) {
			shouldDelete = true
		}
	}
	return shouldDelete, toUpdate
}

func IsServiceOwnedByF5TransportServer(vs *f5cisv1.TransportServer, service *corev1.Service) bool {
	for i, owners := 0, service.GetOwnerReferences(); owners != nil && i < len(owners); i++ {
		if owners[i].UID == vs.UID && owners[i].Kind == vs.Kind {
			return true
		}
	}
	return false
}

func (i *f5Service) generateNewServiceForTS(obj *f5cisv1.TransportServer, serviceType corev1.ServiceType) *corev1.Service {
	controller := true
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: i.serviceName,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       obj.Name,
					APIVersion: "cis.f5.com/v1",
					UID:        obj.UID,
					Kind:       "TransportServer",
					Controller: &controller,
				},
			},
			Namespace: obj.Namespace,
			Annotations: map[string]string{
				util.WorkloadAnnotation: i.workloadIDs,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: serviceType,
			Ports: []corev1.ServicePort{
				{
					Port:       i.servicePort,
					TargetPort: intstr.FromInt(int(i.servicePort)),
					Protocol:   "TCP",
				},
			},
		},
	}
}

func getF5TransportServerRelatedServices(serviceLister v1.ServiceLister, obj *f5cisv1.TransportServer, expectedServices map[string]f5Service) (map[string]*corev1.Service, error) {
	rtn := map[string]*corev1.Service{}
	services, err := serviceLister.List(obj.Namespace, labels.NewSelector())
	if err != nil {
		return nil, err
	}
	for _, service := range services {
		if _, ok := expectedServices[service.Name]; ok {
			rtn[service.Name] = service
			continue
		}
		if IsServiceOwnedByF5TransportServer(obj, service) {
			rtn[service.Name] = service
		}
	}
	return rtn, nil
}
