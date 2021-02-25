package httpproxy

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/rancher/rancher/pkg/auth/providers/publicapi"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
)

const (
	disableProxyLeader = "PANDARIA_DISABLE_PROXY_LEADER"
)

func ProxyToLeader(scaledContext *config.ScaledContext, next http.Handler) http.HandlerFunc {
	if needRegisterProxy() {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			leaderEndpoint := settings.LeaderEndpoint.Get()
			if leaderEndpoint == "" || scaledContext.PeerManager == nil {
				logrus.Debug("ProxyToLeader: skip proxy as it is a single server")
				next.ServeHTTP(w, req)
				return
			}
			if scaledContext.PeerManager != nil && scaledContext.PeerManager.IsLeader() {
				logrus.Debug("ProxyToLeader: do not proxy as leader is here")
				next.ServeHTTP(w, req)
				return
			}

			// TODO: we should also append the Config.HTTPSListenPort to replace hard-code port 443
			leaderurl, err := url.Parse(fmt.Sprintf("https://%s", leaderEndpoint))
			if err != nil {
				logrus.Warnf("ProxyToLeader: do not proxy as invalid endpoint %s, err: %v", leaderEndpoint, err)
				next.ServeHTTP(w, req)
				return
			}
			logrus.Debugf("PoxyToLeader: do proxy request to leader url %s", leaderurl.String())
			reverseProxy := httputil.NewSingleHostReverseProxy(leaderurl)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			reverseProxy.Transport = transport
			reverseProxy.ServeHTTP(w, req)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(w, req)
	})
}

func needRegisterProxy() bool {
	if os.Getenv(disableProxyLeader) != "" {
		logrus.Info("ProxyToLeader: skip register proxy as PANDARIA_DISABLE_PROXY_LEADER")
		return false
	}

	if !publicapi.HasLoginLimit() {
		logrus.Info("ProxyToLeader: skip register proxy as no login limit settings")
		return false
	}

	return true
}
