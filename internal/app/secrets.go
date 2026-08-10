package app

import (
	"github.com/adamraziv/dataporch/internal/config"
	"github.com/adamraziv/dataporch/internal/secret/local"
)

func InitializeSecrets(cfg config.Config) error {
	return local.Init(local.Paths{
		KeyPath:   cfg.MasterKeyPath,
		StorePath: cfg.SecretsStorePath,
	})
}
