package cas

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/auth/providers/common"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/apis/management.cattle.io/v3public"
)

func (p *casProvider) formatter(apiContext *types.APIContext, resource *types.RawResource) {
	common.AddCommonActions(apiContext, resource)
	resource.AddAction(apiContext, "configureTest")
	resource.AddAction(apiContext, "testAndApply")
}

func (p *casProvider) actionHandler(actionName string, action *types.Action, request *types.APIContext) error {
	handled, err := common.HandleCommonAction(actionName, action, request, Name, p.authConfigs)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}

	if actionName == "configureTest" {
		return p.configureTest(actionName, action, request)
	} else if actionName == "testAndApply" {
		return p.testAndApply(actionName, action, request)
	}

	return httperror.NewAPIError(httperror.ActionNotAvailable, "")
}

func (p *casProvider) configureTest(actionName string, action *types.Action, request *types.APIContext) error {
	casConfig := &v3.CASConfig{}
	if err := json.NewDecoder(request.Request.Body).Decode(casConfig); err != nil {
		return httperror.NewAPIError(httperror.InvalidBodyContent,
			fmt.Sprintf("Failed to parse body: %v", err))
	}

	redirectURL := formCASRedirectURL(casConfig)
	data := map[string]interface{}{
		"redirectUrl": redirectURL,
		"type":        "casConfigTestOutput",
	}

	request.WriteResponse(http.StatusOK, data)
	return nil
}

func (p *casProvider) testAndApply(actionName string, action *types.Action, request *types.APIContext) error {
	var casConfig v3.CASConfig
	casTestAndApplyInput := &v3.CASTestAndApplyInput{}

	if err := json.NewDecoder(request.Request.Body).Decode(casTestAndApplyInput); err != nil {
		return httperror.NewAPIError(httperror.InvalidBodyContent,
			fmt.Sprintf("Failed to parse body: %v", err))
	}

	casConfig = casTestAndApplyInput.CASConfig
	casLogin := &v3public.CASLogin{
		Ticket: casTestAndApplyInput.Ticket,
	}

	//Call provider to testLogin
	userPrincipal, groupPrincipals, providerInfo, err := p.loginUser(casLogin, &casConfig, true)
	if err != nil {
		if httperror.IsAPIError(err) {
			return err
		}
		return errors.Wrap(err, "server error while authenticating")
	}

	//if this works, save casConfig CR adding enabled flag
	user, err := p.userMGR.SetPrincipalOnCurrentUser(request, userPrincipal)
	if err != nil {
		return err
	}

	casConfig.Enabled = casTestAndApplyInput.Enabled
	err = p.saveCASConfig(&casConfig)
	if err != nil {
		return httperror.NewAPIError(httperror.ServerError, fmt.Sprintf("Failed to save cas config: %v", err))
	}

	return p.tokenMGR.CreateTokenAndSetCookie(user.Name, userPrincipal, groupPrincipals, providerInfo, 0, "Token via CAS Configuration", request)
}
