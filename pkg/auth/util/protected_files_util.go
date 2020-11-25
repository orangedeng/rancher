package util

import (
	"os"
	"path/filepath"
)

const (
	protectedStaticPath = "/usr/share/rancher/protected-files"
)

func GetProtectedStaticDir() string {
	if dm := os.Getenv("CATTLE_DEV_MODE"); dm != "" {
		if dir, err := os.Getwd(); err == nil {
			// Pandaria: protected-files path `/yourworkplace/protected-files` in dev mode
			protectedFilesPath := filepath.Join(dir, "protected-files")
			os.MkdirAll(protectedFilesPath, 0700)

			return protectedFilesPath
		}
	}
	return protectedStaticPath
}
