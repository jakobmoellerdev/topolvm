package config

import (
	"context"
	"os"
	"sync"

	"github.com/topolvm/topolvm"
	lvmdTypes "github.com/topolvm/topolvm/pkg/lvmd/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// Config represents configuration parameters for lvmd
type Config struct {
	// SocketName is Unix domain socket name
	SocketName string `json:"socket-name"`
	// DeviceClasses is
	DeviceClasses []*lvmdTypes.DeviceClass `json:"device-classes"`
	// LvcreateOptionClasses are classes that define options for the lvcreate command
	LvcreateOptionClasses []*lvmdTypes.LvcreateOptionClass `json:"lvcreate-option-classes"`
}

var config = Config{
	SocketName: topolvm.DefaultLVMdSocket,
}
var configMutex sync.RWMutex

// Get returns the global Config struct in a thread-safe manner
func Get() Config {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return config
}

// Load reads the configuration file and stores the values in the global Config struct
// The configuration file is in YAML format and loading is thread-safe.
func Load(ctx context.Context, cfgFilePath string) error {
	b, err := os.ReadFile(cfgFilePath)
	if err != nil {
		return err
	}

	configMutex.Lock()
	defer configMutex.Unlock()
	if err = yaml.Unmarshal(b, &config); err != nil {
		return err
	}

	log.FromContext(ctx).Info("configuration file loaded",
		"device_classes", config.DeviceClasses,
		"socket_name", config.SocketName,
		"file_name", cfgFilePath,
	)
	return nil
}
