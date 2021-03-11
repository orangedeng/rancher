package monitoring

import (
	"fmt"
	"strings"

	corev1 "github.com/rancher/types/apis/core/v1"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	k8scorev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	nsByProjectIndex        = "resourcequota.cluster.cattle.io/ns-by-project"
	projectDisplayNameLabel = "monitoring.pandaria.io/projectName"
	projectIDLabel          = "field.cattle.io/projectId"
)

type namespaceHandler struct {
	clusterName          string
	cattleProjectClient  mgmtv3.ProjectInterface
	agentNamespaceClient corev1.NamespaceInterface
}

func (nsh *namespaceHandler) sync(key string, project *mgmtv3.Project) (runtime.Object, error) {
	if project == nil || project.DeletionTimestamp != nil ||
		project.Spec.ClusterName != nsh.clusterName {
		return project, nil
	}

	namespaces, err := nsh.agentNamespaceClient.List(metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", projectIDLabel, project.Name),
	})

	if err != nil {
		return nil, err
	}

	for _, ns := range namespaces.Items {
		projectID, ok := ns.Annotations[projectIDLabel]
		if ok && strings.Contains(projectID, project.Name) {
			name := ns.Labels[projectDisplayNameLabel]
			if project.Spec.DisplayName != name {
				nsCopy := ns.DeepCopy()
				nsCopy.Labels[projectDisplayNameLabel] = project.Spec.DisplayName
				_, err := nsh.agentNamespaceClient.Update(nsCopy)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	return nil, nil
}

func (nsh *namespaceHandler) syncNamespace(key string, ns *k8scorev1.Namespace) (runtime.Object, error) {
	if ns == nil || ns.DeletionTimestamp != nil {
		return ns, nil
	}
	projectID, ok := ns.Annotations[projectIDLabel]
	if ok {
		var name string
		if strings.Contains(projectID, ":") {
			ids := strings.Split(projectID, ":")
			if len(ids) == 2 {
				name = ids[1]
			}
		} else {
			name = projectID
		}

		project, err := nsh.cattleProjectClient.GetNamespaced(nsh.clusterName, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		nsProjectName := ns.Labels[projectDisplayNameLabel]
		if nsProjectName != project.Spec.DisplayName {
			nsh.cattleProjectClient.Controller().Enqueue(nsh.clusterName, name)
		}
	} else {
		nsProjectName, ok := ns.Labels[projectDisplayNameLabel]
		if ok && nsProjectName != "" {
			nsCopy := ns.DeepCopy()
			delete(nsCopy.Labels, projectDisplayNameLabel)
			_, err := nsh.agentNamespaceClient.Update(nsCopy)
			if err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}
