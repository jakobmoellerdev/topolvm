package k8s

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/topolvm/topolvm"
	topolvmlegacyv1 "github.com/topolvm/topolvm/api/legacy/v1"
	topolvmv1 "github.com/topolvm/topolvm/api/v1"
	clientwrapper "github.com/topolvm/topolvm/internal/client"
	"github.com/topolvm/topolvm/internal/getter"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// ErrVolumeNotFound represents the specified volume is not found.
var ErrVolumeNotFound = errors.New("VolumeID is not found")

// LogicalVolumeService represents service for LogicalVolume.
// This is not concurrent safe, must take lock on caller.
type LogicalVolumeService struct {
	writer interface {
		client.Writer
		client.StatusClient
	}
	getter       getter.Interface
	volumeGetter *volumeGetter
}

const (
	indexFieldVolumeID = "status.volumeID"
)

var (
	logger = ctrl.Log.WithName("LogicalVolume")
)

type retryMissingGetter struct {
	cacheReader client.Reader
	apiReader   client.Reader
	getter      getter.Interface
}

func newRetryMissingGetter(cacheReader client.Reader, apiReader client.Reader) *retryMissingGetter {
	return &retryMissingGetter{
		cacheReader: cacheReader,
		apiReader:   apiReader,
		getter:      getter.NewRetryMissingGetter(cacheReader, apiReader),
	}
}

func (r *retryMissingGetter) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	var lv *topolvmv1.LogicalVolume
	var ok bool
	if lv, ok = obj.(*topolvmv1.LogicalVolume); !ok {
		return r.getter.Get(ctx, key, obj)
	}

	err := r.cacheReader.Get(ctx, key, lv)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.apiReader.Get(ctx, key, lv)
}

// This type is a safe guard to prohibit calling List from LogicalVolumeService directly.
type volumeGetter struct {
	cacheReader client.Reader
	apiReader   client.Reader
}

// Get returns LogicalVolume by volume ID.
// This ensures read-after-create consistency.
func (v *volumeGetter) Get(ctx context.Context, volumeID string) (*topolvmv1.LogicalVolume, error) {
	lvList := new(topolvmv1.LogicalVolumeList)
	err := v.cacheReader.List(ctx, lvList, client.MatchingFields{indexFieldVolumeID: volumeID})
	if err != nil {
		return nil, err
	}

	if len(lvList.Items) > 1 {
		return nil, fmt.Errorf("multiple LogicalVolume is found for VolumeID %s", volumeID)
	} else if len(lvList.Items) != 0 {
		return &lvList.Items[0], nil
	}

	// not found. try direct reader.
	err = v.apiReader.List(ctx, lvList)
	if err != nil {
		return nil, err
	}

	count := 0
	var foundLv *topolvmv1.LogicalVolume
	for _, lv := range lvList.Items {
		if lv.Status.VolumeID == volumeID {
			count++
			lv := lv
			foundLv = &lv
		}
	}
	if count > 1 {
		return nil, fmt.Errorf("multiple LogicalVolume is found for VolumeID %s", volumeID)
	}
	if foundLv == nil {
		return nil, ErrVolumeNotFound
	}
	return foundLv, nil
}

//+kubebuilder:rbac:groups=topolvm.io,resources=logicalvolumes,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch

// NewLogicalVolumeService returns LogicalVolumeService.
func NewLogicalVolumeService(mgr manager.Manager) (*LogicalVolumeService, error) {
	ctx := context.Background()
	if topolvm.UseLegacy() {
		err := mgr.GetFieldIndexer().IndexField(ctx, &topolvmlegacyv1.LogicalVolume{}, indexFieldVolumeID, func(o client.Object) []string {
			return []string{o.(*topolvmlegacyv1.LogicalVolume).Status.VolumeID}
		})
		if err != nil {
			return nil, err
		}
	} else {
		err := mgr.GetFieldIndexer().IndexField(ctx, &topolvmv1.LogicalVolume{}, indexFieldVolumeID, func(o client.Object) []string {
			return []string{o.(*topolvmv1.LogicalVolume).Status.VolumeID}
		})
		if err != nil {
			return nil, err
		}
	}

	client := clientwrapper.NewWrappedClient(mgr.GetClient())
	apiReader := clientwrapper.NewWrappedReader(mgr.GetAPIReader(), mgr.GetClient().Scheme())
	return &LogicalVolumeService{
		writer:       client,
		getter:       newRetryMissingGetter(client, apiReader),
		volumeGetter: &volumeGetter{cacheReader: client, apiReader: apiReader},
	}, nil
}

// CreateVolume creates volume
func (s *LogicalVolumeService) CreateVolume(ctx context.Context, node, dc, oc, name, sourceName string, requestBytes int64) (string, error) {
	logger.Info("k8s.CreateVolume called", "name", name, "node", node, "size", requestBytes, "sourceName", sourceName)
	var lv *topolvmv1.LogicalVolume
	// if the create volume request has no source, proceed with regular lv creation.
	if sourceName == "" {
		lv = &topolvmv1.LogicalVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: topolvmv1.LogicalVolumeSpec{
				Name:                name,
				NodeName:            node,
				DeviceClass:         dc,
				LvcreateOptionClass: oc,
				Size:                *resource.NewQuantity(requestBytes, resource.BinarySI),
			},
		}

	} else {
		// On the other hand, if a volume has a datasource, create a thin snapshot of the source volume with READ-WRITE access.
		lv = &topolvmv1.LogicalVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: topolvmv1.LogicalVolumeSpec{
				Name:                name,
				NodeName:            node,
				DeviceClass:         dc,
				LvcreateOptionClass: oc,
				Size:                *resource.NewQuantity(requestBytes, resource.BinarySI),
				Source:              sourceName,
				AccessType:          "rw",
			},
		}
	}

	existingLV := new(topolvmv1.LogicalVolume)
	err := s.getter.Get(ctx, client.ObjectKey{Name: name}, existingLV)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return "", err
		}

		err := s.writer.Create(ctx, lv)
		if err != nil {
			return "", err
		}
		logger.Info("created LogicalVolume CR", "name", name, "sourceID", lv.Spec.Source)
	} else {
		// LV with same name was found; check compatibility
		// skip check of capabilities because (1) we allow both of two access types, and (2) we allow only one access mode
		// for ease of comparison, sizes are compared strictly, not by compatibility of ranges
		if !existingLV.IsCompatibleWith(lv) {
			return "", status.Error(codes.AlreadyExists, "Incompatible LogicalVolume already exists")
		}
		// compatible LV was found
	}
	volumeID, err := s.waitForStatusUpdate(ctx, name)
	if err != nil {
		return "", err
	}

	return volumeID, nil
}

// DeleteVolume deletes volume
func (s *LogicalVolumeService) DeleteVolume(ctx context.Context, volumeID string) error {
	logger.Info("k8s.DeleteVolume called", "volumeID", volumeID)

	lv, err := s.GetVolume(ctx, volumeID)
	if err != nil {
		if errors.Is(err, ErrVolumeNotFound) {
			logger.Info("volume is not found", "volume_id", volumeID)
			return nil
		}
		return err
	}

	err = s.writer.Delete(ctx, lv)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// wait until delete the target volume
	return wait.ExponentialBackoffWithContext(ctx,
		wait.Backoff{
			Duration: 100 * time.Millisecond, // initial backoff
			Factor:   2,                      // factor for duration increase
			Jitter:   0.1,
			Steps:    math.MaxInt, // run for infinity; we assume context gets canceled
		}, func(ctx context.Context) (bool, error) {
			if err := s.getter.Get(ctx, client.ObjectKey{Name: lv.Name}, new(topolvmv1.LogicalVolume)); err != nil {
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				logger.Error(err, "failed to get LogicalVolume", "name", lv.Name)
				return false, nil
			}
			logger.Info("waiting for LogicalVolume to be deleted", "name", lv.Name)
			return false, nil
		})
}

// CreateSnapshot creates a snapshot of existing volume.
func (s *LogicalVolumeService) CreateSnapshot(ctx context.Context, node, dc, sourceVol, sname, accessType string, snapSize resource.Quantity) (string, error) {
	logger.Info("CreateSnapshot called", "name", sname)
	snapshotLV := &topolvmv1.LogicalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: sname,
		},
		Spec: topolvmv1.LogicalVolumeSpec{
			Name:        sname,
			NodeName:    node,
			DeviceClass: dc,
			Size:        snapSize,
			Source:      sourceVol,
			AccessType:  accessType,
		},
	}

	existingSnapshot := new(topolvmv1.LogicalVolume)
	err := s.getter.Get(ctx, client.ObjectKey{Name: sname}, existingSnapshot)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return "", err
		}
		err := s.writer.Create(ctx, snapshotLV)
		if err != nil {
			return "", err
		}
		logger.Info("created LogicalVolume CR", "name", sname, "source", snapshotLV.Spec.Source, "accessType", snapshotLV.Spec.AccessType)
	} else {
		if !existingSnapshot.IsCompatibleWith(snapshotLV) {
			return "", status.Error(codes.AlreadyExists, "Incompatible LogicalVolume already exists")
		}
	}

	volumeID, err := s.waitForStatusUpdate(ctx, sname)
	if err != nil {
		return "", err
	}

	return volumeID, nil
}

// ExpandVolume expands volume
func (s *LogicalVolumeService) ExpandVolume(ctx context.Context, volumeID string, requestBytes int64) error {
	logger := logger.WithValues("volume_id", volumeID, "size", requestBytes)
	logger.Info("k8s.ExpandVolume called")
	request := resource.NewQuantity(requestBytes, resource.BinarySI)

	lv, err := s.GetVolume(ctx, volumeID)
	if err != nil {
		return err
	}

	err = s.updateSpecSize(ctx, volumeID, request)
	if err != nil {
		return err
	}

	return wait.ExponentialBackoffWithContext(ctx, wait.Backoff{
		Duration: 1 * time.Second, // initial backoff
		Factor:   2,               // factor for duration increase
		Jitter:   0.1,
		Steps:    math.MaxInt, // run for infinity; we assume context gets canceled
	}, func(ctx context.Context) (bool, error) {
		var changedLV topolvmv1.LogicalVolume
		if err := s.getter.Get(ctx, client.ObjectKey{Name: lv.Name}, &changedLV); err != nil {
			logger.Error(err, "failed to get LogicalVolume", "name", lv.Name)
			return false, nil
		}

		if changedLV.Spec.Size.Cmp(*request) != 0 {
			logger.Info("waiting for update of 'spec.size' to propagate "+
				"to signal requested expansion", "name", lv.Name)
			return false, nil
		}

		if changedLV.Status.Code != codes.OK {
			return true, status.Error(changedLV.Status.Code, changedLV.Status.Message)
		}

		if changedLV.Status.CurrentSize == nil {
			logger.Info("waiting for update of 'status.currentSize' "+
				"to be filled initially", "name", lv.Name)
			// WA: since Status.CurrentSize is added in v0.4.0. it may be missing.
			// if the expansion is completed, it is filled, so wait for that.
			return false, nil
		}

		if changedLV.Status.CurrentSize.Value() != changedLV.Spec.Size.Value() {
			logger.Info("waiting for update of 'status.currentSize' to be updated to 'spec.currentSize' "+
				"to signal successful expansion", "name", lv.Name,
				"status.currentSize", changedLV.Status.CurrentSize, "spec.size", changedLV.Spec.Size)
			return false, nil
		}

		logger.Info("LogicalVolume successfully expanded")
		return true, nil
	})
}

// GetVolume returns LogicalVolume by volume ID.
func (s *LogicalVolumeService) GetVolume(ctx context.Context, volumeID string) (*topolvmv1.LogicalVolume, error) {
	return s.volumeGetter.Get(ctx, volumeID)
}

// updateSpecSize updates .Spec.Size of LogicalVolume.
func (s *LogicalVolumeService) updateSpecSize(ctx context.Context, volumeID string, size *resource.Quantity) error {
	return wait.ExponentialBackoffWithContext(ctx,
		retry.DefaultBackoff,
		func(ctx context.Context) (bool, error) {
			lv, err := s.GetVolume(ctx, volumeID)
			if err != nil {
				return true, err
			}
			lv.Spec.Size = *size
			if lv.Annotations == nil {
				lv.Annotations = make(map[string]string)
			}
			lv.Annotations[topolvm.GetResizeRequestedAtKey()] = time.Now().UTC().String()

			if err := s.writer.Update(ctx, lv); err != nil {
				if apierrors.IsConflict(err) {
					logger.Info("detected conflict when trying to update LogicalVolume spec", "name", lv.Name)
				} else {
					logger.Error(err, "failed to update LogicalVolume spec", "name", lv.Name)
				}
				return false, nil
			}
			return true, nil
		})
}

// waitForStatusUpdate waits for logical volume creation/failure/timeout, whichever comes first.
func (s *LogicalVolumeService) waitForStatusUpdate(ctx context.Context, name string) (string, error) {
	var volumeID string
	return volumeID, wait.ExponentialBackoffWithContext(ctx, wait.Backoff{
		Duration: 1 * time.Second, // initial backoff
		Factor:   2,               // factor for duration increase
		Jitter:   0.1,
		Steps:    math.MaxInt, // run for infinity; we assume context gets canceled
	}, func(ctx context.Context) (bool, error) {
		var newLV topolvmv1.LogicalVolume
		if err := s.getter.Get(ctx, client.ObjectKey{Name: name}, &newLV); err != nil {
			logger.Error(err, "failed to get LogicalVolume", "name", name)
			return false, nil
		}
		if newLV.Status.VolumeID != "" {
			logger.Info("LogicalVolume successfully provisioned", "volume_id", newLV.Status.VolumeID)
			volumeID = newLV.Status.VolumeID
			return true, nil
		}
		if newLV.Status.Code != codes.OK {
			err := s.writer.Delete(ctx, &newLV)
			if err != nil {
				// log this error but do not return this error, because newLV.Status.Message is more important
				logger.Error(err, "failed to delete LogicalVolume")
			}
			return true, status.Error(newLV.Status.Code, newLV.Status.Message)
		}
		return false, nil
	})
}
