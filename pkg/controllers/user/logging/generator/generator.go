package generator

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	loggingconfig "github.com/rancher/rancher/pkg/controllers/user/logging/config"
	workloadUtil "github.com/rancher/rancher/pkg/controllers/user/workload"
	"github.com/rancher/rancher/pkg/project"
	v1 "github.com/rancher/types/apis/core/v1"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"

	"github.com/pkg/errors"
	k8scorev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

var tmplCache = template.New("template")

const LoggingExcludeAnnotation = "field.cattle.io/excludeContainer"

func init() {
	tmplCache = tmplCache.Funcs(template.FuncMap{"escapeString": escapeString})
	tmplCache = template.Must(tmplCache.Parse(SourceTemplate))
	tmplCache = template.Must(tmplCache.Parse(FilterTemplate))
	tmplCache = template.Must(tmplCache.Parse(MatchTemplate))
	tmplCache = template.Must(tmplCache.Parse(Template))
}

func GenerateConfig(tempalteName string, conf interface{}) ([]byte, error) {
	buf := &bytes.Buffer{}
	if err := tmplCache.ExecuteTemplate(buf, tempalteName, conf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func GenerateClusterConfig(logging mgmtv3.ClusterLoggingSpec, excludeNamespaces, certDir string, workloadController *workloadUtil.CommonController, podLister v1.PodLister) ([]byte, error) {
	paths, err := getExcludePaths(workloadController, podLister, "")
	if err != nil {
		return nil, err
	}

	wl, err := newWrapClusterLogging(logging, excludeNamespaces, certDir, paths)
	if err != nil {
		return nil, errors.Wrap(err, "to wraper cluster logging failed")
	}

	if wl == nil {
		return []byte{}, nil
	}

	if logging.SyslogConfig != nil && logging.SyslogConfig.Token != "" {
		if err = ValidateSyslogToken(wl); err != nil {
			return nil, err
		}
	}

	if len(logging.OutputTags) != 0 {
		if err = ValidateCustomTags(wl); err != nil {
			return nil, err
		}
	}

	validateData := *wl
	if logging.FluentForwarderConfig != nil && wl.EnableShareKey {
		validateData.EnableShareKey = false //skip generate precan configure included ruby code
	}
	if err = ValidateCustomTarget(validateData); err != nil {
		return nil, err
	}

	buf, err := GenerateConfig("cluster-template", wl)
	if err != nil {
		return nil, errors.Wrap(err, "generate cluster config file failed")
	}

	return buf, nil
}

func GenerateProjectConfig(projectLoggings []*mgmtv3.ProjectLogging, namespaces []*k8scorev1.Namespace, systemProjectID, certDir string, workloadController *workloadUtil.CommonController, podLister v1.PodLister) ([]byte, error) {
	var wl []ProjectLoggingTemplateWrap
	for _, v := range projectLoggings {
		var containerSourcePath []string
		var paths []string
		for _, v2 := range namespaces {
			if nsProjectName, ok := v2.Annotations[project.ProjectIDAnn]; ok && nsProjectName == v.Spec.ProjectName {
				sourcePathPattern := loggingconfig.GetNamespacePathPattern(v2.Name)
				containerSourcePath = append(containerSourcePath, sourcePathPattern)

				nsPaths, err := getExcludePaths(workloadController, podLister, v2.Name)
				if err != nil {
					return nil, err
				}

				paths = append(paths, nsPaths...)
			}
		}

		if len(containerSourcePath) == 0 {
			continue
		}

		sort.Strings(containerSourcePath)
		containerSourcePaths := strings.Join(containerSourcePath, ",")
		isSystemProject := v.Spec.ProjectName == systemProjectID
		wpl, err := newWrapProjectLogging(v.Spec, containerSourcePaths, certDir, isSystemProject, paths)
		if err != nil {
			return nil, err
		}

		if wpl == nil {
			continue
		}

		if wpl.SyslogConfig.Token != "" {
			if err = ValidateSyslogToken(wpl); err != nil {
				return nil, err
			}
		}

		if len(wpl.OutputTags) != 0 {
			if err = ValidateCustomTags(wpl); err != nil {
				return nil, err
			}
		}

		validateData := *wpl
		if v.Spec.FluentForwarderConfig != nil && wpl.EnableShareKey {
			validateData.EnableShareKey = false //skip generate precan configure included ruby code
		}
		if err = ValidateCustomTarget(validateData); err != nil {
			return nil, err
		}

		wl = append(wl, *wpl)
	}

	buf, err := GenerateConfig("project-template", wl)
	if err != nil {
		return nil, errors.Wrap(err, "generate project config file failed")
	}

	return buf, nil
}

func getExcludePaths(workloadController *workloadUtil.CommonController, podLister v1.PodLister, namespaceName string) ([]string, error) {
	if podLister == nil || workloadController == nil {
		return nil, nil
	}

	workloads, err := workloadController.GetAllWorkloads(namespaceName)
	if err != nil {
		return nil, err
	}

	var podListers []*k8scorev1.Pod
	for _, workload := range workloads {
		if strings.EqualFold(workload.Kind, workloadUtil.ReplicationControllerType) || strings.EqualFold(workload.Kind, workloadUtil.ReplicaSetType) {
			continue
		}

		if value, ok := workload.TemplateSpec.Annotations[LoggingExcludeAnnotation]; ok && value == "true" {
			pods, err := podLister.List(namespaceName, labels.Set(workload.SelectorLabels).AsSelector())
			if err != nil {
				return nil, err
			}

			podListers = append(podListers, pods...)
		}
	}

	var paths []string
	for _, pod := range podListers {
		if pod.DeletionTimestamp != nil {
			continue
		}

		path := fmt.Sprintf(`"/var/log/containers/%s_*.log"`, pod.Name)
		paths = append(paths, path)
	}

	return paths, nil
}
