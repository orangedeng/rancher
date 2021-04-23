package app

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/rancher/rancher/pkg/controllers/management/drivers/kontainerdriver"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func addKontainerDrivers(management *config.ManagementContext) error {
	// create binary drop location if not exists
	err := os.MkdirAll(kontainerdriver.DriverDir, 0777)
	if err != nil {
		return fmt.Errorf("error creating binary drop folder: %v", err)
	}

	creator := driverCreator{
		driversLister: management.Management.KontainerDrivers("").Controller().Lister(),
		drivers:       management.Management.KontainerDrivers(""),
	}

	if err := cleanupImportDriver(creator); err != nil {
		return err
	}

	if err := creator.add("rancherKubernetesEngine"); err != nil {
		return err
	}

	if err := creator.add("googleKubernetesEngine"); err != nil {
		return err
	}

	if err := creator.add("azureKubernetesService"); err != nil {
		return err
	}

	if err := creator.add("amazonElasticContainerService"); err != nil {
		return err
	}

	customActive := true
	if dl := os.Getenv("CATTLE_DEV_MODE"); dl != "" {
		customActive = false
	}

	if err := creator.addCustomDriver(
		"baiducloudcontainerengine",
		"https://localhost/assets/engine-drivers/kontainer-engine-driver-baidu-linux",
		"4613e3be3ae5487b0e21dfa761b95de2144f80f98bf76847411e5fcada343d5e",
		"https://drivers.rancher.cn/kontainer-engine-driver-baidu/0.2.0/component.js",
		false,
		"drivers.rancher.cn", "*.baidubce.com",
	); err != nil {
		return err
	}

	if err := creator.addCustomDriver(
		"aliyunkubernetescontainerservice",
		"https://localhost/assets/engine-drivers/kontainer-engine-driver-aliyun-linux",
		"",
		"/assets/rancher-ui-driver-aliyun/component.js",
		customActive,
		"*.aliyuncs.com",
	); err != nil {
		return err
	}

	if err := creator.addCustomDriver(
		"tencentkubernetesengine",
		"https://localhost/assets/engine-drivers/kontainer-engine-driver-tencent-linux",
		"",
		"/assets/rancher-ui-driver-tencent/component.js",
		customActive,
		"*.tencentcloudapi.com", "*.qcloud.com",
	); err != nil {
		return err
	}

	if err := creator.addCustomDriver(
		"huaweicontainercloudengine",
		"https://localhost/assets/engine-drivers/kontainer-engine-driver-huawei-linux",
		"",
		"/assets/rancher-ui-driver-huawei/component.js",
		customActive,
		"*.myhuaweicloud.com",
	); err != nil {
		return err
	}
	if err := creator.addCustomDriver(
		"oraclecontainerengine",
		"https://github.com/rancher-plugins/kontainer-engine-driver-oke/releases/download/v1.5.2/kontainer-engine-driver-oke-linux",
		"7c43b1e5af81670bcb6204301e6d17db3bdf2890d0fe8b18d1be99f645ca1bc9",
		"",
		false,
		"*.oraclecloud.com",
	); err != nil {
		return err
	}

	return nil
}

func cleanupImportDriver(creator driverCreator) error {
	var err error
	if _, err = creator.driversLister.Get("", "import"); err == nil {
		err = creator.drivers.Delete("import", &v1.DeleteOptions{})
	}

	if !errors.IsNotFound(err) {
		return err
	}

	return nil
}

type driverCreator struct {
	driversLister v3.KontainerDriverLister
	drivers       v3.KontainerDriverInterface
}

func (c *driverCreator) add(name string) error {
	logrus.Infof("adding kontainer driver %v", name)

	driver, err := c.driversLister.Get("", name)
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = c.drivers.Create(&v3.KontainerDriver{
				ObjectMeta: v1.ObjectMeta{
					Name:      strings.ToLower(name),
					Namespace: "",
				},
				Spec: v3.KontainerDriverSpec{
					URL:     "",
					BuiltIn: true,
					Active:  true,
				},
				Status: v3.KontainerDriverStatus{
					DisplayName: name,
				},
			})
			if err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("error creating driver: %v", err)
			}
		} else {
			return fmt.Errorf("error getting driver: %v", err)
		}
	} else {
		driver.Spec.URL = ""

		_, err = c.drivers.Update(driver)
		if err != nil {
			return fmt.Errorf("error updating driver: %v", err)
		}
	}

	return nil
}

func (c *driverCreator) addCustomDriver(name, url, checksum, uiURL string, active bool, domains ...string) error {
	if runtime.GOARCH != "amd64" {
		logrus.Infof("skipping kontainer driver %v as the Arch is %s", name, runtime.GOARCH)
		return nil
	}
	logrus.Infof("adding kontainer driver %v", name)
	_, err := c.driversLister.Get("", name)
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = c.drivers.Create(&v3.KontainerDriver{
				ObjectMeta: v1.ObjectMeta{
					Name: strings.ToLower(name),
				},
				Spec: v3.KontainerDriverSpec{
					URL:              url,
					BuiltIn:          false,
					Active:           active,
					Checksum:         checksum,
					UIURL:            uiURL,
					WhitelistDomains: domains,
				},
				Status: v3.KontainerDriverStatus{
					DisplayName: name,
				},
			})
			if err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("error creating driver: %v", err)
			}
		} else {
			return fmt.Errorf("error getting driver: %v", err)
		}
	}
	return nil
}
