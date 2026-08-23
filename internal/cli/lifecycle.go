package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/adamraziv/dataporch/internal/config"
)

func (r *Runner) managerFor(cfg config.Config) (ServiceManager, error) {
	if r.dependencies.newServiceManager == nil {
		return nil, errors.New("background service unavailable; use dataporch run -f")
	}
	manager, err := r.dependencies.newServiceManager(cfg)
	if err != nil {
		return nil, fmt.Errorf("background service unavailable; use dataporch run -f: %w", err)
	}
	return manager, nil
}

func (r *Runner) validateInitialized(cfg config.Config) error {
	if r.dependencies.protectedFileValidator == nil {
		return errors.New("protected file validator is required")
	}
	for _, path := range []string{cfg.MasterKeyPath, cfg.SecretsStorePath} {
		if err := r.dependencies.protectedFileValidator(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return errors.New("DataPorch is not initialized; run dataporch secrets init")
			}
			return fmt.Errorf("DataPorch state is unsafe or corrupt: %w", err)
		}
	}
	return nil
}

func (r *Runner) definition(cfg config.Config) (ServiceDefinition, error) {
	if err := r.validateInitialized(cfg); err != nil {
		return ServiceDefinition{}, err
	}
	definition := ServiceDefinition{Executable: r.dependencies.invocationPath, Arguments: []string{runCommand, foregroundFlag}, Environment: serviceEnvironment(cfg), StdoutPath: filepath.Join(filepath.Dir(cfg.SecretsStorePath), "logs", "dataporch.log"), StderrPath: filepath.Join(filepath.Dir(cfg.SecretsStorePath), "logs", "dataporch.err.log")}
	return definition, validateDefinition(definition)
}

func (r *Runner) runBackground(ctx context.Context) error {
	cfg, err := loadConfig(r.dependencies)
	if err != nil {
		return err
	}
	manager, err := r.managerFor(cfg)
	if err != nil {
		return err
	}
	status, err := manager.Status(ctx)
	if err != nil {
		return fmt.Errorf("checking service status: %w", err)
	}
	if status.Registered && status.State == NativeRunning {
		if r.dependencies.healthChecker == nil {
			return errors.New("health checker is required")
		}
		return r.dependencies.healthChecker.Check(ctx, cfg.HTTPAddress)
	}
	definition, err := r.definition(cfg)
	if err != nil {
		return err
	}
	if err := manager.Register(ctx, definition); err != nil {
		return fmt.Errorf("registering service: %w", err)
	}
	if err := manager.Start(ctx); err != nil {
		return errors.Join(
			fmt.Errorf("starting service: %w", err),
			manager.Stop(ctx),
			manager.Unregister(ctx),
		)
	}
	if r.dependencies.healthChecker == nil {
		return errors.New("health checker is required")
	}
	if err := r.dependencies.healthChecker.Wait(ctx, cfg.HTTPAddress); err != nil {
		return errors.Join(fmt.Errorf("waiting for service health: %w", err), manager.Stop(ctx), manager.Unregister(ctx))
	}
	return nil
}

func (r *Runner) restartBackground(ctx context.Context) error {
	cfg, err := loadConfig(r.dependencies)
	if err != nil {
		return err
	}
	manager, err := r.managerFor(cfg)
	if err != nil {
		return err
	}
	status, err := manager.Status(ctx)
	if err != nil {
		return fmt.Errorf("checking service status: %w", err)
	}
	if !status.Registered {
		return errors.New("service is not registered; run dataporch run")
	}
	definition, err := r.definition(cfg)
	if err != nil {
		return err
	}
	if err := manager.Register(ctx, definition); err != nil {
		return fmt.Errorf("refreshing service definition: %w", err)
	}
	if err := manager.Restart(ctx); err != nil {
		return fmt.Errorf("restarting service: %w", err)
	}
	if r.dependencies.healthChecker == nil {
		return errors.New("health checker is required")
	}
	return r.dependencies.healthChecker.Wait(ctx, cfg.HTTPAddress)
}

func (r *Runner) stopBackground(ctx context.Context) error {
	cfg, err := loadConfig(r.dependencies)
	if err != nil {
		return err
	}
	manager, err := r.managerFor(cfg)
	if err != nil {
		return err
	}
	if err := manager.Stop(ctx); err != nil {
		return fmt.Errorf("stopping service: %w", err)
	}
	if err := manager.Unregister(ctx); err != nil {
		return fmt.Errorf("unregistering service: %w", err)
	}
	return nil
}

func (r *Runner) statusBackground(ctx context.Context) error {
	cfg, err := loadConfig(r.dependencies)
	if err != nil {
		return err
	}
	manager, err := r.managerFor(cfg)
	if err != nil {
		return err
	}
	status, err := manager.Status(ctx)
	if err != nil {
		if status.State == NativeFailed {
			return r.failedStatus(err)
		}
		return fmt.Errorf("checking service status: %w", err)
	}
	if !status.Registered || status.State == NativeStopped {
		if err := writeString(r.dependencies.stdout, "stopped\n"); err != nil {
			return err
		}
		return stoppedResult()
	}
	if status.State != NativeRunning {
		return r.failedStatus(nil)
	}
	if r.dependencies.healthChecker == nil {
		return errors.New("health checker is required")
	}
	if err := r.dependencies.healthChecker.Check(ctx, cfg.HTTPAddress); err != nil {
		return r.failedStatus(err)
	}
	return writeString(r.dependencies.stdout, fmt.Sprintf("running\npid: %d\naddress: %s\nlogs: %s\n", status.PID, cfg.HTTPAddress, manager.LogLocation()))
}

func (r *Runner) failedStatus(cause error) error {
	if err := writeString(r.dependencies.stdout, "failed\n"); err != nil {
		return err
	}
	if cause == nil {
		return errors.New("service is failed")
	}
	return fmt.Errorf("service is failed: %w", cause)
}
