# Supporting PVCs smaller than 1 GB in size.

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
- [Proposal](#proposal)
    - [Decision Outcome](#decision-outcome)
    - [Open Questions](#open-questions)
- [Design Details](#design-details)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
    - [Implementation Considerations](#implementation-considerations)
        - [removing `convertRequestCapacity` in `driver/controller`](#removing-convertrequestcapacity-in-drivercontroller)
        - [changing `LogicalVolumeService` value insertion](#changing-logicalvolumeservice-value-insertion)
        - [modifying `controllers/logicalvolume_controller` to create correct LVMD calls](#modifying-controllerslogicalvolumecontroller-to-create-correct-lvmd-calls)
        - [switching all occurrences of `uint64` to int64` in `lvmd` and change `size_gb` to `size` in protobuf messages.](#switching-all-occurrences-of-uint64-to-int64-in-lvmd-and-change-sizegb-to-size-in-protobuf-messages)
<!-- /toc -->

## Summary

Currently, TopoLVM lives with a design decision to have the smallest amount of storage be limited to 1 GB.
This is mainly due to various bit shifting and assumptions made in the code and not because of limitations in CSI, Kubernetes or LVM.
We want to get rid of this limitation.

## Motivation

For most users of TopoLVM, local nodes are the focus. 
While TopoLVM was designed to be run in datacenters where the lowest provisional unit might not be an issue, edge use cases require us to make the most of volumes.
To allow TopoLVM for proper use on edge devices and small form-factor use cases of Pods, we want to allow storage capacities that can supply storage lower than 1GB.

### Goals

- Ensuring backwards-compatibility with schedulers and legacy bit shifts that would be replaced
- Discovering all places with hardcoded bit shifts and transitioning them to a more modular and sizable approach
- Allowing creation of PVCs that request less than 1GB of storage, with a minimum size that is reasonably set.

## Proposal

**TLDR: Find all occurrences of `<<30` Bit shifts and `GB` values and replace them with _byte-level_ comparisons. Add LVMD `size` field, deprecating and replacing `size_gb`. Remove LVMD `uint64` bit shift dependance and replace it with `int64` comparisons.**

The main reason of hard coding 1GB as minimum size is the `<<30` bitshift.
Any occurrence of the infamous `30` bit shift needs to be identified and replaced with a more dynamic version.

In total the entire codebase contains 73 occurrences of `<<30` and 8 occurrences of `>>30` as of writing this proposal.
While it would not be completely trivial to replace, we believe this is still a reasonable amount to refactor with manual care.

The biggest impact on the code base would be introducing additional test cases if we decide to introduce full backwards compatibility by testing with size_gb and the newly introduced field that would replace the bitshift. 
We can severely reduce the amount of changes required by changing existing test cases to use the new field instead.

As one can see below we can simply get around the bitshift by using the full byte definition instead of using a scaled version of `gb`:

```go
package main

import (
	"fmt"

	. "k8s.io/apimachinery/pkg/api/resource"
)

func main() {
	var oldDefGi = int64(1)
	var newDefGi = MustParse(fmt.Sprintf("%vGi", oldDefGi))
	var someGiBytes = int64(1073741824)

	var oldDefMi = int64(500)
	var newDefMi = MustParse(fmt.Sprintf("%vMi", oldDefMi))
	var someMiBytes = int64(524288000)

	println(someGiBytes == oldDefGi<<30) // always true
	println(NewQuantity(someGiBytes, BinarySI).Cmp(newDefGi) == 0) // always true

	println(someMiBytes == oldDefMi<<30) // always false
	println(NewQuantity(someMiBytes, BinarySI).Cmp(newDefMi) == 0) // always true
}

```

At the same time we already make use of the resource Capacity in `LogicalVolume` for parsing into JSON or YAML so we do not have to break user-facing Kubernetes APIs.
Since all CSI Driver values already work with bytes, we have no trouble taking in the new data, we will just accept more ranges.


We will do 2 major changes to LVMD:
1. We will accept a breaking change in `lvmd` that moves from `size_gb` to a more flexible `size` when relating to request / response sizes.
2. We will move from `uint64` comparisons in `lvmd` to `int64` comparisons. This is the same level of precision as within CSI driver specifications.

Together with changes in LVMD we can easily replace all bitshifted comparisons with byte level comparisons based on `int64`.

Within tests, we will write a small helper function that easily allows defining `int64` for any amount of Gi that we previously used bitshifts for.

### Decision Outcome


### Open Questions

Under the assumption that the change is deemed useful or accepted, we still need to decide on the following points:

1. should `lvmd` probuf message set be considered `user-facing`? If so should we `reserve` or `deprecate` during the change?
2. should we duplicate test cases for <1GB volume sources, or adjust existing tests to cover this.

## Design Details

### Upgrade / Downgrade Strategy

An upgrade will be seamless and not cause any issues with inflight messages into lvmd.

A downgrade will work seamless as well with any component no matter the restarts or order of downgrade, since we have a nil check on size quantity and will fallback
to legacy size calculations instead of the new size_quantity.

### Deprecation / Removal

We can easily remove the legacy `size_gb` field by making use of `reserved` if we are sure there will be no future usages in the next release:

```protobuf

import "k8s.io/apimachinery/pkg/api/resource/resource.proto"; // contains copy of https://github.com/kubernetes/apimachinery/blob/master/pkg/api/resource/generated.proto:
// Represents the input for CreateLV.
message CreateLVRequest {
  string name = 1;              // The logical volume name.
  reserved 2;
  repeated string tags = 3;     // Tags to add to the volume during creation
  string device_class = 4;
  string lvcreate_option_class = 5;


  uint64 size = 6; // Volume size in bytes
  // k8s.io.apimachinery.pkg.api.resource.Quantity size_quantity = 6; // Volume size in canonical kubernetes represenation.
}
```

### Implementation Considerations

#### removing `convertRequestCapacity` in `driver/controller`

The first and probably the easiest option to rework the sizing constraints is to make use of the native sizing that is imposed by the CSI specification:

All values in CSI are coming from `req.GetCapacityRange().GetRequiredBytes()` and `req.GetCapacityRange().GetLimitBytes()` specifically which are both `int64`.

We currently convert all request capacity with the following method

```go
func convertRequestCapacity(requestBytes, limitBytes int64) (int64, error) {
	if requestBytes < 0 {
		return 0, errors.New("required capacity must not be negative")
	}
	if limitBytes < 0 {
		return 0, errors.New("capacity limit must not be negative")
	}

	if limitBytes != 0 && requestBytes > limitBytes {
		return 0, fmt.Errorf(
			"requested capacity exceeds limit capacity: request=%d limit=%d", requestBytes, limitBytes,
		)
	}

	if requestBytes == 0 {
		return 1, nil
	}
	return (requestBytes-1)>>30 + 1, nil
}
```

By replacing this method with a method that simply checks for limitBytes and and then returning the proper requestBytes, we can easily
push that value downstream.

We currently pass all these Volumes to the `LogicalVolumeService`.

#### changing `LogicalVolumeService` value insertion

TopoLVM sets bitshifted sizing for all current `LogicalVolumeSpec` objects when calling `CreateVolume`.

```go
// CreateVolume creates volume
func (s *LogicalVolumeService) CreateVolume(ctx context.Context, node, dc, oc, name, sourceName string, requestGb int64) (string, error) {
	logger.Info("k8s.CreateVolume called", "name", name, "node", node, "size_gb", requestGb, "sourceName", sourceName)
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
				Size:                *resource.NewQuantity(requestGb<<30, resource.BinarySI),
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
				Size:                *resource.NewQuantity(requestGb<<30, resource.BinarySI),
				Source:              sourceName,
				AccessType:          "rw",
			},
		}
	}
}
```

As one can see currently size, is always defined with `*resource.NewQuantity(requestGb<<30, resource.BinarySI)`, however this definition is wrong.

This specifications currently sets all logicalVolumeSpecs to have the wrong sizing. Instead it should have a scaled quantity `*resource.NewQuantity(requestSize, resource.BinarySI)`
after which all calls will work fine again.

#### modifying `controllers/logicalvolume_controller` to create correct LVMD calls

Since the main TopoLVM controllers already use `reqBytes := lv.Spec.Size.Value()` for their sizing the only thing necessary
is to change the calls of LVMD (protocol modifications below).

Calls will directly include the request / response bytes like so:

```go
resp, err := r.lvService.CreateLVSnapshot(ctx, &proto.CreateLVSnapshotRequest{
    Name:         string(lv.UID),
    DeviceClass:  lv.Spec.DeviceClass,
    SourceVolume: sourceVolID,
    Size:         reqBytes,
    AccessType:   lv.Spec.AccessType,
})
```

will have to have `SizeGb` replaced with `Size` and their raw values so we can get rid of the bitshift. This is only
possible by introducing a breaking change to LVMD protocol.

#### switching all occurrences of `uint64` to int64` in `lvmd` and change `size_gb` to `size` in protobuf messages.

Since lvmd currently includes various messages with a `uint64 size_gb` field, we should think of how to properly
serialize new the capacity information in a scalable way. Here we have an inbuilt option with the kubernetes quantities as well.

we could simply remove this limitation and pass requestBytes natively as its byte count into LVMD:

```protobuf
// Represents the input for CreateLV.
message CreateLVRequest {
  string name = 1;              // The logical volume name.
  uint64 size_gb = 2 [deprecated = true];  // Volume size in GiB. Deprecated in favor of size
  repeated string tags = 3;     // Tags to add to the volume during creation
  string device_class = 4;
  string lvcreate_option_class = 5;

  int64 size = 6; // Volume size in canonical bytes.
}
```

Note that SizeGB is used as uint64 where we cast down to int64. This means that potentially,
if someone had a volume before this change greater than `9.223.372.036.854.775.807 Gi`, he would now experience an overflow
that would break lvmd. *However*, we need to be aware that the CSIDriver capacity-ranges at most support values up to int64 limits,
so we do not break any currently known path.

This change from `size_gb` to `size` can be reused across all Requests/Responses and only needs minor adjustment.

There is a second part to this change: the json parsing in `lvmd/command`: This is currently the root of why all comparisons need to be done with `uint64` instead of `int64`.

We can replace all occurrences during the parsing process to get arround this:

```go
type vg struct {
	name string
	uuid string
	size int64
	free int64
}

func (u *vg) UnmarshalJSON(data []byte) error {
	type vgInternal struct {
		Name string `json:"vg_name"`
		UUID string `json:"vg_uuid"`
		Size string `json:"vg_size"`
		Free string `json:"vg_free"`
	}

	var temp vgInternal
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	u.name = temp.Name
	u.uuid = temp.UUID

	var convErr error
	u.size, convErr = strconv.ParseInt(temp.Size, 10, 64)
	if convErr != nil {
		return convErr
	}
	u.free, convErr = strconv.ParseInt(temp.Free, 10, 64)
	if convErr != nil {
		return convErr
	}

	return nil
}
```

Of course the same adjustments from `uint64` to `int64` be done for all methods using these structs and similar structs like `lv`.

Then they can be reused in `lvmd/lvservice` and `lvmd/vgservice` to simplify the calls from uint64 bitshift comparisons to simple byte comparisons:

The resulting `CreateLV` call for example would look almost exactly like the original `CreateLV`:

```go
func (s *lvService) CreateLV(_ context.Context, req *proto.CreateLVRequest) (*proto.CreateLVResponse, error) {
	dc, err := s.dcmapper.DeviceClass(req.DeviceClass)
    // ...
	vg, err := command.FindVolumeGroup(dc.VolumeGroup)
    // ...
	oc := s.ocmapper.LvcreateOptionClass(req.LvcreateOptionClass)
	requested := req.GetSize()
	free := int64(0)
	var pool *command.ThinPool
	switch dc.Type {
	case TypeThick:
		free, err = vg.Free()
		// ...
	case TypeThin:
		pool, err = vg.FindPool(dc.ThinPoolConfig.Name)
        // ...
		tpu, err := pool.Free()
        // ...
		free = int64(math.Floor(dc.ThinPoolConfig.OverprovisionRatio*float64(tpu.SizeBytes))) - tpu.VirtualBytes
	default:
		// technically this block will not be hit however make sure we return error
		// in such cases where deviceclass target is neither thick or thinpool
		return nil, status.Error(codes.Internal, fmt.Sprintf("unsupported device class target: %s", dc.Type))
	}

	if free < requested {
		log.Error("no enough space left on VG", map[string]interface{}{
			"free":      free,
			"requested": requested,
		})
		return nil, status.Errorf(codes.ResourceExhausted, "no enough space left on VG: free=%d, requested=%d", free, requested)
	}
    // ...
	var lv *command.LogicalVolume
	switch dc.Type {
	case TypeThick:
		lv, err = vg.CreateVolume(req.GetName(), requested, req.GetTags(), stripe, stripeSize, lvcreateOptions)
	case TypeThin:
		lv, err = pool.CreateVolume(req.GetName(), requested, req.GetTags(), stripe, stripeSize, lvcreateOptions)
	default:
		return nil, status.Error(codes.Internal, fmt.Sprintf("unsupported device class target: %s", dc.Type))
	}
    // ...
	return &proto.CreateLVResponse{
		Volume: &proto.LogicalVolume{
			Name:     lv.Name(),
			Size:     lv.Size(),
			DevMajor: lv.MajorNumber(),
			DevMinor: lv.MinorNumber(),
		},
	}, nil
}
```