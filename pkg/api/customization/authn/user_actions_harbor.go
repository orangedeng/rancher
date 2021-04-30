package authn

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/parse"
	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/auth/util/aesutil"
	"github.com/rancher/rancher/pkg/namespace"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/sirupsen/logrus"
	apicorev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	HarborUserAnnotationAuth         = "authz.management.cattle.io.cn/harborauth"
	HarborUserAnnotationEmail        = "authz.management.cattle.io.cn/harboremail"
	HarborUserAnnotationSyncComplete = "management.harbor.pandaria.io/synccomplete"

	HarborUserAuthSecretLabel = "management.harbor.pandaria.io/harbor-secrets"
	HarborUserSecretKey       = "harborAuth"
	HarborLDAPMode            = "ldap_auth"
	HarborLocalMode           = "db_auth"
	HarborAdminConfig         = "harbor-config"
	HarborVersion2            = "v2.0"
)

type HarborUser struct {
	UserID       int    `json:"user_id"`
	UserName     string `json:"username"`
	Email        string `json:"email"`
	Password     string `json:"password"`
	RealName     string `json:"realname"`
	Deleted      bool   `json:"deleted"`
	HasAdminRole bool   `json:"has_admin_role"`
}

type HarborPassword struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

type HarborAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type HarborAPIErrorV2 struct {
	Errors []HarborAPIError `json:"errors"`
}

// setHarborAuth sync rancher user with harbor.
// If harbor auth is `db_auth`, will create a new harbor user or using current auth to sync with harbor.
// If harbor auth is `ldap_auth`, will using current auth to sync with harbor.
// If current harbor auth is not valid, will update harbor auth secret with new auth.
func (h *Handler) setHarborAuth(actionName string, action *types.Action, request *types.APIContext) error {
	userID := request.Request.Header.Get("Impersonate-User")
	if userID == "" {
		return errors.New("can't find user")
	}

	// get harbor-server to sync user
	harborServer := settings.HarborServerURL.Get()
	if harborServer == "" {
		return httperror.NewAPIError(httperror.InvalidOption, "Can't sync with harbor without setting harbor-server-url")
	}

	// get harbor-version
	harborVersion := settings.HarborVersion.Get()

	adminSecret, err := h.SecretClient.GetNamespaced(namespace.PandariaGlobalNamespace, HarborAdminConfig, v1.GetOptions{})
	if err != nil {
		logrus.Errorf("can't set harbor auth without admin config: %v", err)
		return httperror.NewAPIError(httperror.InvalidOption, "Can't sync with harbor without config admin auth")
	}

	harborAdminAuth := fmt.Sprintf("%s:%s", string(adminSecret.Data[apicorev1.BasicAuthUsernameKey]), string(adminSecret.Data[apicorev1.BasicAuthPasswordKey]))

	actionInput, err := parse.ReadBody(request.Request)
	if err != nil {
		return err
	}
	store := request.Schema.Store
	if store == nil {
		return errors.New("no user store available")
	}

	harborAuthMode := settings.HarborAuthMode.Get()
	if harborAuthMode == "" || (!strings.EqualFold(harborAuthMode, HarborLocalMode) && !strings.EqualFold(harborAuthMode, HarborLDAPMode)) {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "Unsupport harbor auth mode")
	}
	harborEmail, ok := actionInput["email"].(string)
	if !ok || len(harborEmail) == 0 {
		harborEmail = "default@from-rancher.com"
	}

	harborUserName, ok := actionInput["username"].(string)
	if !ok || len(harborUserName) == 0 {
		harborUserName = ""
	}
	harborPwd, ok := actionInput["password"].(string)
	if !ok || len(harborPwd) == 0 {
		harborPwd = ""
	}

	// If current harbor auth is not valid, rancher will call user reset username and password to resynchronization harbor auth
	// If so, will update auth secret with new auth
	if harborUserName != "" && harborPwd != "" {
		logrus.Debugf("resync user %s with latest auth", harborUserName)
		// decrypt password
		pass, err := decryptPassword(request, harborPwd)
		if err != nil {
			return err
		}
		harborAuth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", harborUserName, pass)))
		// harbor auth validation
		_, err = h.harborAuthValidation(harborServer, harborAuth, harborVersion)
		if err != nil {
			// check user exist due to password validation
			if strings.EqualFold(harborAuthMode, HarborLocalMode) {
				statusCode, e := h.createNewHarborUser(harborAdminAuth, harborUserName, pass, harborEmail, harborServer, harborVersion)
				// if return 409 means user already exist
				if e != nil {
					return e
				}
				if statusCode == http.StatusConflict && e == nil {
					return err
				}
			} else {
				return err
			}
		}
		err = h.ensureHarborAuth(fmt.Sprintf("%s:%s", harborUserName, pass), userID, true)
		if err != nil {
			return err
		}
		return h.ensureUserSyncComplete(userID, harborAuthMode, harborEmail)
	}

	logrus.Debugf("sync user %s with default auth", userID)
	// if not set username and password, means current harbor auth is valid, will use the default auth to check
	authSecret, err := h.SecretClient.GetNamespaced(userID, fmt.Sprintf("%s-harbor", userID), v1.GetOptions{})
	if err != nil {
		// if harbor auth secret is missing, need re-login to ensure harbor auth
		if apierrors.IsNotFound(err) {
			return httperror.NewAPIError(httperror.ErrorCode{
				Code:   "SyncHarborFailed",
				Status: http.StatusGone,
			}, "Missing harbor auth")
		}
		return err
	}

	if authSecret != nil && authSecret.Data != nil {
		// if secret auth is not valid, need to call user reset harbor auth
		harborAuth := authSecret.Data[HarborUserSecretKey]
		harborAuthArray := strings.Split(string(harborAuth), ":")
		if len(harborAuthArray) != 2 {
			return httperror.NewAPIError(httperror.ErrorCode{
				Code:   "SyncHarborFailed",
				Status: http.StatusGone,
			}, "Invalid harbor auth")
		}
		harborUserName := harborAuthArray[0]
		harborUserAuthPwd := harborAuthArray[1]
		if strings.EqualFold(harborAuthMode, HarborLocalMode) {
			_, err = h.createNewHarborUser(harborAdminAuth, harborUserName, harborUserAuthPwd, harborEmail, harborServer, harborVersion)
			if err != nil {
				return err
			}
		}
		harborAuthHeader := base64.StdEncoding.EncodeToString(harborAuth)
		// sync harbor user
		_, err = h.harborAuthValidation(harborServer, harborAuthHeader, harborVersion)
		if err != nil {
			return err
		}
		return h.ensureUserSyncComplete(userID, harborAuthMode, harborEmail)
	}

	return nil
}

// updateHarborAuth help to change harbor user password from rancher(only available for db_auth of Harbor)
func (h *Handler) updateHarborAuth(actionName string, action *types.Action, request *types.APIContext) error {
	actionInput, err := parse.ReadBody(request.Request)
	if err != nil {
		return err
	}
	store := request.Schema.Store
	if store == nil {
		return errors.New("no user store available")
	}

	harborAuthMode := settings.HarborAuthMode.Get()
	if harborAuthMode == "" || !strings.EqualFold(harborAuthMode, HarborLocalMode) {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "Unsupport change password")
	}

	userID := request.Request.Header.Get("Impersonate-User")
	if userID == "" {
		return errors.New("can't find user")
	}

	newPwd, ok := actionInput["newPassword"].(string)
	if !ok || len(newPwd) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid value of newPassword")
	}
	// decrypt password
	newPwdValue, err := decryptPassword(request, newPwd)
	if err != nil {
		return err
	}

	oldPwd, ok := actionInput["oldPassword"].(string)
	if !ok || len(oldPwd) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid value of oldPassword")
	}
	// decrypt password
	oldPwdValue, err := decryptPassword(request, oldPwd)
	if err != nil {
		return err
	}

	adminHeader := request.Request.Header.Get("X-API-Harbor-Admin-Header")
	if adminHeader == "true" {
		adminSecret, err := h.SecretClient.GetNamespaced(namespace.PandariaGlobalNamespace, HarborAdminConfig, v1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return httperror.NewAPIError(httperror.ErrorCode{
					Code:   "SyncHarborFailed",
					Status: http.StatusGone,
				}, "Missing harbor admin auth")
			}
			return err
		}
		if adminSecret != nil && adminSecret.Data != nil {
			harborUserAuth := base64.StdEncoding.
				EncodeToString([]byte(fmt.Sprintf("%s:%s", adminSecret.Data[apicorev1.BasicAuthUsernameKey],
					adminSecret.Data[apicorev1.BasicAuthPasswordKey])))
			_, err = h.changeHarborPassword(harborUserAuth, oldPwdValue, newPwdValue)
			if err != nil {
				return err
			}
			updateSecret := adminSecret.DeepCopy()
			updateSecret.Data[apicorev1.BasicAuthPasswordKey] = []byte(newPwdValue)
			_, err = h.SecretClient.Update(updateSecret)
			if err != nil && !apierrors.IsConflict(err) {
				return err
			}
		}
	} else {
		authSecret, err := h.SecretClient.GetNamespaced(userID, fmt.Sprintf("%s-harbor", userID), v1.GetOptions{})
		if err != nil {
			// if harbor auth secret is missing, need to re-sync harbor auth
			if apierrors.IsNotFound(err) {
				return httperror.NewAPIError(httperror.ErrorCode{
					Code:   "SyncHarborFailed",
					Status: http.StatusGone,
				}, "Missing harbor auth")
			}
			return err
		}
		if authSecret != nil && authSecret.Data != nil {
			harborUserAuth := base64.StdEncoding.EncodeToString(authSecret.Data[HarborUserSecretKey])
			harborUserName, err := h.changeHarborPassword(harborUserAuth, oldPwdValue, newPwdValue)
			if err != nil {
				return err
			}
			// update secret auth
			newHarborAuth := fmt.Sprintf("%s:%s", harborUserName, string(newPwdValue))
			updateSecret := authSecret.DeepCopy()
			updateSecret.Data[HarborUserSecretKey] = []byte(newHarborAuth)
			_, err = h.SecretClient.Update(updateSecret)
			if err != nil && !apierrors.IsConflict(err) {
				return err
			}
		}
	}

	request.WriteResponse(http.StatusOK, nil)

	return nil
}

func (h *Handler) serveHarbor(method, url, auth string, body []byte) ([]byte, int, error) {
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	if auth != "" {
		request.Header.Set("Authorization", auth)
	}

	request.Header.Add("Content-Type", "application/json")
	response, err := h.HarborClient.Do(request)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent {
		result, err := ioutil.ReadAll(response.Body)
		if err != nil {
			return nil, response.StatusCode, err
		}
		return result, response.StatusCode, fmt.Errorf("%s", string(result))
	}
	respResult, err := ioutil.ReadAll(response.Body)
	return respResult, response.StatusCode, err
}

func (h Handler) changeHarborPassword(harborUserAuth, oldPwdValue, newPwdValue string) (string, error) {
	// get harbor-server and auth to sync user
	harborServer := settings.HarborServerURL.Get()

	harborVersion := settings.HarborVersion.Get()

	result, err := h.harborAuthValidation(harborServer, harborUserAuth, harborVersion)

	versionPath := h.harborVersionChangeToPath(harborVersion)
	if err != nil {
		return "", err
	}
	harborUser := &HarborUser{}
	err = json.Unmarshal(result, harborUser)
	if err != nil {
		return "", err
	}

	// update user password
	pwd := &HarborPassword{
		OldPassword: oldPwdValue,
		NewPassword: newPwdValue,
	}

	logrus.Infof("update harbor auth for user %d:%s", harborUser.UserID, harborUser.UserName)
	updatePwdBody, err := json.Marshal(pwd)
	r, statusCode, err := h.serveHarbor("PUT", fmt.Sprintf("%s/%s/users/%d/password", harborServer, versionPath, harborUser.UserID), fmt.Sprintf("Basic %s", harborUserAuth), updatePwdBody)
	if err != nil {
		return "", httperror.NewAPIError(httperror.ErrorCode{
			Code:   "SyncHarborFailed",
			Status: statusCode,
		}, string(r))
	}

	return harborUser.UserName, nil
}

// syncHarborUser: Used for sync harbor auth when user login to rancher
// `<userID>-harbor` will be the auth secret saved under user namespace
func (h *Handler) syncHarborUser(actionName string, action *types.Action, request *types.APIContext) error {
	// if not config harbor server, skip
	if settings.HarborServerURL.Get() == "" {
		return nil
	}

	// ensure harbor auth mode
	if err := h.ensureHarborAuthMode(); err != nil {
		return err
	}

	actionInput, err := parse.ReadBody(request.Request)
	if err != nil {
		return err
	}

	username, ok := actionInput["username"].(string)
	if !ok || len(username) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid username")
	}

	// only support for ldap and local auth
	provider, ok := actionInput["provider"].(string)
	if !ok || len(provider) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid provider")
	}

	// only support ldap and db auth
	harborAuthMode := settings.HarborAuthMode.Get()
	if harborAuthMode == "" || (!strings.EqualFold(harborAuthMode, HarborLocalMode) && !strings.EqualFold(harborAuthMode, HarborLDAPMode)) {
		return nil
	}

	password, ok := actionInput["password"].(string)
	if !ok || len(password) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid password")
	}

	// decrypt password
	pass, err := decryptPassword(request, password)
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

	harborAuthValue := fmt.Sprintf("%s:%s", username, pass)

	switch provider {
	case "local":
		err := h.ensureHarborAuth(harborAuthValue, userID, false)
		if err != nil {
			logrus.Errorf("ensure harbor auth secret error: %v", err)
			return err
		}
	case "openldap", "activedirectory":
		needCheckAuthChange := false
		if strings.EqualFold(harborAuthMode, HarborLDAPMode) {
			needCheckAuthChange = true
		}
		err := h.ensureHarborAuth(harborAuthValue, userID, needCheckAuthChange)
		if err != nil {
			logrus.Errorf("ensure harbor auth secret error: %v", err)
			return err
		}
	default:
		return nil
	}

	request.WriteResponse(http.StatusOK, nil)

	return nil
}

// saveHarborConfig: save harbor admin auth to secret `harbor-config` under `cattle-global-data` namespace
func (h *Handler) saveHarborConfig(actionName string, action *types.Action, request *types.APIContext) error {
	actionInput, err := parse.ReadBody(request.Request)
	if err != nil {
		return err
	}

	username, ok := actionInput["username"].(string)
	if !ok || len(username) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid username")
	}

	password, ok := actionInput["password"].(string)
	if !ok || len(password) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid password")
	}

	serverURL, ok := actionInput["serverURL"].(string)
	if !ok || len(serverURL) == 0 {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid server url")
	}

	version, ok := actionInput["version"].(string)
	if !ok {
		return httperror.NewAPIError(httperror.InvalidBodyContent, "invalid version")
	}

	store := request.Schema.Store
	if store == nil {
		return errors.New("no user store available")
	}

	userID := request.Request.Header.Get("Impersonate-User")
	if userID == "" {
		return errors.New("can't find user")
	}

	pass, err := decryptPassword(request, password)
	if err != nil {
		return err
	}

	encodeAuth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, pass)))
	_, err = h.harborAuthValidation(serverURL, encodeAuth, version)
	if err != nil {
		return err
	}

	return h.createOrUpdateAuthSecrets(username, pass)
}

// createOrUpdateAuthSecrets will create or update harbor admin auth secret
func (h *Handler) createOrUpdateAuthSecrets(username, password string) error {
	err := h.ensureUserNamespace(namespace.PandariaGlobalNamespace)
	if err != nil {
		return err
	}

	secret := &apicorev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      HarborAdminConfig,
			Namespace: namespace.PandariaGlobalNamespace,
		},
		Type: apicorev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			apicorev1.BasicAuthUsernameKey: []byte(username),
			apicorev1.BasicAuthPasswordKey: []byte(password),
		},
	}

	curr, err := h.SecretClient.GetNamespaced(namespace.PandariaGlobalNamespace, HarborAdminConfig, v1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if err == nil && !reflect.DeepEqual(curr.Data, secret.Data) {
		_, err = h.SecretClient.Update(secret)
		if err != nil {
			return err
		}
	} else if apierrors.IsNotFound(err) {
		_, err = h.SecretClient.Create(secret)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}

	return nil
}

// ensureHarborAuth will ensure harbor auth secret exists under user namespace
func (h *Handler) ensureHarborAuth(harborAuthEncodeValue, userID string, needCheckAuthChange bool) error {
	user, err := h.UserClient.Get(userID, v1.GetOptions{})
	if err != nil {
		return err
	}
	if user.Labels != nil && user.Labels["authz.management.cattle.io/bootstrapping"] == "admin-user" {
		return nil
	}
	err = h.ensureUserNamespace(userID)
	if err != nil {
		return err
	}
	// get harbor auth secret
	harborAuthSecretName := fmt.Sprintf("%s-harbor", userID)
	authSecret, err := h.SecretClient.GetNamespaced(userID, harborAuthSecretName, v1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// check history data
			if userAuth, ok := user.Annotations[HarborUserAnnotationAuth]; ok {
				decodeAuthValue, err := base64.StdEncoding.DecodeString(userAuth)
				if err != nil {
					return err
				}
				harborAuthEncodeValue = string(decodeAuthValue)
			}
			// create auth secret
			_, err := h.SecretClient.Create(&apicorev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{
						HarborUserAuthSecretLabel: "true",
					},
					Name:      harborAuthSecretName,
					Namespace: userID,
				},
				Data: map[string][]byte{
					HarborUserSecretKey: []byte(harborAuthEncodeValue),
				},
				Type: apicorev1.SecretTypeOpaque,
			})
			if err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			// remove annotations and auto sync user
			if _, ok := user.Annotations[HarborUserAnnotationAuth]; ok {
				updateUser := user.DeepCopy()
				delete(updateUser.Annotations, HarborUserAnnotationAuth)
				updateUser.Annotations[HarborUserAnnotationSyncComplete] = "true"
				_, err = h.UserClient.Update(updateUser)
				if err != nil && !apierrors.IsConflict(err) {
					logrus.Errorf("remove harbor user annotation error: %v", err)
				}
			}

			return nil
		}
		return err
	}

	// check whether need update auth value
	if needCheckAuthChange {
		if authSecret.Data != nil && !strings.EqualFold(string(authSecret.Data[HarborUserSecretKey]), harborAuthEncodeValue) {
			updateSecret := authSecret.DeepCopy()
			updateSecret.Data[HarborUserSecretKey] = []byte(harborAuthEncodeValue)
			_, err := h.SecretClient.Update(updateSecret)
			if err != nil && !apierrors.IsConflict(err) {
				return err
			}
		}
	}

	return nil
}

func (h *Handler) ensureUserNamespace(ns string) error {
	_, err := h.NamespaceClient.Get(ns, v1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err = h.NamespaceClient.Create(&apicorev1.Namespace{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						"management.cattle.io/system-namespace": "true",
					},
					Name: ns,
				},
			})
			if err != nil && !apierrors.IsAlreadyExists(err) {
				logrus.Errorf("User namespace is not exist, create failed: %v", err)
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (h *Handler) harborAuthValidation(harborServer, auth string, harborVersion string) ([]byte, error) {
	// If the version is 2.0 change request address
	versionPath := h.harborVersionChangeToPath(harborVersion)

	result, statusCode, err := h.serveHarbor("GET", fmt.Sprintf("%s/%s/users/current", harborServer, versionPath), fmt.Sprintf("Basic %s", auth), nil)
	if err != nil {
		logrus.Errorf("haborAuthValidation failed with code %d, error %v", statusCode, err)
		// to deal with empty message from harbor api
		message := strings.TrimSpace(string(result))
		if len(message) == 0 {
			message = fmt.Sprintf("{\"code\":%d}", statusCode)
		}
		return nil, httperror.NewAPIError(httperror.ErrorCode{
			Code:   "SyncHarborFailed",
			Status: http.StatusGone,
		}, message)
	}
	return result, nil
}

func (h *Handler) ensureHarborAuthMode() error {
	harborAuthMode := settings.HarborAuthMode.Get()
	if harborAuthMode == "" {
		harborServer := settings.HarborServerURL.Get()
		harborVersion := settings.HarborVersion.Get()
		versionPath := h.harborVersionChangeToPath(harborVersion)
		result, _, err := h.serveHarbor("GET", fmt.Sprintf("%s/%s/systeminfo", harborServer, versionPath), "", nil)
		if err != nil {
			return err
		}
		harborSystemInfo := map[string]interface{}{}
		err = json.Unmarshal(result, &harborSystemInfo)
		if err != nil {
			return err
		}
		if authMode, ok := harborSystemInfo["auth_mode"].(string); ok {
			return settings.HarborAuthMode.Set(authMode)
		}
	}
	return nil
}

func (h *Handler) ensureUserSyncComplete(userID, harborAuthMode, harborEmail string) error {
	// update user annotation with email and set sync complete
	user, err := h.UserClient.Get(userID, v1.GetOptions{})
	if err != nil {
		return err
	}
	updateUser := user.DeepCopy()
	if strings.EqualFold(harborAuthMode, HarborLocalMode) {
		updateUser.Annotations[HarborUserAnnotationEmail] = harborEmail
	}
	if hasSync, ok := updateUser.Annotations[HarborUserAnnotationSyncComplete]; !ok || hasSync == "false" {
		updateUser.Annotations[HarborUserAnnotationSyncComplete] = "true"
	}
	if !reflect.DeepEqual(user, updateUser) {
		_, err = h.UserClient.Update(updateUser)
		if err != nil && !apierrors.IsConflict(err) {
			logrus.Errorf("update user with harbor email error: %v", err)
			return err
		}
	}
	return nil
}

func decryptPassword(request *types.APIContext, password string) (string, error) {
	if parse.IsBrowser(request.Request, false) && !strings.EqualFold(settings.DisablePasswordEncrypt.Get(), "true") {
		cookie, err := request.Request.Cookie("CSRF")
		if err == http.ErrNoCookie {
			logrus.Error("Can not get descrypt key for user password")
			return password, err
		}
		if password != "" {
			aesd := aesutil.New()
			pass, err := aesd.DecryptBytes(cookie.Value, []byte(password))
			if err != nil {
				logrus.Errorf("Descrypt user password error: %v", err)
				return password, err
			}
			if len(pass) == 0 {
				logrus.Errorf("Descrypt user password error, decrypted pass length is 0")
				return password, errors.New("Descrypt user password error, decrypted pass length is 0")
			}
			return string(pass), nil
		}
	}
	return password, nil
}

func (h *Handler) createNewHarborUser(harborAdminAuth, harborUserName, harborUserAuthPwd, harborEmail, harborServer string, harborVersion string) (int, error) {
	versionPath := h.harborVersionChangeToPath(harborVersion)
	adminAuth := base64.StdEncoding.EncodeToString([]byte(harborAdminAuth))
	adminAuthHeader := fmt.Sprintf("Basic %s", adminAuth)
	// create a new one
	harborUser := &HarborUser{
		UserName:     harborUserName,
		Password:     harborUserAuthPwd,
		Email:        harborEmail,
		RealName:     harborUserName,
		Deleted:      false,
		HasAdminRole: false,
	}
	postUser, err := json.Marshal(harborUser)
	if err != nil {
		return http.StatusBadRequest, httperror.NewAPIError(httperror.ErrorCode{
			Code:   "SyncHarborFailed",
			Status: http.StatusBadRequest,
		}, err.Error())
	}

	result, statusCode, err := h.serveHarbor("POST", fmt.Sprintf("%s/%s/users", harborServer, versionPath), adminAuthHeader, postUser)
	// if return 409 means create conflict
	if err != nil && statusCode != http.StatusConflict {
		return statusCode, httperror.NewAPIError(httperror.ErrorCode{
			Code:   "SyncHarborFailed",
			Status: http.StatusGone,
		}, err.Error())
	}

	// if return 409 and username has already exist, will use current auth to validate
	if statusCode == http.StatusConflict {
		// to deal with harbor previous api with different result body
		if !h.isHarborUserNameAlreadyExist(result, harborVersion) {
			return statusCode, httperror.NewAPIError(httperror.ErrorCode{
				Code:   "SyncHarborFailed",
				Status: http.StatusGone,
			}, string(result))
		}
	}
	return statusCode, nil
}

func (h Handler) harborVersionChangeToPath(harborVersion string) string {
	if harborVersion == HarborVersion2 {
		return "/api/v2.0"
	}

	return "/api"
}

// isHarborUserNameAlreadyExist Determine whether the user name is repeated from the error message
func (h Handler) isHarborUserNameAlreadyExist(result []byte, harborVersion string) bool {
	var apiMessage string
	// Different version correspond to different error struct
	apiErrorV2 := HarborAPIErrorV2{}
	apiError := HarborAPIError{}

	if harborVersion == HarborVersion2 {
		e := json.Unmarshal(result, &apiErrorV2)
		if e != nil {
			apiMessage = string(result)
		} else if len(apiErrorV2.Errors) > 0 {
			apiMessage = apiErrorV2.Errors[0].Message
		}
	} else {
		e := json.Unmarshal(result, &apiError)
		if e != nil {
			apiMessage = string(result)
		} else {
			apiMessage = apiError.Message
		}
	}

	return strings.Contains(apiMessage, "username has already")
}
