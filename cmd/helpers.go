package cmd

import (
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/michaelperel/docker-lock/registry"
	"github.com/michaelperel/docker-lock/registry/contrib"
	"github.com/michaelperel/docker-lock/registry/firstparty"
)

// defaultConfigPath returns the default location of docker's config.json
// for all platforms.
func defaultConfigPath() string {
	if homeDir, err := os.UserHomeDir(); err == nil {
		cPath := filepath.ToSlash(
			filepath.Join(homeDir, ".docker", "config.json"),
		)
		if _, err := os.Stat(cPath); err != nil {
			return ""
		}

		return cPath
	}

	return ""
}

// defaultWrapperManager returns a wrapper manager where the default
// wrapper is the first party's default wrapper (a wrapper for Docker Hub),
// and all other first party and contrib wrappers are added.
func defaultWrapperManager(
	configPath string,
	client *registry.HTTPClient,
) (*registry.WrapperManager, error) {
	dw, err := firstparty.DefaultWrapper(configPath, client)
	if err != nil {
		return nil, err
	}

	wm := registry.NewWrapperManager(dw)

	fpWrappers, err := firstparty.AllWrappers(configPath, client)
	if err != nil {
		return nil, err
	}

	cWrappers, err := contrib.AllWrappers(client)
	if err != nil {
		return nil, err
	}

	wm.Add(fpWrappers...)
	wm.Add(cWrappers...)

	return wm, nil
}

// loadEnv loads environment variables from a dot env file into the process.
// If the default path, ".env", is supplied and does not exist, no error
// occurs. If any other path is supplied and does not exist, an error occurs.
// If the dot env file exists, but cannot be parsed, an error occurs.
func loadEnv(p string) error {
	if err := godotenv.Load(p); err != nil {
		if p != ".env" {
			return err
		}
	}

	return nil
}
