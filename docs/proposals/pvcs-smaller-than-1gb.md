# Supporting PVCs smaller than 1 GB in size.

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
- [Proposal](#proposal)
    - [Decision Outcome](#decision-outcome)
- [Design Details](#design-details)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
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

- Ensuring backwards-compatibility with schedulers and legacy bitshifts that would be replaced
- Discovering all places with hardcoded bitshifts and transitioniing them to a more modular and sizable approach
- Allowing creation of PVCs that request less than 1GB of storage, with a minimum size that is reasonably set.

## Proposal

1. Find all occurrences of `<<30` Bitshifts and replace with dynamic comparisons

The main reason of hardcoding 1GB as minimum size is the `<<30` bitshift. This causes
all values in GB to be translated to bytes.
Any occurrence of the infamous `<<30` bitshift needs to be identified
and replaced with a more dynamic version.

In total the entire codebase contains 73 occurrences of `<<30` and 8 occurences of `>>30` as of writing this proposal.
While it would not be completely trivial to replace, we believe this is still a reasonable amount to refactor with manual care.

The easiest way to do this is by introducing a new utility that is able to convert
various units of storage and compare them to each other.

Interestingly enough, kubernetes upstream already has a solution for this problem: the resource Quantity:
https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/

This way we can easily communicate with various definitions of storage and still get back to a uniform format.

From upstream we can take the existing definitions
```
	DecimalExponent = Format("DecimalExponent") // e.g., 12e6
	BinarySI        = Format("BinarySI")        // e.g., 12Mi (12 * 2^20)
	DecimalSI       = Format("DecimalSI")       // e.g., 12M  (12 * 10^6)
```

And use them whenever we encounter a bitshift:

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

as one can see we can easily replace the comparison logic with either a `MustParse` for tests
and with a normal creation of bits for code-paths. 

At the same time we can make use of the resource Capacity for parsing into JSON or YAML so it can be reused
in communication within LVMD. That way we can choose to either break LVMD protocol and translate all existing code
or to introduce a new field that gives that information.

Since all CSI Driver values already work with bytes, we have no trouble taking in the new data, we will just accept more ranges.

### Protocol Considerations in lvmd

Since lvmd currently includes various messages with a `uint64 size_gb` field, we should think of how to properly
serialize new the capacity information in a scalable way. Here we have an inbuilt option with the kubernetes quantities as well.

By using the embedding of the Quantity directly from https://github.com/kubernetes/apimachinery/blob/master/pkg/api/resource/generated.proto:
```protobuf
// +protobuf=true
// +protobuf.embed=string
// +protobuf.options.marshal=false
// +protobuf.options.(gogoproto.goproto_stringer)=false
// +k8s:deepcopy-gen=true
// +k8s:openapi-gen=true
message Quantity {
  optional string string = 1;
}
```

We could include this schema dynamically or just copy it once and keep it in line with upstream and then include it like so:

```protobuf

import "k8s.io/apimachinery/pkg/api/resource/resource.proto"; // contains copy of https://github.com/kubernetes/apimachinery/blob/master/pkg/api/resource/generated.proto:
// Represents the input for CreateLV.
message CreateLVRequest {
    string name = 1;              // The logical volume name.
    uint64 size_gb = 2;           // Volume size in GiB.
    repeated string tags = 3;     // Tags to add to the volume during creation
    string device_class = 4;
    string lvcreate_option_class = 5;

    k8s.io.apimachinery.pkg.api.resource.Quantity size_quantity = 6;
}
```

As one can see, we already have existing protobuf definitions that we could include as `Quantity size_quantity` within our message definitions
while keeping serialization safe. The logic to use this would be the following pseudo-code path:

```
if field size_gb > 0
    use size_gb and convert to capacity
else 
    use size_quantity and parse as capacity
```

That way we could always stay backwards compatible by switching to the more accurate `size_quantity` in case `size_gb` is not filled,
and using `size_gb` under all other circumstances. At the same time, we would switch all implementations to use `size_capacity` when interacting with LVMD.

However, it should be noted that it may not even be necessary to keep backwards compatibility since lvmd is an internal protocol.


### Decision Outcome


## Design Details

### Upgrade / Downgrade Strategy
