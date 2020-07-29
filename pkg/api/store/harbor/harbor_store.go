package harbor

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/pkg/errors"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/namespace"
	"github.com/rancher/rancher/pkg/settings"
	v1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/project.cattle.io/v3"
	apicorev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DockerRegistryHarborLabel      = "rancher.cn/registry-harbor-auth"
	DockerRegistryHarborAdminLabel = "rancher.cn/registry-harbor-admin-auth"
	DockerRegistryAuthKey          = "harborAuth"
	AdminAuthSecretName            = "harbor-config"
)

type SecretStore struct {
	Store        types.Store
	SecretClient v1.SecretInterface
}

func NewStore(secretClient v1.SecretInterface, store types.Store) *SecretStore {
	return &SecretStore{
		Store:        store,
		SecretClient: secretClient,
	}
}

func (s *SecretStore) Context() types.StorageContext {
	return s.Store.Context()
}

func (s *SecretStore) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {
	user := apiContext.Request.Header.Get("Impersonate-User")
	if user == "" {
		return nil, httperror.NewAPIError(httperror.NotFound, "missing user")
	}

	labels := convert.ToMapInterface(data["labels"])
	if labels != nil {
		// only support empty registry body for harbor type
		if harborSecretLabel, ok := labels[DockerRegistryHarborLabel]; ok && harborSecretLabel == "true" {
			harborServer := settings.HarborServerURL.Get()
			if harborServer == "" {
				return nil, errors.New("can't create harbor type docker registry secret without setting harbor server")
			}
			serverURL, err := url.Parse(harborServer)
			if err != nil {
				return nil, err
			}
			registryConfig := convert.ToMapInterface(data["registries"])
			for registry, auth := range registryConfig {
				if strings.EqualFold(serverURL.Host, registry) {
					cred := &v3.RegistryCredential{}
					err := convert.ToObj(auth, cred)
					if err != nil {
						return nil, err
					}
					var auth, username, password string
					// check whether using admin auth
					if isAdminLabel, ok := labels[DockerRegistryHarborAdminLabel]; ok && isAdminLabel == "true" {
						// get admin auth secret
						adminSecret, err := s.SecretClient.GetNamespaced(namespace.PandariaGlobalNamespace, AdminAuthSecretName, metav1.GetOptions{})
						if err != nil {
							return nil, errors.New("can't create harbor type credential without sync with harbor")
						}
						username = string(adminSecret.Data[apicorev1.BasicAuthUsernameKey])
						password = string(adminSecret.Data[apicorev1.BasicAuthPasswordKey])
						adminAuth := fmt.Sprintf("%s:%s", username, password)
						auth = base64.StdEncoding.EncodeToString([]byte(adminAuth))
					} else {
						authSecret, err := s.SecretClient.GetNamespaced(user, fmt.Sprintf("%s-harbor", user), metav1.GetOptions{})
						if err != nil {
							return nil, errors.New("can't create harbor type credential without sync with harbor")
						}
						username, password, err = generateUserAndPassword(string(authSecret.Data[DockerRegistryAuthKey]))
						if err != nil {
							return nil, err
						}
						auth = base64.StdEncoding.EncodeToString(authSecret.Data[DockerRegistryAuthKey])
					}
					cred.Auth = auth
					cred.Username = username
					cred.Password = password
					registryConfig[registry] = cred
				}
			}
			data["registries"] = registryConfig
		}
	}

	return s.Store.Create(apiContext, schema, data)
}

func (s *SecretStore) ByID(apiContext *types.APIContext, schema *types.Schema, id string) (map[string]interface{}, error) {
	return s.Store.ByID(apiContext, schema, id)
}

func (s *SecretStore) Update(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, id string) (map[string]interface{}, error) {
	return s.Store.Update(apiContext, schema, data, id)
}

func (s *SecretStore) List(apiContext *types.APIContext, schema *types.Schema, opt *types.QueryOptions) ([]map[string]interface{}, error) {
	return s.Store.List(apiContext, schema, opt)
}

func (s *SecretStore) Delete(apiContext *types.APIContext, schema *types.Schema, id string) (map[string]interface{}, error) {
	return s.Store.Delete(apiContext, schema, id)
}

func (s *SecretStore) Watch(apiContext *types.APIContext, schema *types.Schema, opt *types.QueryOptions) (chan map[string]interface{}, error) {
	return s.Store.Watch(apiContext, schema, opt)
}

func generateUserAndPassword(auth string) (string, string, error) {
	authArray := strings.Split(string(auth), ":")
	if len(authArray) != 2 {
		return "", "", errors.New("Invalid harbor auth")
	}
	return authArray[0], authArray[1], nil
}
