package pod

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	uuid "github.com/satori/go.uuid"

	"github.com/mitchellh/mapstructure"
	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/api/handler"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/auth/util"
	"github.com/rancher/rancher/pkg/clustermanager"
	"github.com/rancher/types/apis/project.cattle.io/v3/schema"
	projectschema "github.com/rancher/types/apis/project.cattle.io/v3/schema"
	projectclient "github.com/rancher/types/client/project/v3"
	"github.com/rancher/types/config"
	"github.com/rancher/types/user"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	tmpDir = "./management-state/tmp"
)

type ActionWrapper struct {
	ClusterManager *clustermanager.Manager
	UserManager    user.Manager
}

func (a ActionWrapper) ActionHandler(actionName string, action *types.Action, apiContext *types.APIContext) error {
	var pod projectclient.Pod
	accessError := access.ByID(apiContext, &projectschema.Version, "pod", apiContext.ID, &pod)
	if accessError != nil {
		return httperror.NewAPIError(httperror.InvalidReference, "Error accessing pod")
	}
	namespace, name := splitID(pod.ID)
	switch actionName {
	case "download":
		clusterName := a.ClusterManager.ClusterName(apiContext)
		if clusterName == "" {
			return httperror.NewAPIError(httperror.ServerError, fmt.Sprintf("Cluster name empty %s", pod.ID))
		}
		clusterContext, err := a.ClusterManager.UserContext(clusterName)
		if err != nil {
			return httperror.NewAPIError(httperror.ServerError, fmt.Sprintf("Error getting cluster context %s", pod.ID))
		}
		return a.downloadPodFile(apiContext, clusterContext, actionName, pod, namespace, name)
	}
	return nil
}

func Formatter(apiContext *types.APIContext, resource *types.RawResource) {
	podID := resource.ID
	podSchema := apiContext.Schemas.Schema(&schema.Version, "pod")
	resource.Actions["download"] = apiContext.URLBuilder.ActionLinkByID(podSchema, podID, "download")
	resource.Links["yaml"] = apiContext.URLBuilder.Link("yaml", resource)
}

func splitID(id string) (string, string) {
	namespace := ""
	parts := strings.SplitN(id, ":", 2)
	if len(parts) == 2 {
		namespace = parts[0]
		id = parts[1]
	}

	return namespace, id
}

func (a ActionWrapper) getToken(apiContext *types.APIContext) (string, error) {
	userName := a.UserManager.GetUser(apiContext)
	return a.UserManager.EnsureToken("kubeconfig-"+userName, "Kubeconfig token", "kubeconfig", userName)
}

func (a ActionWrapper) getKubeConfig(apiContext *types.APIContext, clusterContext *config.UserContext) (*clientcmdapi.Config, error) {
	token, err := a.getToken(apiContext)
	if err != nil {
		return nil, err
	}

	cfg := a.ClusterManager.KubeConfig(clusterContext.ClusterName, token)
	return cfg, nil
}

func (a ActionWrapper) downloadPodFile(apiContext *types.APIContext, clusterContext *config.UserContext,
	actionName string, pod projectclient.Pod, namespace string, name string) error {
	input, err := handler.ParseAndValidateActionBody(apiContext, apiContext.Schemas.Schema(&projectschema.Version,
		projectclient.PodFileDownloadInputType))
	if err != nil {
		return httperror.NewAPIError(httperror.InvalidBodyContent,
			fmt.Sprintf("Failed to parse action body: %v", err))
	}
	podFileDownloadInput := &projectclient.PodFileDownloadInput{}
	if err := mapstructure.Decode(input, podFileDownloadInput); err != nil {
		return httperror.NewAPIError(httperror.InvalidBodyContent,
			fmt.Sprintf("Failed to parse body: %v", err))
	}
	cfg, err := a.getKubeConfig(apiContext, clusterContext)
	if err != nil {
		return httperror.NewAPIError(httperror.InvalidState,
			fmt.Sprintf("Failed to get kubeconfig: %v", err))
	}
	if podFileDownloadInput.ContainerName == "" &&
		len(pod.Containers) > 0 {
		podFileDownloadInput.ContainerName = pod.Containers[0].Name
	}
	kubeConfigFile, err := writeKubeConfig(cfg)
	defer cleanup(kubeConfigFile)
	if err != nil {
		return httperror.NewAPIError(httperror.InvalidState,
			fmt.Sprintf("Failed to make kubeconfig file: %v", err))
	}
	// Use the `kubectl.copy` snippet to complete the file copy to a static directory.
	_, podFileName := filepath.Split(podFileDownloadInput.FilePath)
	localPath := fmt.Sprintf("%s/%s-%s", util.GetProtectedStaticDir(), podFileName, uuid.NewV4().String())
	cmd := exec.Command("kubectl",
		"--kubeconfig",
		kubeConfigFile.Name(),
		"cp",
		namespace+"/"+name+":"+podFileDownloadInput.FilePath,
		localPath)
	if err := cmd.Start(); err != nil {
		return httperror.NewAPIError(httperror.InvalidState,
			fmt.Sprintf("Failed start to copy pod file: %v", err))
	}
	logrus.Infof("Copying pod files")
	if err := cmd.Wait(); err != nil {
		return httperror.NewAPIError(httperror.InvalidState,
			fmt.Sprintf("Failed to copy pod file: %v", err))
	}
	// Determining the existence of the file
	if !fileExists(localPath) {
		return httperror.NewAPIError(httperror.InvalidState,
			fmt.Sprintf("Failed to copy pod file: %v", "No such file or directory"))
	}
	logrus.Infof("Complete file copy")
	podfile, err := os.Open(localPath)
	defer cleanup(podfile)
	if err != nil {
		return httperror.NewAPIError(httperror.InvalidState,
			fmt.Sprintf("Failed to get FileInfo structure describing: %v", err))
	}
	FileStat, err := podfile.Stat()
	if err != nil {
		return httperror.NewAPIError(httperror.InvalidState,
			fmt.Sprintf("Failed to Statistical file size: %v", err))
	}
	FileSize := strconv.FormatInt(FileStat.Size(), 10)
	apiContext.Response.Header().Set("Content-Disposition", "attachment; filename="+podFileName)
	apiContext.Response.Header().Set("Content-Type", "application/octet-stream")
	apiContext.Response.Header().Set("x-decompressed-content-length", FileSize)
	apiContext.Response.Header().Set("Pandaria-Download-Attachment", podFileName)
	podfile.Seek(0, 0)
	io.Copy(apiContext.Response, podfile)
	return nil
}

func writeKubeConfig(kubeConfig *clientcmdapi.Config) (*os.File, error) {
	kubeConfigFile, err := tempFile("kubeconfig-")
	if err != nil {
		return nil, err
	}
	if err := clientcmd.WriteToFile(*kubeConfig, kubeConfigFile.Name()); err != nil {
		return nil, err
	}
	return kubeConfigFile, nil
}

func tempFile(prefix string) (*os.File, error) {
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		if err = os.MkdirAll(tmpDir, 0755); err != nil {
			return nil, err
		}
	}

	f, err := ioutil.TempFile(tmpDir, prefix)
	if err != nil {
		return nil, err
	}

	return f, f.Close()
}

func cleanup(files ...*os.File) {
	for _, file := range files {
		if file == nil {
			continue
		}
		os.Remove(file.Name())
	}
}

func fileExists(path string) bool {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}
