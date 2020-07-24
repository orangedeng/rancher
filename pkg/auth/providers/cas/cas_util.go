package cas

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	client "github.com/rancher/types/client/management/v3"
)

func formScheme(tls bool) string {
	scheme := "http://"
	if tls {
		scheme = "https://"
	}
	return scheme
}

func formCASRedirectURL(config *v3.CASConfig) string {
	return casRedirectURL(config.TLS, config.Hostname, config.Port, config.LoginEndpoint, config.Service)
}

func casRedirectURL(tls bool, hostname string, port string, loginEndpoint string, service string) string {
	if loginEndpoint == "" {
		loginEndpoint = "/cas/login"
	}
	formURL := fmt.Sprintf("%s%s:%s%s?service=%s", formScheme(tls), hostname, port, loginEndpoint, url.QueryEscape(service))
	return formURL
}

func formCASRedirectURLFromMap(config map[string]interface{}) string {
	tls, _ := config[client.CASConfigFieldTLS].(bool)
	hostname, _ := config[client.CASConfigFieldHostname].(string)
	port, _ := config[client.CASConfigFieldPort].(string)
	loginEndpoint, _ := config[client.CASConfigFieldLoginEndpoint].(string)
	service, _ := config[client.CASConfigFieldService].(string)
	return casRedirectURL(tls, hostname, port, loginEndpoint, service)
}

func formServiceValidateURLFromConfig(config *v3.CASConfig, ticket string) string {
	return formServiceValidateURL(config.TLS, config.Hostname, config.Port, config.ServiceValidate, config.Service, ticket)
}

func formServiceValidateURL(tls bool, hostname string, port string, serviceValidate string, service string, ticket string) string {
	if serviceValidate == "" {
		serviceValidate = "/cas/serviceValidate"
	}
	formURL := fmt.Sprintf("%s%s:%s%s?service=%s&ticket=%s", formScheme(tls), hostname, port, serviceValidate, url.QueryEscape(service), ticket)
	return formURL
}

func formCASLogoutURL(config *v3.CASConfig) string {
	return casLogoutURL(config.TLS, config.Hostname, config.Port, config.LogoutEndpoint)
}

func casLogoutURL(tls bool, hostname string, port string, logoutEndpoint string) string {
	if logoutEndpoint == "" {
		logoutEndpoint = "/cas/logout"
	}
	formURL := fmt.Sprintf("%s%s:%s%s", formScheme(tls), hostname, port, logoutEndpoint)
	return formURL
}

func formCASLogoutURLFromMap(config map[string]interface{}) string {
	tls, _ := config[client.CASConfigFieldTLS].(bool)
	hostname, _ := config[client.CASConfigFieldHostname].(string)
	port, _ := config[client.CASConfigFieldPort].(string)
	logoutEndpoint, _ := config[client.CASConfigFieldLogoutEndpoint].(string)
	return casLogoutURL(tls, hostname, port, logoutEndpoint)
}

func isThisUserMe(me v3.Principal, other v3.Principal) bool {
	if me.ObjectMeta.Name == other.ObjectMeta.Name && me.PrincipalType == other.PrincipalType {
		return true
	}
	return false
}

func toPrincipal(account Account) v3.Principal {
	principal := v3.Principal{
		ObjectMeta:    metav1.ObjectMeta{Name: PrincipalIDPrefix + account.Username},
		DisplayName:   account.Username,
		LoginName:     account.Username,
		Provider:      Name,
		Me:            false,
		PrincipalType: "user",
	}
	return principal
}

func toPrincipalWithID(principalID string, account Account) v3.Principal {
	principal := v3.Principal{
		ObjectMeta:    metav1.ObjectMeta{Name: principalID},
		DisplayName:   account.Username,
		LoginName:     account.Username,
		Provider:      Name,
		Me:            false,
		PrincipalType: "user",
	}
	return principal
}

func listUsersByKey(userLister v3.UserLister, searchKey string) ([]*v3.User, error) {
	var userResults []*v3.User

	allUsers, err := userLister.List("", labels.NewSelector())
	if err != nil {
		logrus.Infof("Failed to search User resources for %v: %v", searchKey, err)
		return userResults, err
	}
	for _, user := range allUsers {
		if !(strings.HasPrefix(user.ObjectMeta.Name, searchKey) || strings.HasPrefix(user.Username, searchKey) || strings.HasPrefix(user.DisplayName, searchKey)) {
			continue
		}
		userResults = append(userResults, user)
	}
	return userResults, nil
}

func parseCASPrincipalIDFromUser(user *v3.User) (string, bool) {
	for _, principalID := range user.PrincipalIDs {
		if strings.HasPrefix(principalID, PrincipalIDPrefix) {
			return principalID, true
		}
	}
	return "", false
}

func getCASNameByPrincipalID(principalID string) string {
	name := principalID
	id := strings.Split(principalID, "://")
	if len(id) == 2 {
		name = id[1]
	}
	return name
}
