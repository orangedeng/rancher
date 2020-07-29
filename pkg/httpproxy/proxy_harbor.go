package httpproxy

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httputil"

	"github.com/rancher/rancher/pkg/namespace"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	apicorev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	HarborAdminHeader   = "X-API-Harbor-Admin-Header"
	HarborAccountHeader = "X-API-Harbor-Account-Header"

	HarborUserSecretKey = "harborAuth"
)

func NewHarborProxy(prefix string, validHosts Supplier, scaledContext *config.ScaledContext) http.Handler {
	p := proxy{
		prefix:             prefix,
		validHostsSupplier: validHosts,
		credentials:        scaledContext.Core.Secrets(""),
	}
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			harborAuth := ""
			// header for login check
			accountHeader := req.Header.Get(HarborAccountHeader)
			if accountHeader != "" {
				harborAuth = accountHeader
			} else {
				// check admin auth header
				adminHeader := req.Header.Get(HarborAdminHeader)
				if adminHeader == "true" {
					authSecret, err := scaledContext.Core.Secrets("").GetNamespaced(namespace.PandariaGlobalNamespace, "harbor-config", metav1.GetOptions{})
					if err != nil {
						if !apierrors.IsNotFound(err) {
							logrus.Errorf("Failed to get admin harbor auth %v: %v", req.Header, err)
						}
					} else {
						harborAuth = base64.StdEncoding.
							EncodeToString([]byte(fmt.Sprintf("%s:%s",
								authSecret.Data[apicorev1.BasicAuthUsernameKey], authSecret.Data[apicorev1.BasicAuthPasswordKey])))
					}
				} else {
					// get harbor user auth by rancher user
					userID := req.Header.Get("Impersonate-User")
					authSecret, err := scaledContext.Core.Secrets("").GetNamespaced(userID, fmt.Sprintf("%s-harbor", userID), metav1.GetOptions{})
					if err != nil {
						if !apierrors.IsNotFound(err) {
							logrus.Errorf("Failed to get harbor auth %v: %v", req.Header, err)
						}
					} else {
						harborAuth = base64.StdEncoding.EncodeToString(authSecret.Data[HarborUserSecretKey])
					}
				}
			}
			if harborAuth != "" {
				req.Header.Set(APIAuth, fmt.Sprintf("Basic %s", harborAuth))
			}
			if err := p.proxy(req); err != nil {
				logrus.Infof("Failed to proxy %v: %v", req, err)
			}
		},
		ModifyResponse: setModifiedHeaders,
	}
}
