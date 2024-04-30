package v2

import (
	"context"
	"fmt"
	"time"

	"github.com/topolvm/topolvm/cmd/lvmd/app"
	lvmdTypes "github.com/topolvm/topolvm/pkg/lvmd/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func NewDeviceClassManager(c client.Client) *DeviceClassManager {
	return &DeviceClassManager{
		Client: c,
	}
}

type DeviceClassManager struct {
	client.Client
	managedDeviceClasses []*lvmdTypes.DeviceClass
}

func (m *DeviceClassManager) RefreshDeviceClasses(ctx context.Context, dcs []*lvmdTypes.DeviceClass) error {
	var configMapList corev1.ConfigMapList
	if err := m.List(ctx, &configMapList); err != nil {
		return fmt.Errorf("failed to discover config maps: %w", err)
	}

	configMapPresent := false
	var config app.Config
	var configMap corev1.ConfigMap
	for _, configMap = range configMapList.Items {
		lvmd, found := configMap.Data["lvmd.yaml"]
		if !found {
			continue
		}
		if err := yaml.Unmarshal([]byte(lvmd), config); err != nil {
			return fmt.Errorf("failed to unmarshal lvmd.yaml: %w", err)
		}
		configMapPresent = true
		break
	}
	if !configMapPresent {
		return fmt.Errorf("lvmd.yaml not discovered in any config map")
	}

	config.DeviceClasses = append(config.DeviceClasses, dcs...)
	newConfig, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal new config: %w", err)
	}
	configMap.Data["lvmd.yaml"] = string(newConfig)

	if err := m.Update(ctx, &configMap); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	nodePods := corev1.PodList{}
	if err := m.List(ctx, &nodePods, &client.ListOptions{LabelSelector: labels.SelectorFromSet(labels.Set{
		"app.kubernetes.io/component:": "lvmd",
	})}); err != nil {
		return fmt.Errorf("failed to list daemon sets: %w", err)
	} else if len(nodePods.Items) == 0 {
		return fmt.Errorf("no daemon set found to restart lvmd")
	}
	lvmdPod := nodePods.Items[0]
	if err := m.Delete(ctx, &lvmdPod); err != nil {
		return fmt.Errorf("failed to delete lvmd pod to trigger restart: %w", err)
	}

	if err := wait.PollUntilContextCancel(ctx, 1*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := m.Get(ctx, client.ObjectKeyFromObject(&lvmdPod), &lvmdPod); err != nil {
			return false, err
		}
		return lvmdPod.Status.Phase == corev1.PodRunning, nil
	}); err != nil {
		return fmt.Errorf("failed to wait for lvmd pod to restart: %w", err)
	}

	return nil
}
