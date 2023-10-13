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
