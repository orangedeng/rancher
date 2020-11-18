package securityfilter

import (
	"strings"

	"github.com/rancher/norman/types"

	rbacv1 "k8s.io/api/rbac/v1"
)

var pandariaFilterResourceVerbs = map[string]string{
	"get":    "get",
	"create": "post",
	"delete": "delete",
	"list":   "get",
	"patch":  "put",
	"update": "put",
	"watch":  "",
}

type RequestAttributes struct {
	User       string
	Group      string
	Verb       string
	APIGroup   string
	APIVersion string
	Resource   string
	Path       string
}

func NewRequestAttributes(apiContext *types.APIContext, resource string) *RequestAttributes {
	return &RequestAttributes{
		User:       apiContext.Request.Header.Get("Impersonate-User"),
		Group:      apiContext.Request.Header.Get("Impersonate-Group"),
		Verb:       strings.ToLower(apiContext.Method),
		APIGroup:   apiContext.Version.Group,
		APIVersion: apiContext.Version.Version,
		Resource:   resource,
		Path:       apiContext.Request.URL.RequestURI(),
	}
}

func (a *RequestAttributes) RulesAllow(rules ...rbacv1.PolicyRule) bool {
	for i := range rules {
		if a.RuleAllows(&rules[i]) {
			return true
		}
	}

	return false
}

func (a *RequestAttributes) RuleAllows(rule *rbacv1.PolicyRule) bool {
	if rule.NonResourceURLs != nil {
		return VerbMatches(rule, a.Verb) && NonResourceURLMatches(rule, a.Path, "")
	}

	return VerbMatches(rule, a.Verb) && APIGroupMatches(rule, a.APIGroup) && ResourceMatches(rule, a.Resource)
}

func VerbMatches(rule *rbacv1.PolicyRule, requestedVerb string) bool {
	for _, ruleVerb := range rule.Verbs {
		if ruleVerb == rbacv1.VerbAll {
			return true
		}
		if pandariaFilterResourceVerbs[ruleVerb] == requestedVerb {
			return true
		}
	}

	return false
}

func APIGroupMatches(rule *rbacv1.PolicyRule, requestedGroup string) bool {
	for _, ruleGroup := range rule.APIGroups {
		if ruleGroup == rbacv1.APIGroupAll {
			return true
		}
		if ruleGroup == requestedGroup {
			return true
		}
	}

	return false
}

func ResourceMatches(rule *rbacv1.PolicyRule, requestedResource string) bool {
	for _, ruleResource := range rule.Resources {
		// if everything is allowed, we match
		if ruleResource == rbacv1.ResourceAll {
			return true
		}
		// if we have an exact match, we match
		if ruleResource == requestedResource {
			return true
		}
	}

	return false
}

func NonResourceURLMatches(rule *rbacv1.PolicyRule, requestedURL, schemaURL string) bool {
	for _, ruleURL := range rule.NonResourceURLs {
		if ruleURL == rbacv1.NonResourceAll {
			return true
		}
		if strings.ToLower(ruleURL) == strings.ToLower(requestedURL) {
			return true
		}

		paramRules := strings.Split(ruleURL, "/*")
		if strings.Contains(ruleURL, "*") && strings.HasPrefix(strings.ToLower(requestedURL), strings.TrimRight(strings.ToLower(paramRules[0]), "*")) {
			return true
		}

		if schemaURL != "" && strings.HasPrefix(strings.ToLower(ruleURL), schemaURL) && strings.HasPrefix(strings.ToLower(requestedURL), schemaURL) {
			return true
		}

	}

	return false
}

func IsLinksURLFit(requestedURL, ruleURL string) bool {
	if requestedURL == ruleURL {
		return true
	}
	if strings.HasSuffix(ruleURL, "*") && strings.HasPrefix(requestedURL, strings.TrimRight(ruleURL, "*")) {
		return true
	}
	return false
}

func IsLinksSubURLFit(requestedURL, ruleURL string) bool {
	if ruleURL == requestedURL {
		return true
	}
	rulePaths := strings.Split(ruleURL, "*")
	if len(rulePaths) > 1 {
		if strings.HasPrefix(requestedURL, strings.TrimRight(rulePaths[0], "*")) && strings.Contains(requestedURL, rulePaths[1]) {
			return true
		}
	}
	return false
}

func IsActionURLFit(requestedURL, ruleURL string) bool {
	if ruleURL == requestedURL {
		return true
	}
	if strings.Contains(ruleURL, "*") {
		paramRules := strings.Split(ruleURL, "?")
		if len(paramRules) > 1 &&
			strings.HasPrefix(requestedURL, strings.TrimRight(paramRules[0], "*")) &&
			strings.HasSuffix(requestedURL, paramRules[1]) {
			return true
		}
	}
	return false
}
