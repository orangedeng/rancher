package cas

import (
	"context"
	"errors"

	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/auth/providers/common"
	"github.com/rancher/rancher/pkg/auth/tokens"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/apis/management.cattle.io/v3public"
	publicclient "github.com/rancher/types/client/management/v3public"
	"github.com/rancher/types/config"
	"github.com/rancher/types/user"
)

const (
	Name              = "cas"
	PrincipalIDPrefix = Name + "_user://"
)

type casProvider struct {
	ctx         context.Context
	casClient   *Client
	authConfigs v3.AuthConfigInterface
	userMGR     user.Manager
	tokenMGR    *tokens.Manager
	userLister  v3.UserLister
}

func Configure(ctx context.Context, mgmtCtx *config.ScaledContext, userMGR user.Manager, tokenMGR *tokens.Manager, name string) common.AuthProvider {
	casClient := NewCASClient()
	return &casProvider{
		ctx:         ctx,
		authConfigs: mgmtCtx.Management.AuthConfigs(""),
		casClient:   casClient,
		userMGR:     userMGR,
		tokenMGR:    tokenMGR,
		userLister:  mgmtCtx.Management.Users("").Controller().Lister(),
	}
}

func (p *casProvider) GetName() string {
	return Name
}

func (p *casProvider) AuthenticateUser(ctx context.Context, input interface{}) (v3.Principal, []v3.Principal, string, error) {
	login, ok := input.(*v3public.CASLogin)
	if !ok {
		return v3.Principal{}, nil, "", errors.New("unexpected input type")
	}
	return p.loginUser(login, nil, false)
}

func (p *casProvider) SearchPrincipals(name, principalType string, myToken v3.Token) ([]v3.Principal, error) {
	return p.searchPrincipalsByName(name, principalType, myToken)
}

func (p *casProvider) GetPrincipal(principalID string, token v3.Token) (v3.Principal, error) {
	name := getCASNameByPrincipalID(principalID)
	principal := toPrincipalWithID(principalID, Account{Username: name})
	principal.Me = isThisUserMe(token.UserPrincipal, principal)
	return principal, nil
}

func (p *casProvider) CustomizeSchema(schema *types.Schema) {
	schema.ActionHandler = p.actionHandler
	schema.Formatter = p.formatter
}

func (p *casProvider) TransformToAuthProvider(authConfig map[string]interface{}) (map[string]interface{}, error) {
	cas := common.TransformToAuthProvider(authConfig)
	cas[publicclient.CASProviderFieldRedirectURL] = formCASRedirectURLFromMap(authConfig)
	cas[publicclient.CASProviderFieldLogoutURL] = formCASLogoutURLFromMap(authConfig)
	return cas, nil
}

func (p *casProvider) RefetchGroupPrincipals(principalID string, secret string) ([]v3.Principal, error) {
	return nil, nil
}

func (p *casProvider) CanAccessWithGroupProviders(userPrincipalID string, groups []v3.Principal) (bool, error) {
	return true, nil
}
