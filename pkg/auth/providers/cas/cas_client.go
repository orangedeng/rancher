package cas

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/rancher/norman/httperror"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	gocas "gopkg.in/cas.v2"
)

type Account struct {
	Username string `json:"username,omitempty"`
}

type Client struct {
	httpClient *http.Client
}

func NewCASClient() *Client {
	return &Client{
		httpClient: &http.Client{},
	}
}

func (c *Client) ServiceValidate(url string, timeoutMs int64, config *v3.CASConfig) (string, error) {
	logrus.Debugf("ServiceValidate: %s", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		logrus.Error(err)
		return "", err
	}

	c.httpClient.Timeout = time.Millisecond * time.Duration(timeoutMs)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		logrus.Errorf("Received error from cas/serviceValidate: %v , url: %s", err, url)
		return "", err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logrus.Error(err)
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Request failed, got status code: %d. Response: %s",
			resp.StatusCode, string(body))
	}

	username, err := parseCASValidateResponse(body)
	if err != nil {
		logrus.Errorf("parseCASValidateResponse failed %v %s", err, string(body))
		return "", httperror.NewAPIError(httperror.ServerError,
			fmt.Sprintf("Parse CAS ValidateResponse failed, got error: %v, \n access \n%s\n to logout",
				err,
				formCASLogoutURL(config)))
	}
	return username, nil
}

func parseCASValidateResponse(b []byte) (string, error) {
	res, err := gocas.ParseServiceResponse(b)
	if err != nil {
		return "", err
	}
	return res.User, nil
}
