package rbac

import (
	"github.com/rancher/rancher/pkg/rbac"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	v12 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	readonlyClusterRole = "read-only-admin"
)

// isReadonlyRole detects whether a GlobalRole is read-only role for
// all clusters or not(add for PANDARIA).
func (c *grbHandler) isReadonlyRole(rtName string) (bool, error) {
	gr, err := c.grLister.Get("", rtName)
	if err != nil {
		return false, err
	}

	// global role is builtin admin role
	if gr.Builtin && gr.Name == "read-only-pandaria" {
		return true, nil
	}
	return false, nil
}

func (c *grbHandler) syncReadonlyRole(obj *v3.GlobalRoleBinding) (runtime.Object, error) {
	// Do not sync read-only role to the local cluster
	if c.clusterName != "local" {
		logrus.Debugf("%v is a read only role", obj.GlobalRoleName)
		err := c.ensureReadonlyRole()
		if err != nil {
			return obj, err
		}

		bindingName := rbac.GrbCRBReadonlyName(obj)
		b, err := c.crbLister.Get("", bindingName)
		if err != nil && !apierrors.IsNotFound(err) {
			return obj, err
		}

		if b != nil {
			// binding exists, nothing to do
			return obj, nil
		}
		_, err = c.clusterRoleBindings.Create(&v12.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: bindingName,
			},
			Subjects: []v12.Subject{rbac.GetGRBSubject(obj)},
			RoleRef: v12.RoleRef{
				Name: readonlyClusterRole,
				Kind: "ClusterRole",
			},
		})
		if err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return obj, err
			}
		}
	}

	return obj, nil
}

func (c *grbHandler) ensureReadonlyRole() error {
	_, err := c.crLister.Get("", readonlyClusterRole)
	if err != nil && apierrors.IsNotFound(err) {
		logrus.Debugf("Creating clusterRole %v for read-only-pandaria GlobalRole", readonlyClusterRole)
		// create default read-only-admin cluster role
		rules := []v12.PolicyRule{
			{
				Verbs:     []string{"list", "get", "watch"},
				APIGroups: []string{"*"},
				Resources: []string{"*"},
			},
			{
				NonResourceURLs: []string{"*"},
				Verbs:           []string{"list", "get", "watch"},
			},
		}
		readonlyRole := &v12.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: readonlyClusterRole,
			},
			Rules: rules,
		}
		_, err = c.crClient.Create(readonlyRole)
		return err
	}
	return err
}
