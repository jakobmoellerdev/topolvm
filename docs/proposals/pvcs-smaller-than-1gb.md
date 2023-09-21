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



### Decision Outcome


## Design Details

### Upgrade / Downgrade Strategy
