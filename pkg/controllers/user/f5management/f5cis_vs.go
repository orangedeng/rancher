package f5management

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	f5cisv1 "github.com/F5Networks/k8s-bigip-ctlr/config/apis/cis/v1"
	util "github.com/rancher/rancher/pkg/controllers/user/workload"
	v1 "github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	f5TargetAnnotation       = "f5.pandaria.io/targets"
	poolMemberTypeAnnotation = "f5.cattle.io/poolmembertype"
)

type Controller struct {
	serviceLister v1.ServiceLister
	services      v1.ServiceInterface
	nodeLister    v1.NodeLister
}

func RegisterAgent(ctx context.Context, workload *config.UserOnlyContext) {
	c := &Controller{
		services:      workload.Core.Services(""),
		serviceLister: workload.Core.Services("").Controller().Lister(),
		nodeLister:    workload.Core.Nodes("").Controller().Lister(),
	}
	workload.F5CIS.VirtualServers("").AddHandler(ctx, "F5CISVirtualServerController", c.syncVirtualServer)
	workload.F5CIS.TransportServers("").AddHandler(ctx, "F5CISTransportServerController", c.syncTansportServer)
}

func (c *Controller) syncVirtualServer(key string, obj *f5cisv1.VirtualServer) (runtime.Object, error) {

	if obj == nil || obj.DeletionTimestamp != nil {
		return nil, nil
	}

	poolMemberType := obj.Annotations[poolMemberTypeAnnotation]
	var serviceType corev1.ServiceType
	switch poolMemberType {
	case "cluster":
		serviceType = corev1.ServiceTypeClusterIP
	case "nodeport":
		serviceType = corev1.ServiceTypeNodePort
	default:
		serviceType = corev1.ServiceTypeClusterIP
	}

	targets := obj.Annotations[f5TargetAnnotation]

	if len(targets) == 0 {
		return nil, nil
	}

	tar := map[string]string{}
	err := json.Unmarshal(([]byte)(targets), &tar)
	if err != nil {
		logrus.Errorf("Unmarshal f5 targes string error: %v", err)
		return nil, nil
	}

	expectedServices, err := generateExpectedF5Services(tar)
	if err != nil {
		return nil, err
	}

	existingServices, err := getF5VirtualServerRelatedServices(c.serviceLister, obj, expectedServices)
	if err != nil {
		return nil, err
	}

	for _, service := range existingServices {
		shouldDelete, toUpdate := updateOrDelete(obj, service, expectedServices)
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
		toCreate := f5Service.generateNewService(obj, serviceType)

		logrus.Infof("Creating %s service %s for f5 virtualserver %s, port %d", f5Service.serviceName, toCreate.Spec.Type, key, f5Service.servicePort)
		if _, err := c.services.Create(toCreate); err != nil {
			return nil, err
		}
	}

	return nil, nil
}

type f5Service struct {
	serviceName string
	servicePort int32
	workloadIDs string
}

func (i *f5Service) generateNewService(obj *f5cisv1.VirtualServer, serviceType corev1.ServiceType) *corev1.Service {
	controller := true
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: i.serviceName,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       obj.Name,
					APIVersion: "cis.f5.com/v1",
					UID:        obj.UID,
					Kind:       "VirtualServer",
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

func generateExpectedF5Services(targets map[string]string) (map[string]f5Service, error) {
	rtn := map[string]f5Service{}
	for k, v := range targets {
		svcs := strings.Split(k, "/")
		if len(svcs) != 2 {
			return nil, nil
		}
		svcName := svcs[0]
		port := svcs[1]
		f5svc, err := generateF5Service(svcName, port, v)
		if err != nil {
			return nil, err
		}
		rtn[svcName] = f5svc
	}
	return rtn, nil
}

func generateF5Service(name string, port string, workloadID string) (f5Service, error) {
	p, err := strconv.Atoi(port)
	if err != nil {
		logrus.Errorf("Convert port string failed: %v", err)
		return f5Service{}, err
	}
	wids := []string{workloadID}
	b, err := json.Marshal(wids)
	if err != nil {
		return f5Service{}, err
	}
	rtn := f5Service{
		serviceName: name,
		servicePort: int32(p),
		workloadIDs: string(b),
	}
	return rtn, nil
}

func getF5VirtualServerRelatedServices(serviceLister v1.ServiceLister, obj *f5cisv1.VirtualServer, expectedServices map[string]f5Service) (map[string]*corev1.Service, error) {
	rtn := map[string]*corev1.Service{}
	services, err := serviceLister.List(obj.Namespace, labels.NewSelector())
	if err != nil {
		return nil, err
	}
	for _, service := range services {
		//mark the service which related to ingress
		if _, ok := expectedServices[service.Name]; ok {
			rtn[service.Name] = service
			continue
		}
		//mark the service which own by ingress but not related to ingress
		if IsServiceOwnedByF5VirtualServer(obj, service) {
			rtn[service.Name] = service
		}
	}
	return rtn, nil
}

func IsServiceOwnedByF5VirtualServer(vs *f5cisv1.VirtualServer, service *corev1.Service) bool {
	for i, owners := 0, service.GetOwnerReferences(); owners != nil && i < len(owners); i++ {
		if owners[i].UID == vs.UID && owners[i].Kind == vs.Kind {
			return true
		}
	}
	return false
}

func updateOrDelete(obj *f5cisv1.VirtualServer, service *corev1.Service, expectedServices map[string]f5Service) (bool, *corev1.Service) {
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
		if IsServiceOwnedByF5VirtualServer(obj, service) {
			shouldDelete = true
		}
	}
	return shouldDelete, toUpdate
}
