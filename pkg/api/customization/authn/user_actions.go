package authn

import (
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/parse"
	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/auth/providerrefresh"
	"github.com/rancher/rancher/pkg/auth/util/aesutil"
	"github.com/rancher/rancher/pkg/settings"
	corev1 "github.com/rancher/types/apis/core/v1"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	client "github.com/rancher/types/client/management/v3"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (h *Handler) UserFormatter(apiContext *types.APIContext, resource *types.RawResource) {
	resource.AddAction(apiContext, "setpassword")
	// pandaria
	resource.AddAction(apiContext, "setharborauth")
	resource.AddAction(apiContext, "updateharborauth")
	if canRefresh := h.userCanRefresh(apiContext); canRefresh {
		resource.AddAction(apiContext, "refreshauthprovideraccess")
	}
}

func (h *Handler) CollectionFormatter(apiContext *types.APIContext, collection *types.GenericCollection) {
	collection.AddAction(apiContext, "changepassword")
	if canRefresh := h.userCanRefresh(apiContext); canRefresh {
		collection.AddAction(apiContext, "refreshauthprovideraccess")
	}
	// PANDARIA
	if canSetHarbor := h.userCanConfigHarbor(apiContext); canSetHarbor {
		collection.AddAction(apiContext, "saveharborconfig")
	}
	collection.AddAction(apiContext, "syncharboruser")
}

type Handler struct {
	UserClient               v3.UserInterface
	GlobalRoleBindingsClient v3.GlobalRoleBindingInterface
	UserAuthRefresher        providerrefresh.UserAuthRefresher
	SecretClient             corev1.SecretInterface    // PANDARIA
	HarborClient             *http.Client              //PANDARIA
	NamespaceClient          corev1.NamespaceInterface // PANDARIA
}

func (h *Handler) Actions(actionName string, action *types.Action, apiContext *types.APIContext) error {
	switch actionName {
	case "changepassword":
		if err := h.changePassword(actionName, action, apiContext); err != nil {
			return err
		}
	case "setpassword":
		if err := h.setPassword(actionName, action, apiContext); err != nil {
			return err
		}
	case "refreshauthprovideraccess":
		if err := h.refreshAttributes(actionName, action, apiContext); err != nil {
			return err
		}
	case "setharborauth":
		if err := h.setHarborAuth(actionName, action, apiContext); err != nil {
			return err
		}
	case "updateharborauth":
		if err := h.updateHarborAuth(actionName, action, apiContext); err != nil {
			return err
		}
	case "syncharboruser":
		if err := h.syncHarborUser(actionName, action, apiContext); err != nil {
			return err
		}
	case "saveharborconfig":
		if err := h.saveHarborConfig(actionName, action, apiContext); err != nil {
			return err
		}
	default:
		return errors.Errorf("bad action %v", actionName)
	}

	if !strings.EqualFold(settings.FirstLogin.Get(), "false") {
		if err := settings.FirstLogin.Set("false"); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) changePassword(actionName string, action *types.Action, request *types.APIContext) error {
	actionInput, err := parse.ReadBody(request.Request)
	if err != nil {
		return err
	}

	store := request.Schema.Store
	if store == nil {
		return errors.New("no user store available")
	}

	userID := request.Request.Header.Get("Impersonate-User")
	if userID == "" {
		return errors.New("can't find user")
	}

	// Pandaria: descrypt password
	var needDescrypt = false
	var descryptKey string
	if parse.IsBrowser(request.Request, false) {
		cookie, err := request.Request.Cookie("CSRF")
		if err == http.ErrNoCookie {
			logrus.Error("Can not get descrypt key for user password")
			return errors.New("Can not get descrypt key for user password")
		}
		needDescrypt = true
		descryptKey = cookie.Value
	}

	currentPass, ok := actionInput["currentPassword"].(string)
	// Pandaria: descrypt password
	if needDescrypt && currentPass != "" {
		aesd := aesutil.New()
		pass, err := aesd.DecryptBytes(descryptKey, []byte(currentPass))
		if err != nil {
			logrus.Errorf("Descrypt user password error: %v", err)
			return err
		}
		if len(pass) == 0 {
			logrus.Errorf("Descrypt user password error, decrypted pass length is 0")
			return errors.New("Descrypt user password error, decrypted pass length is 0")
		}
		currentPass = string(pass)
	}

	if !ok || len(currentPass) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "must specify current password")
	}

	newPass, ok := actionInput["newPassword"].(string)
	// Pandaria: descrypt password
	if needDescrypt && newPass != "" {
		aesd := aesutil.New()
		pass, err := aesd.DecryptBytes(descryptKey, []byte(newPass))
		if err != nil {
			logrus.Errorf("Descrypt user password error: %v", err)
			return err
		}
		if len(pass) == 0 {
			logrus.Errorf("Descrypt user password error, decrypted pass length is 0")
			return errors.New("Descrypt user password error, decrypted pass length is 0")
		}

		newPass = string(pass)
	}

	if !ok || len(newPass) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid new password")
	}

	user, err := h.UserClient.Get(userID, v1.GetOptions{})
	if err != nil {
		return err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPass)); err != nil {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid current password")
	}

	newPassHash, err := HashPasswordString(newPass)
	if err != nil {
		return err
	}

	user.Password = newPassHash
	user.MustChangePassword = false
	user, err = h.UserClient.Update(user)
	if err != nil {
		return err
	}

	return nil
}

func (h *Handler) setPassword(actionName string, action *types.Action, request *types.APIContext) error {
	actionInput, err := parse.ReadBody(request.Request)
	if err != nil {
		return err
	}

	store := request.Schema.Store
	if store == nil {
		return errors.New("no user store available")
	}

	userData, err := store.ByID(request, request.Schema, request.ID)
	if err != nil {
		return err
	}

	var needDescrypt = false
	var descryptKey string
	if parse.IsBrowser(request.Request, false) {
		cookie, err := request.Request.Cookie("CSRF")
		if err == http.ErrNoCookie {
			logrus.Error("Can not get descrypt key for user password")
			return errors.New("Can not get descrypt key for user password")
		}
		needDescrypt = true
		descryptKey = cookie.Value
	}

	newPass, ok := actionInput["newPassword"].(string)
	// Pandaria: descrypt password
	if needDescrypt && newPass != "" {
		aesd := aesutil.New()
		pass, err := aesd.DecryptBytes(descryptKey, []byte(newPass))
		if err != nil {
			logrus.Errorf("Descrypt user password error: %v", err)
			return err
		}
		if len(pass) == 0 {
			logrus.Errorf("Descrypt user password error, decrypted pass length is 0")
			return errors.New("Descrypt user password error, decrypted pass length is 0")
		}
		newPass = string(pass)
	}

	if !ok || len(newPass) == 0 {
		return errors.New("Invalid password")
	}

	userData[client.UserFieldPassword] = newPass
	if err := hashPassword(userData); err != nil {
		return err
	}
	userData[client.UserFieldMustChangePassword] = false
	delete(userData, "me")

	userData, err = store.Update(request, request.Schema, userData, request.ID)
	if err != nil {
		return err
	}

	request.WriteResponse(http.StatusOK, userData)
	return nil
}

func (h *Handler) refreshAttributes(actionName string, action *types.Action, request *types.APIContext) error {
	canRefresh := h.userCanRefresh(request)

	if !canRefresh {
		return errors.New("Not Allowed")
	}

	if request.ID != "" {
		h.UserAuthRefresher.TriggerUserRefresh(request.ID, true)
	} else {
		h.UserAuthRefresher.TriggerAllUserRefresh()
	}

	request.WriteResponse(http.StatusOK, nil)
	return nil
}

func (h *Handler) userCanRefresh(request *types.APIContext) bool {
	return request.AccessControl.CanDo(v3.UserGroupVersionKind.Group, v3.UserResource.Name, "create", request, nil, request.Schema) == nil
}

func (h *Handler) userCanConfigHarbor(request *types.APIContext) bool {
	return request.AccessControl.CanDo(v3.SettingGroupVersionKind.Group, v3.SettingResource.Name, "update", request, nil, request.Schema) == nil
}
