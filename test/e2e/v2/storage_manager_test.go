package v2

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

const totalStorageLimit = 30 * 1024 * 1024 * 1024 // 30 GB in bytes

// StorageManager ensures we do not allocate more than the limit
type StorageManager struct {
	backingDirectory string

	storageMutex        sync.Mutex
	currentStorageUsage int64
	managedLoopDevices  map[string]managedLoopDevice
}

type managedLoopDevice struct {
	device string
	size   int64
}

func NewStorageManager(t testing.TB) *StorageManager {
	rm := &StorageManager{
		backingDirectory: filepath.Join(t.TempDir(), "topolvm-backing-files"),
	}
	t.Cleanup(func() {
		for id := range rm.managedLoopDevices {
			if err := rm.Release(id); err != nil {
				t.Errorf("failed to release loop device during cleanup %s: %v", id, err)
			}
		}
	})
	return rm
}

func (rm *StorageManager) Allocate(id string, size int64) (string, error) {
	rm.storageMutex.Lock()
	defer rm.storageMutex.Unlock()

	newUsage := rm.currentStorageUsage + size
	if newUsage > totalStorageLimit {
		return "", fmt.Errorf("allocating %v would exceed storage limit %v", totalStorageLimit)
	}

	file, err := rm.createBackingFile(id, size)
	if err != nil {
		return "", fmt.Errorf("failed to create backing file: %w", err)
	}

	loopDevice, err := rm.setupLoopDevice(file)
	if err != nil {
		if err := os.Remove(file); err != nil {
			return "", fmt.Errorf("failed to remove backing file after loop device setup failed: %w", err)
		}
		return "", fmt.Errorf("failed to setup loop device: %w", err)
	}

	rm.currentStorageUsage += size
	rm.managedLoopDevices[id] = managedLoopDevice{
		device: loopDevice,
		size:   size,
	}

	return loopDevice, nil
}

func (rm *StorageManager) Release(id string) error {
	rm.storageMutex.Lock()
	defer rm.storageMutex.Unlock()

	if err := rm.teardownLoopDevice(rm.managedLoopDevices[id].device); err != nil {
		return fmt.Errorf("failed to teardown loop device during release of %s: %w", id, err)
	}

	if err := os.Remove(rm.managedLoopDevices[id].device); err != nil {
		return fmt.Errorf("failed to remove backing file during release of %s: %w", id, err)
	}

	rm.currentStorageUsage -= rm.managedLoopDevices[id].size
	delete(rm.managedLoopDevices, id)

	return nil
}

func (rm *StorageManager) createBackingFile(id string, size int64) (string, error) {
	path := filepath.Join(rm.backingDirectory, id)

	cmd := exec.Command("fallocate", "-l", fmt.Sprintf("%dB", size), path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to allocate temporary backing file for %s with size %v: %s, error: %s", id, size, output, err)
	}

	return path, nil
}

func (rm *StorageManager) setupLoopDevice(path string) (string, error) {
	cmd := exec.Command("losetup", "--show", "-f", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w:%s", err, output)
	}

	return string(output), nil
}

func (rm *StorageManager) teardownLoopDevice(path string) error {
	cmd := exec.Command("losetup", "-d", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w:%s", err, output)
	}

	return nil
}
