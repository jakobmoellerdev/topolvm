# Starting LVMD from within topolvm-node

## Motivation

To run TopoLVM on Edge currently requires quite a lot of memory for a minimal installation. One of the main
resons for that is due to the lvmd container running as a separate daemonset from the topolvm-node daemonset.
The idea is to combine these two into a single daemonset to reduce the memory footprint as they are usually run
within the same pod anyway when lvmd is not running as a systemd service.

### Goals

- Unify the topolvm-node and lvmd containers into a single container.
- Allow lvmd to run as a systemd service or as a daemonset like before.
- Do not break existing installations.
- Reduce memory footprint.

## Proposal

TopoLVM is a storage plugin based on [CSI](https://github.com/container-storage-interface/spec/).
Therefore, the architecture basically follows the one described in
https://kubernetes-csi.github.io/docs/ .

To manage LVM, `lvmd` should be run as a system service of the node OS.
It provides gRPC services via UNIX domain socket to create/update/delete
LVM logical volumes and watch a volume group status.

`topolvm-node` implements CSI node services as well as miscellaneous control
on each Node.  It communicates with `lvmd` to watch changes in free space
of a volume group and exports the information by annotating Kubernetes
`Node` resource of the running node.  In the meantime, it adds a finalizer
to the `Node` to clean up PersistentVolumeClaims (PVC) bound on the node.

Since both topolvm-node and lvmd need to run as DaemonSet, we can simply combine them and start them under a single binary.

## Design Details

### Allowing topolvm-node to start lvmd

The main problem with running lvmd as a systemd service or daemonset is that it is not possible to start it from within the topolvm-node container.
We can fix this by allowing topolvm-node to start lvmd by compiling a service into its startup command.
This can be enabled via flag and then uses the following code to start lvmd:

```go
package lvmd

import (
	"context"

	"github.com/topolvm/topolvm/lvmd/proto"
	"google.golang.org/grpc"
)

// NewLocal creates a locally calling LVMD
func NewLocal(dcmapper *DeviceClassManager, ocmapper *LvcreateOptionClassManager) (
	proto.LVServiceServer,
	proto.LVServiceClient,
	proto.VGServiceServer,
	proto.VGServiceClient,
) {
	vgServiceServerInstance, notifier := NewVGService(dcmapper)
	lvServiceServerInstance := NewLVService(dcmapper, ocmapper, notifier)
	caller := &localCaller{
		lvServiceServer: lvServiceServerInstance,
		vgServiceServer: vgServiceServerInstance,
	}
	return caller.lvServiceServer, caller.lvServiceClient, caller.vgServiceServer, caller.vgServiceClient
}

type localCaller struct {
	lvServiceServer proto.LVServiceServer
	vgServiceServer proto.VGServiceServer
	lvServiceClient proto.LVServiceClient
	vgServiceClient proto.VGServiceClient
}

func (l *localCaller) CreateLV(ctx context.Context, in *proto.CreateLVRequest, opts ...grpc.CallOption) (*proto.CreateLVResponse, error) {
	return l.lvServiceServer.CreateLV(ctx, in)
}

func (l *localCaller) RemoveLV(ctx context.Context, in *proto.RemoveLVRequest, opts ...grpc.CallOption) (*proto.Empty, error) {
	return l.lvServiceServer.RemoveLV(ctx, in)
}

func (l *localCaller) ResizeLV(ctx context.Context, in *proto.ResizeLVRequest, opts ...grpc.CallOption) (*proto.Empty, error) {
	return l.lvServiceServer.ResizeLV(ctx, in)
}

func (l *localCaller) CreateLVSnapshot(ctx context.Context, in *proto.CreateLVSnapshotRequest, opts ...grpc.CallOption) (*proto.CreateLVSnapshotResponse, error) {
	return l.lvServiceServer.CreateLVSnapshot(ctx, in)
}

func (l *localCaller) GetLVList(ctx context.Context, in *proto.GetLVListRequest, opts ...grpc.CallOption) (*proto.GetLVListResponse, error) {
	return l.vgServiceServer.GetLVList(ctx, in)
}

func (l *localCaller) GetFreeBytes(ctx context.Context, in *proto.GetFreeBytesRequest, opts ...grpc.CallOption) (*proto.GetFreeBytesResponse, error) {
	return l.vgServiceServer.GetFreeBytes(ctx, in)
}
```

As one can see, this is a simple wrapper around the lvmd server that is used to call lvmd code behind the usual grpc server from within the topolvm-node service.

We can easily integrate it on `pkg/topolvm-node/cmd` by introducing the following flags:

```go
func init() {
	//...
	fs.BoolVar(&config.llvmd, "llvmd", false, "Runs LVMD localy")
	fs.StringVar(&cfgFilePath, "config", filepath.Join("/etc", "topolvm", "lvmd.yaml"), "config file")
	fs.BoolVar(&command.Containerized, "container", false, "Run within a container")
    //...

	fs.AddGoFlagSet(goflags)
}
```

After this, we can use the flags taken from the lvmd binary and start it within the command:

```go
package cmd

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/cybozu-go/log"
	"github.com/spf13/viper"
	"github.com/topolvm/topolvm"
	topolvmlegacyv1 "github.com/topolvm/topolvm/api/legacy/v1"
	topolvmv1 "github.com/topolvm/topolvm/api/v1"
	clientwrapper "github.com/topolvm/topolvm/client"
	"github.com/topolvm/topolvm/controllers"
	"github.com/topolvm/topolvm/driver"
	"github.com/topolvm/topolvm/lvmd"
	"github.com/topolvm/topolvm/lvmd/proto"
	"github.com/topolvm/topolvm/runners"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(topolvmv1.AddToScheme(scheme))
	utilruntime.Must(topolvmlegacyv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func subMain() error {
	nodename := viper.GetString("nodename")
	if len(nodename) == 0 {
		return errors.New("node name is not given")
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&config.zapOpts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: config.metricsAddr,
		LeaderElection:     false,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		return err
	}
	client := clientwrapper.NewWrappedClient(mgr.GetClient())
	apiReader := clientwrapper.NewWrappedReader(mgr.GetAPIReader(), mgr.GetClient().Scheme())

	var lvclnt proto.LVServiceClient
	var vgclnt proto.VGServiceClient

	if config.llvmd {
		if err := loadConfFile(cfgFilePath); err != nil {
			return err
		}
		dcm := lvmd.NewDeviceClassManager(config.lvmd.DeviceClasses)
		ocm := lvmd.NewLvcreateOptionClassManager(config.lvmd.LvcreateOptionClasses)
		_, lvclnt, _, vgclnt = lvmd.NewLocal(dcm, ocm)
	} else {
		dialer := &net.Dialer{}
		dialFunc := func(ctx context.Context, a string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", a)
		}
		conn, err := grpc.Dial(config.lvmdSocket, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(dialFunc))
		if err != nil {
			return err
		}
		defer conn.Close()
	}

	lvcontroller := controllers.NewLogicalVolumeReconcilerWithServices(client, nodename, vgclnt, lvclnt)
	if err := lvcontroller.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LogicalVolume")
		return err
	}
	//+kubebuilder:scaffold:builder

	// Add health checker to manager
	checker := runners.NewChecker(checkFunc(vgclnt, apiReader), 1*time.Minute)
	if err := mgr.Add(checker); err != nil {
		return err
	}

	// Add metrics exporter to manager.
	// Note that grpc.ClientConn can be shared with multiple stubs/services.
	// https://github.com/grpc/grpc-go/tree/master/examples/features/multiplex
	if err := mgr.Add(runners.NewMetricsExporter(vgclnt, client, nodename)); err != nil {
		return err
	}

	// Add gRPC server to manager.
	if err := os.MkdirAll(driver.DeviceDirectory, 0755); err != nil {
		return err
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(ErrorLoggingInterceptor))
	csi.RegisterIdentityServer(grpcServer, driver.NewIdentityServer(checker.Ready))
	nodeServer, err := driver.NewNodeServer(nodename, vgclnt, lvclnt, mgr)
	if err != nil {
		return err
	}
	csi.RegisterNodeServer(grpcServer, nodeServer)
	err = mgr.Add(runners.NewGRPCRunner(grpcServer, config.csiSocket, false))
	if err != nil {
		return err
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return err
	}

	return nil
}

//+kubebuilder:rbac:groups=storage.k8s.io,resources=csidrivers,verbs=get;list;watch

func checkFunc(clnt proto.VGServiceClient, r client.Reader) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, err := clnt.GetFreeBytes(ctx, &proto.GetFreeBytesRequest{DeviceClass: topolvm.DefaultDeviceClassName}); err != nil {
			return err
		}

		var drv storagev1.CSIDriver
		return r.Get(ctx, types.NamespacedName{Name: topolvm.GetPluginName()}, &drv)
	}
}

func ErrorLoggingInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	resp, err = handler(ctx, req)
	if err != nil {
		ctrl.Log.Error(err, "error on grpc call", "method", info.FullMethod)
	}
	return resp, err
}

func loadConfFile(cfgFilePath string) error {
	b, err := os.ReadFile(cfgFilePath)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(b, config.lvmd)
	if err != nil {
		return err
	}
	log.Info("configuration file loaded: ", map[string]interface{}{
		"device_classes": config.lvmd.DeviceClasses,
		"socket_name":    config.lvmd.SocketName,
		"file_name":      cfgFilePath,
	})
	return nil
}

```

This will allow starting and using lvmd without grpc communication and another container.

## Limitations

- LVMD can no longer be deployed and managed separately from topolvm-node.

## Packaging and deployment

`lvmd` is no longer necessary as a binary and can completely ommitted in a deployment without lvmd as a systemd service.
All other components are unaffected, but topolvm-node needs to be started correctly if this should be exposed in the helm chart.
