package clusterregistrationtokens

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/urlbuilder"
	"github.com/rancher/rancher/pkg/image"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/rancher/pkg/systemtemplate"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/apis/management.cattle.io/v3/schema"
)

func ClusterImportHandler(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Type", "text/plain")
	token := mux.Vars(req)["token"]

	urlBuilder, err := urlbuilder.New(req, schema.Version, types.NewSchemas())
	if err != nil {
		resp.WriteHeader(500)
		resp.Write([]byte(err.Error()))
		return
	}
	url := urlBuilder.RelativeToRoot("")

	authImage := ""
	authImages := req.URL.Query()["authImage"]
	if len(authImages) > 0 {
		authImage = authImages[0]
	}

	// PANDARIA: using cluster private registry setting
	var cluster *v3.Cluster
	privateRegistries := req.URL.Query()["privateRegistry"]
	if len(privateRegistries) > 0 {
		cluster = &v3.Cluster{
			Spec: v3.ClusterSpec{
				ClusterSpecBase: v3.ClusterSpecBase{
					SystemDefaultRegistry: privateRegistries[0],
				},
			},
		}
	}

	if err := systemtemplate.SystemTemplate(resp, image.ResolveWithCluster(settings.AgentImage.Get(), cluster), authImage, "", token, url,
		false, cluster, nil); err != nil {
		resp.WriteHeader(500)
		resp.Write([]byte(err.Error()))
	}
	//PANDARIA: end
}
