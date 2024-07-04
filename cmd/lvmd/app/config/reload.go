package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var ErrConfigModified = fmt.Errorf("configuration file has been modified and context needs to reload")

// ForceReloadTickerInterval is the interval at which the configuration file is checked for changes at a minimum.
// This is to ensure that the configuration file is reloaded even if the file watcher fails to detect changes.
const ForceReloadTickerInterval = 120 * time.Second

// LoadAndCancelOnReload behaves like Load, but also watches the directory of the configuration file and cancels the context.
func LoadAndCancelOnReload(ctx context.Context, cfgFilePath string) (context.Context, error) {
	if err := Load(ctx, cfgFilePath); err != nil {
		return ctx, err
	}

	dir := filepath.Dir(cfgFilePath)
	if fi, err := os.Stat(dir); err != nil {
		return ctx, err
	} else if !fi.IsDir() {
		return ctx, fmt.Errorf("%s is not a directory", dir)
	}

	// create a new context that will be canceled if the configuration file is modified and returned
	ctx, cancelWithCause := context.WithCancelCause(ctx)
	// setup is used to signal that the watcher has been set up correctly
	setup := make(chan struct{}, 1)
	// start watching the configuration file
	go watchAndCancelOnConfFileChange(ctx, cancelWithCause, setup, cfgFilePath)

	// wait for the watcher to be set up correctly until the context is canceled
	for {
		select {
		case <-ctx.Done():
			return ctx, ctx.Err()
		case <-setup:
			return ctx, nil
		}
	}
}

// watchAndCancelOnConfFileChange watches the configuration file for changes
// and cancels the context if the file is modified.
// It also reloads the configuration file at a set ForceReloadTickerInterval
// to ensure that the configuration is reloaded even if the file watcher fails to detect changes.
// Depending on the event, the context is canceled with a specific error message.
// The context is also canceled if the configuration file is removed or renamed.
func watchAndCancelOnConfFileChange(
	ctx context.Context,
	cancelWithCause context.CancelCauseFunc,
	watcherSetup chan struct{},
	file string,
) {
	dir := filepath.Dir(file)

	watcher, err := fsnotify.NewWatcher()
	defer func() {
		if err := watcher.Close(); err != nil {
			log.FromContext(ctx).Error(err, "failed to close file watcher")
		}
	}()
	if err != nil {
		cancelWithCause(fmt.Errorf("failed to create watcher: %w", err))
		return
	}

	if err := watcher.Add(dir); err != nil {
		cancelWithCause(fmt.Errorf("failed to watch directory %s: %w", dir, err))
		return
	}

	logger := log.FromContext(ctx).WithValues("file", file)
	logger.Info("watching configuration file for changes")

	ticker := time.NewTicker(ForceReloadTickerInterval)
	defer ticker.Stop()

	// signal that the watcher has been set up
	watcherSetup <- struct{}{}

	for {
		select {
		case <-ctx.Done():
			logger.Info("context has been canceled, stop watching configuration file")
			return
		case <-ticker.C:
			logger.Info("looking for updated configuration file after set interval", "interval", ForceReloadTickerInterval)
			if err := Load(ctx, file); err != nil {
				logger.Error(err, "failed to reload configuration file")
				cancelWithCause(err)
				return
			}
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Name != file {
				continue
			}
			if event.Has(fsnotify.Remove) {
				cancelWithCause(fmt.Errorf("configuration file %s has been removed", file))
				return
			}
			if event.Has(fsnotify.Rename) {
				cancelWithCause(fmt.Errorf("configuration file %s has been renamed", file))
				return
			}
			if event.Has(fsnotify.Chmod) {
				logger.Info("configuration file permission has been changed, this can lead to unexpected behaviors")
			}
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
				if err := Load(ctx, file); err != nil {
					logger.Error(err, "failed to reload configuration file")
					cancelWithCause(err)
					return
				}
				cancelWithCause(fmt.Errorf("%s was %s: %w", file, map[fsnotify.Op]string{
					fsnotify.Create: "created",
					fsnotify.Write:  "modified",
				}, ErrConfigModified))
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Error(err, "error while watching configuration file")
			cancelWithCause(err)
		}
	}
}
