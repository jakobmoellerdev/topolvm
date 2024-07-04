package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

var fileWatchReadyPollInterval = 10 * time.Millisecond

func TestLoadAndCancelOnReload(t *testing.T) {
	ctx := log.IntoContext(context.Background(), testr.New(t))
	testFile := filepath.Join(t.TempDir(), fmt.Sprintf("%s.lvmd.yaml", t.Name()))
	initialConfig := Get()

	if err := SaveFile(ctx, initialConfig, testFile); err != nil {
		t.Fatal(fmt.Errorf("failed to save initial test initialConfig: %w", err))
	}

	lctx, lcancel := context.WithCancel(ctx)
	defer lcancel()

	lctx, err := LoadAndCancelOnReload(lctx, testFile)
	if err != nil {
		t.Fatal(fmt.Errorf("failed to load and cancel on reload: %w", err))
	}

	if cfg := Get(); !reflect.DeepEqual(initialConfig, cfg) {
		t.Fatal("initialConfig and newConfig should be equal")
	}

	// Modify the file
	newSocketName := "new-socket-name"
	newConfig := initialConfig
	newConfig.SocketName = newSocketName
	if err := SaveFile(ctx, newConfig, testFile); err != nil {
		t.Fatal(fmt.Errorf("failed to save new test initialConfig: %w", err))
	}

	if err := wait.PollUntilContextTimeout(
		ctx,
		fileWatchReadyPollInterval,
		ForceReloadTickerInterval, // we dont want to test the force reload ticker here, so we wait at most this time
		true,
		func(ctx context.Context) (done bool, err error) {
			socketAfterUpdate := Get().SocketName
			lctxCorrectlyCancelled := errors.Is(context.Cause(lctx), ErrConfigModified)
			cfgCorrectlyUpdated := socketAfterUpdate == newSocketName
			return lctxCorrectlyCancelled && cfgCorrectlyUpdated, nil
		},
	); err != nil {
		t.Fatalf("Config should be updated by hot reload: %v", err)
	}

	// Cancel the context
	lcancel()

	newSocketName = "new-socket-name-2"
	postCancelConfig := initialConfig
	postCancelConfig.SocketName = newSocketName
	if err := SaveFile(ctx, postCancelConfig, testFile); err != nil {
		t.Fatal(fmt.Errorf("failed to save new config after context cancel: %w", err))
	}

	// Wait for the file to be modified
	time.Sleep(fileWatchReadyPollInterval)

	if cfg := Get(); cfg.SocketName == newSocketName {
		t.Fatal("Config should not be updated by hot reload after context cancel")
	}
}

func SaveFile(ctx context.Context, config Config, path string) error {
	configMutex.Lock()
	defer configMutex.Unlock()

	b, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, b, 0644); err != nil {
		return err
	}

	log.FromContext(ctx).Info("configuration file saved",
		"device_classes", config.DeviceClasses,
		"socket_name", config.SocketName,
		"file_name", path,
	)
	return nil
}
