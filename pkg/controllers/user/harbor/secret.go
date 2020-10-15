package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/rancher/rancher/pkg/namespace"
	"github.com/rancher/rancher/pkg/settings"
	v1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/credentialprovider"
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
	settings          v3.SettingInterface
	users             v3.UserInterface
	managementSecrets v1.SecretInterface
	secrets           v1.SecretInterface
}

func Register(ctx context.Context, cluster *config.UserContext) {
	s := &Controller{
		settings:          cluster.Management.Management.Settings(""),
		users:             cluster.Management.Management.Users(""),
		managementSecrets: cluster.Management.Core.Secrets(""),
		secrets:           cluster.Core.Secrets(""),
	}

	cluster.Management.Management.Settings("").AddHandler(ctx, "harborClearUserSecretController", s.clearUserHarborSecret)
	cluster.Management.Core.Secrets("").AddHandler(ctx, "harborAuthSecretController", s.syncAuth)
}

func (c *Controller) syncAuth(key string, obj *corev1.Secret) (runtime.Object, error) {
	if obj == nil || obj.DeletionTimestamp != nil {
		return nil, nil
	}
	if obj.Labels != nil && obj.Labels[harborUserAuthSecret] == "true" {
		// if not clear harbor configuration, need to sync docker registry auth
		if settings.HarborServerURL.Get() != "" && obj.Data != nil {
			return c.syncSecret(string(obj.Data[harborUserSecretKey]))
		}
	}

	if obj.Namespace == namespace.PandariaGlobalNamespace && obj.Name == harborAdminConfig {
		if settings.HarborServerURL.Get() != "" && obj.Data != nil {
			return c.syncAdminAuth()
		}
	}
	return nil, nil
}

func (c *Controller) syncSecret(auth string) (runtime.Object, error) {
	// get harbor server
	harborServerStr := settings.HarborServerURL.Get()
	harborServerArray := strings.Split(harborServerStr, "://")
	harborServer := ""
	if len(harborServerArray) != 2 {
		harborServer = harborServerArray[0]
	} else {
		harborServer = harborServerArray[1]
	}

	harborAuth := strings.Split(auth, ":")
	if len(harborAuth) != 2 {
		return nil, fmt.Errorf("invalid user auth of harbor: %v", auth)
	}

	// get all harbor registry secrets
	secretList, err := c.managementSecrets.List(metav1.ListOptions{
		FieldSelector: "type=kubernetes.io/dockerconfigjson",
		LabelSelector: fmt.Sprintf("%s=true", harborRegistryAuthLabel),
	})
	if err != nil {
		return nil, err
	}
	if len(secretList.Items) > 0 {
		compareDockerCredential(secretList.Items, c.managementSecrets, harborServer, harborAuth[0], harborAuth[1], false)
	}

	// get all namespace secrets
	nsSecretList, err := c.secrets.List(metav1.ListOptions{
		FieldSelector: "type=kubernetes.io/dockerconfigjson",
		LabelSelector: fmt.Sprintf("%s=true", harborRegistryAuthLabel),
	})
	if err != nil {
		return nil, err
	}
	if len(nsSecretList.Items) > 0 {
		compareDockerCredential(nsSecretList.Items, c.secrets, harborServer, harborAuth[0], harborAuth[1], false)
	}

	return nil, nil
}

func (c *Controller) clearUserHarborSecret(key string, obj *v3.Setting) (runtime.Object, error) {
	if obj == nil || obj.DeletionTimestamp != nil {
		return nil, nil
	}

	if settings.HarborServerURL.Name == obj.Name {
		harborServerStr := obj.Value
		if harborServerStr == "" {
			logrus.Info("harborClearUserSecretController: clean harbor secret")
			// remove global admin auth
			err := c.managementSecrets.DeleteNamespaced(namespace.PandariaGlobalNamespace, harborAdminConfig, &metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				logrus.Errorf("harborClearUserAnnotationsController: remove harbor global admin auth error: %v", err)
			}

			users, _ := c.users.List(metav1.ListOptions{})
			for _, user := range users.Items {
				// remove auth secrets
				err = c.managementSecrets.DeleteNamespaced(user.Name, fmt.Sprintf("%s-harbor", user.Name), &metav1.DeleteOptions{})
				if err != nil && !apierrors.IsNotFound(err) {
					logrus.Errorf("harborClearUserAnnotationsController: remove %s secret error: %v", fmt.Sprintf("%s-harbor", user.Name), err)
				}
			}
		}
	}

	return nil, nil
}

func (c *Controller) syncAdminAuth() (runtime.Object, error) {
	// get latest admin auth
	adminSecret, err := c.managementSecrets.GetNamespaced(namespace.PandariaGlobalNamespace, harborAdminConfig, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	username := string(adminSecret.Data[corev1.BasicAuthUsernameKey])
	password := string(adminSecret.Data[corev1.BasicAuthPasswordKey])

	// get harbor server
	harborServerStr := settings.HarborServerURL.Get()
	harborServerArray := strings.Split(harborServerStr, "://")
	harborServer := ""
	if len(harborServerArray) != 2 {
		harborServer = harborServerArray[0]
	} else {
		harborServer = harborServerArray[1]
	}

	secretList, err := c.managementSecrets.List(metav1.ListOptions{
		FieldSelector: "type=kubernetes.io/dockerconfigjson",
		LabelSelector: fmt.Sprintf("%s=true,%s=true", harborRegistryAuthLabel, harborRegistryAdminLabel),
	})
	if err != nil {
		return nil, err
	}

	if len(secretList.Items) > 0 {
		compareDockerCredential(secretList.Items, c.managementSecrets, harborServer, username, password, true)
	}

	// get all namespace secrets
	nsSecretList, err := c.secrets.List(metav1.ListOptions{
		FieldSelector: "type=kubernetes.io/dockerconfigjson",
		LabelSelector: fmt.Sprintf("%s=true,%s=true", harborRegistryAuthLabel, harborRegistryAdminLabel),
	})
	if err != nil {
		return nil, err
	}
	if len(nsSecretList.Items) > 0 {
		compareDockerCredential(nsSecretList.Items, c.secrets, harborServer, username, password, true)
	}

	return nil, nil
}

func compareDockerCredential(secretList []corev1.Secret, secretClient v1.SecretInterface, harborServer, username, password string, isAdminAuth bool) {
	for _, s := range secretList {
		if !isAdminAuth {
			// skip admin kind of registry secret
			if isAdmin, ok := s.Labels[harborRegistryAdminLabel]; ok && isAdmin == "true" {
				continue
			}
		}

		secret := s.DeepCopy()
		dockerConfigContent := secret.Data[corev1.DockerConfigJsonKey]
		dockerConfig := &credentialprovider.DockerConfigJson{}
		err := json.Unmarshal(dockerConfigContent, dockerConfig)
		if err != nil {
			logrus.Errorf("HarborSecretController: convert docker config json key for secret %s/%s error: %v", secret.Namespace, secret.Name, err)
			continue
		}
		dockerConfigAuth := dockerConfig.Auths
		newConfigAuth := map[string]credentialprovider.DockerConfigEntry{}
		for serverURL, configAuth := range dockerConfigAuth {
			if serverURL == harborServer {
				if (!isAdminAuth && configAuth.Username == username) || isAdminAuth {
					dockercfgAuth := credentialprovider.DockerConfigEntry{
						Username: username,
						Password: password,
					}
					newConfigAuth[serverURL] = dockercfgAuth
				}
			}
		}

		if len(newConfigAuth) > 0 {
			dockerConfig.Auths = newConfigAuth
			configJSON, err := json.Marshal(dockerConfig)
			if err != nil {
				logrus.Errorf("HarborSecretController: convert docker config auth error: %v", err)
				continue
			}
			secret.Data[corev1.DockerConfigJsonKey] = configJSON

			if !reflect.DeepEqual(configJSON, s.Data[corev1.DockerConfigJsonKey]) {
				_, err = secretClient.Update(secret)
				if err != nil && !apierrors.IsConflict(err) {
					logrus.Errorf("HarborSecretController: update secret %s/%s error: %v", secret.Namespace, secret.Name, err)
					continue
				}
			}
		}
	}
}
