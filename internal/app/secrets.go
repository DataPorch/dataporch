package app

import (
	"github.com/DataPorch/dataporch/internal/config"
	"github.com/DataPorch/dataporch/internal/secret/local"
)

func InitializeSecrets(cfg config.Config) error {
	return local.Init(local.Paths{
		KeyPath:   cfg.MasterKeyPath,
		StorePath: cfg.SecretsStorePath,
	})
}
