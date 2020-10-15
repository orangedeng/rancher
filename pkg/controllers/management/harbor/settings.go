package harbor

import (
	"context"

	"github.com/rancher/rancher/pkg/settings"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	harborRegistryAuthLabel          = "rancher.cn/registry-harbor-auth"
	harborRegistryAdminLabel         = "rancher.cn/registry-harbor-admin-auth"
	harborUserAnnotationAuth         = "authz.management.cattle.io.cn/harborauth"
	harborUserAnnotationEmail        = "authz.management.cattle.io.cn/harboremail"
	harborUserAnnotationSyncComplete = "management.harbor.pandaria.io/synccomplete"
	harborUserAuthSecret             = "management.harbor.pandaria.io/harbor-secrets"
	harborUserSecretKey              = "harborAuth"
	harborAdminConfig                = "harbor-config"
)

type Controller struct {
	settings v3.SettingInterface
	users    v3.UserInterface
}

func Register(ctx context.Context, management *config.ManagementContext) {
	c := &Controller{
		settings: management.Management.Settings(""),
		users:    management.Management.Users(""),
	}

	management.Management.Settings("").AddHandler(ctx, "harborClearUserAnnotationsController", c.clearUserAnnotations)
}

func (c *Controller) clearUserAnnotations(key string, obj *v3.Setting) (runtime.Object, error) {
	if obj == nil || obj.DeletionTimestamp != nil {
		return nil, nil
	}

	if settings.HarborServerURL.Name == obj.Name {
		harborServerStr := obj.Value
		if harborServerStr == "" {
			logrus.Info("harborClearUserAnnotationsController: clean harbor Annotations")

			users, _ := c.users.List(metav1.ListOptions{})
			for _, user := range users.Items {
				if user.Annotations != nil && (user.Annotations[harborUserAnnotationAuth] != "" || user.Annotations[harborUserAnnotationEmail] != "" || user.Annotations[harborUserAnnotationSyncComplete] != "") {
					newUser := user.DeepCopy()
					logrus.Debugf("harborClearUserAnnotationsController: clear harbor auth for user: %s", newUser.Name)
					annotations := newUser.Annotations

					delete(annotations, harborUserAnnotationAuth)
					delete(annotations, harborUserAnnotationEmail)
					delete(annotations, harborUserAnnotationSyncComplete)
					newUser, err := c.users.Update(newUser)
					if err != nil && !apierrors.IsConflict(err) {
						logrus.Errorf("harborClearUserAnnotationsController: clear user annotation error: %v", err)
					}
				}
			}
		}
	}

	return nil, nil
}
