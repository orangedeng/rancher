package globalmonitoring

import (
	"context"

	"github.com/rancher/rancher/pkg/systemaccount"
	"github.com/rancher/types/config"
)

// Register initializes the controllers and registers
func Register(ctx context.Context, management *config.ManagementContext) {
	ah := &appHandler{
		appClient:            management.Project.Apps(""),
		clusterLister:        management.Management.Clusters("").Controller().Lister(),
		clusterClient:        management.Management.Clusters(""),
		secretClient:         management.Core.Secrets(""),
		secretLister:         management.Core.Secrets("").Controller().Lister(),
		systemAccountManager: systemaccount.NewManager(management),
	}

	ch := clusterHandler{
		clusterClient: management.Management.Clusters(""),
		appLister:     management.Project.Apps("").Controller().Lister(),
		appClient:     management.Project.Apps(""),
		projectLister: management.Management.Projects("").Controller().Lister(),
	}

	management.Project.Apps("").AddLifecycle(ctx, "globalmonitoring-app-handler", ah)
	management.Management.Clusters("").AddHandler(ctx, "globalmonitoring-cluster-handler", ch.sync)

}
