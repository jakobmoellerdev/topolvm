package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/topolvm/topolvm"
	"github.com/topolvm/topolvm/cmd/lvmd/app/config"
	"github.com/topolvm/topolvm/internal/lvmd"
	"github.com/topolvm/topolvm/internal/lvmd/command"
	"github.com/topolvm/topolvm/pkg/lvmd/proto"
	lvmdTypes "github.com/topolvm/topolvm/pkg/lvmd/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	cfgFilePath     string
	lvmPath         string
	hotReloadConfig bool
	zapOpts         zap.Options
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "lvmd",
	Version: topolvm.Version,
	Short:   "a gRPC service to manage LVM volumes",
	Long: `A gRPC service to manage LVM volumes.

lvmd handles a LVM volume group and provides gRPC API to manage logical
volumes in the volume group.

If command-line option "spare" is not zero, that value multiplied by 1 GiB
will be subtracted from the value lvmd reports as the free space of the
volume group.
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return subMain(cmd.Context())
	},
}

func subMain(cmdCtx context.Context) error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := log.FromContext(cmdCtx)

	command.SetLVMPath(lvmPath)

	// this is the actual context used for a lvmd instance
	// it may be cancelled if the configuration file is modified
	// and the services need to be restarted
	var ctx context.Context
	if hotReloadConfig {
		logger.Info("hot-reload enabled, watching configuration file for changes")
		var err error
		if ctx, err = config.LoadAndCancelOnReload(cmdCtx, cfgFilePath); err != nil {
			return err
		}
	} else {
		logger.Info("hot-reload disabled, loading configuration file once at startup into memory")
		ctx = cmdCtx
		if err := config.Load(ctx, cfgFilePath); err != nil {
			return err
		}
	}

	cfg := config.Get()

	if err := lvmd.ValidateDeviceClasses(cfg.DeviceClasses); err != nil {
		return err
	}

	vgs, err := command.ListVolumeGroups(ctx)
	if err != nil {
		logger.Error(err, "error while retrieving volume groups")
		return err
	}

	for _, dc := range cfg.DeviceClasses {
		vg, err := command.SearchVolumeGroupList(vgs, dc.VolumeGroup)
		if err != nil {
			logger.Error(err, "volume group not found", "volume_group", dc.VolumeGroup)
			return err
		}

		if dc.Type == lvmdTypes.TypeThin {
			_, err = vg.FindPool(ctx, dc.ThinPoolConfig.Name)
			if err != nil {
				logger.Error(err, "Thin pool not found:", "thinpool", dc.ThinPoolConfig.Name)
				return err
			}
		}
	}

	// UNIX domain socket file should be removed before listening.
	err = os.Remove(cfg.SocketName)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	lis, err := net.Listen("unix", cfg.SocketName)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer()
	dcm := lvmd.NewDeviceClassManager(cfg.DeviceClasses)
	ocm := lvmd.NewLvcreateOptionClassManager(cfg.LvcreateOptionClasses)
	vgService, notifier := lvmd.NewVGService(dcm)
	proto.RegisterVGServiceServer(grpcServer, vgService)
	proto.RegisterLVServiceServer(grpcServer, lvmd.NewLVService(dcm, ocm, notifier))
	grpc_health_v1.RegisterHealthServer(grpcServer, lvmd.NewHealthService())

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				grpcServer.GracefulStop()
				return
			case <-ticker.C:
				notifier()
			}
		}
	}()

	if err := grpcServer.Serve(lis); err != nil {
		return err
	}

	if errors.Is(context.Cause(ctx), config.ErrConfigModified) {
		// if the config was modified while running, restart the process
		// use the command context here as this wasn't cancelled
		return subMain(cmdCtx)
	} else if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error(err, "error while running lvmd, exiting abnormally")
		return err
	}
	return nil
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

//nolint:lll
func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFilePath, "config", filepath.Join("/etc", "topolvm", "lvmd.yaml"), "config file")
	rootCmd.PersistentFlags().StringVar(&lvmPath, "lvm-path", "", "lvm command path on the host OS")
	rootCmd.PersistentFlags().BoolVar(&hotReloadConfig, "hot-reload", false, "watch and reload configuration file dynamically")

	goflags := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(goflags)
	zapOpts.BindFlags(goflags)
}
