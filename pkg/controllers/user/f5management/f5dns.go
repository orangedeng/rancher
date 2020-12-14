package f5management

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"

	f5cisv1 "github.com/F5Networks/k8s-bigip-ctlr/config/apis/cis/v1"
	v1 "github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
)

const (
	f5GlobalDNSAnnotation = "f5.cattle.io/globalDNSHostName"
)

type F5DNSHandler struct {
	serviceLister v1.ServiceLister
	services      v1.ServiceInterface
	nodeLister    v1.NodeLister
}

func Register(ctx context.Context, workload *config.UserContext) {
	h := &F5DNSHandler{
		services:      workload.Core.Services(""),
		serviceLister: workload.Core.Services("").Controller().Lister(),
		nodeLister:    workload.Core.Nodes("").Controller().Lister(),
	}
	workload.F5CIS.VirtualServers("").AddHandler(ctx, "F5DNSHandler", h.sync)
}

func (c *F5DNSHandler) sync(key string, obj *f5cisv1.VirtualServer) (runtime.Object, error) {
	globalDNSHostName, ok := obj.Annotations[f5GlobalDNSAnnotation]
	if !ok {
		logrus.Infof("VirtualServer does not have globaldns annotation")
		return nil, nil
	}

	logrus.Infof("globaldns host name: %v", globalDNSHostName)

	return nil, nil
}
