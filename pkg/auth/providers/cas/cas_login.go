package cas

import (
	"fmt"
	"time"

	"github.com/mitchellh/mapstructure"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/apis/management.cattle.io/v3public"
	client "github.com/rancher/types/client/management/v3"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func (p *casProvider) loginUser(login *v3public.CASLogin, config *v3.CASConfig, test bool) (v3.Principal, []v3.Principal, string, error) {
	var groupPrincipals []v3.Principal
	var userPrincipal v3.Principal
	var err error

	logrus.Debugln("cas loginUser start: ")
	start := time.Now()
	defer func() {
		end := time.Since(start)
		logrus.Debugln("cas login user end, time: ", end)
	}()

	// get config
	if config == nil {
		config, err = p.getCASConfig()
		if err != nil {
			return v3.Principal{}, nil, "", err
		}
	}

	// validateService
	ticket := login.Ticket
	validateURL := formServiceValidateURLFromConfig(config, ticket)
	username, err := p.casClient.ServiceValidate(validateURL, config.ConnectionTimeout, config)
	if err != nil {
		return v3.Principal{}, nil, "", err
	}

	account := Account{Username: username}
	userPrincipal = toPrincipal(account)
	userPrincipal.Me = true

	return userPrincipal, groupPrincipals, "", nil
}

func (p *casProvider) getCASConfig() (*v3.CASConfig, error) {
	authConfigObj, err := p.authConfigs.ObjectClient().UnstructuredClient().Get(Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve CASConfig, error: %v", err)
	}

	u, ok := authConfigObj.(runtime.Unstructured)
	if !ok {
		return nil, fmt.Errorf("failed to retrieve CASConfig, cannot read k8s Unstructured data")
	}
	storedCASConfigMap := u.UnstructuredContent()

	storedCASConfig := &v3.CASConfig{}
	mapstructure.Decode(storedCASConfigMap, storedCASConfig)

	metadataMap, ok := storedCASConfigMap["metadata"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("failed to retrieve CASConfig metadata, cannot read k8s Unstructured data")
	}

	typemeta := &metav1.ObjectMeta{}
	mapstructure.Decode(metadataMap, typemeta)
	storedCASConfig.ObjectMeta = *typemeta

	return storedCASConfig, nil
}

func (p *casProvider) saveCASConfig(config *v3.CASConfig) error {
	storedCASConfig, err := p.getCASConfig()
	if err != nil {
		return err
	}
	config.APIVersion = "management.cattle.io/v3"
	config.Kind = v3.AuthConfigGroupVersionKind.Kind
	config.Type = client.CASConfigType
	config.ObjectMeta = storedCASConfig.ObjectMeta

	logrus.Debugf("updating casConfig")
	_, err = p.authConfigs.ObjectClient().Update(config.ObjectMeta.Name, config)
	if err != nil {
		return err
	}
	return nil
}

func (p *casProvider) searchPrincipalsByName(searchKey, principalType string, token v3.Token) ([]v3.Principal, error) {

	var principals []v3.Principal
	var localUsers []*v3.User
	var err error

	localUsers, err = listUsersByKey(p.userLister, searchKey)
	if err != nil {
		logrus.Infof("Failed to search User resources for %v: %v", searchKey, err)
		return principals, err
	}

	if principalType == "" || principalType == "user" {
		for _, user := range localUsers {
			principalID, isCASUser := parseCASPrincipalIDFromUser(user)
			if isCASUser {
				username := getCASNameByPrincipalID(principalID)
				userPrincipal := toPrincipalWithID(principalID, Account{Username: username})
				userPrincipal.Me = isThisUserMe(token.UserPrincipal, userPrincipal)
				principals = append(principals, userPrincipal)
			}
		}
	}

	return principals, nil
}
