package f5management

import (
	"context"

	f5cisv1 "github.com/F5Networks/k8s-bigip-ctlr/config/apis/cis/v1"
	"github.com/rancher/norman/store/crd"
	"github.com/rancher/norman/types"
	f5 "github.com/rancher/rancher/pkg/f5cis"
	projectclient "github.com/rancher/types/client/project/v3"
	"github.com/rancher/types/config"
	"github.com/rancher/types/factory"
)

func CRDSetup(ctx context.Context, apiContext *config.UserOnlyContext) error {
	overrided := struct {
		types.Namespaced
	}{}

	schemas := factory.Schemas(&f5.APIVersion).
		MustImport(&f5.APIVersion, f5cisv1.VirtualServer{}, overrided).
		MustImport(&f5.APIVersion, f5cisv1.TransportServer{}, overrided).
		MustImport(&f5.APIVersion, f5cisv1.ExternalDNS{}, overrided).
		MustImportAndCustomize(&f5.APIVersion, f5cisv1.TLSProfile{}, func(schema *types.Schema) {
			schema.ID = "TLSProfile"
		}, overrided)

	f, err := crd.NewFactoryFromClient(apiContext.RESTConfig)
	if err != nil {
		return err
	}

	_, err = f.CreateCRDs(ctx, config.UserStorageContext,
		schemas.Schema(&f5.APIVersion, projectclient.VirtualServerType),
		schemas.Schema(&f5.APIVersion, projectclient.TransportServerType),
		schemas.Schema(&f5.APIVersion, projectclient.TLSProfileType),
		schemas.Schema(&f5.APIVersion, projectclient.ExternalDNSType),
	)

	err = f.BatchWait()

	return err
}
