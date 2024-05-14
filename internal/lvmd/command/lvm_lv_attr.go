package command

import (
	"fmt"
)

type VolumeType rune

const (
	VolumeTypeMirrored                   VolumeType = 'm'
	VolumeTypeMirroredNoInitialSync      VolumeType = 'M'
	VolumeTypeOrigin                     VolumeType = 'o'
	VolumeTypeOriginWithMergingSnapshot  VolumeType = 'O'
	VolumeTypeRAID                       VolumeType = 'r'
	VolumeTypeRAIDNoInitialSync          VolumeType = 'R'
	VolumeTypeSnapshot                   VolumeType = 's'
	VolumeTypeMergingSnapshot            VolumeType = 'S'
	VolumeTypePVMove                     VolumeType = 'p'
	VolumeTypeVirtual                    VolumeType = 'v'
	VolumeTypeMirrorOrRAIDImage          VolumeType = 'i'
	VolumeTypeMirrorOrRAIDImageOutOfSync VolumeType = 'I'
	VolumeTypeMirrorLogDevice            VolumeType = 'l'
	VolumeTypeUnderConversion            VolumeType = 'c'
	VolumeTypeThinVolume                 VolumeType = 'V'
	VolumeTypeThinPool                   VolumeType = 't'
	VolumeTypeThinPoolData               VolumeType = 'T'
	VolumeTypeThinPoolMetadata           VolumeType = 'e'
	VolumeTypeNone                       VolumeType = '-'
)

type Permissions rune

const (
	PermissionsWriteable                             Permissions = 'w'
	PermissionsReadOnly                              Permissions = 'r'
	PermissionsReadOnlyActivationOfNonReadOnlyVolume Permissions = 'R'
	PermissionsNone                                  Permissions = '-'
)

type AllocationPolicy rune

const (
	AllocationPolicyAnywhere         AllocationPolicy = 'a'
	AllocationPolicyAnywhereLocked   AllocationPolicy = 'A'
	AllocationPolicyContiguous       AllocationPolicy = 'c'
	AllocationPolicyContiguousLocked AllocationPolicy = 'C'
	AllocationPolicyInherited        AllocationPolicy = 'i'
	AllocationPolicyInheritedLocked  AllocationPolicy = 'I'
	AllocationPolicyCling            AllocationPolicy = 'l'
	AllocationPolicyClingLocked      AllocationPolicy = 'L'
	AllocationPolicyNormal           AllocationPolicy = 'n'
	AllocationPolicyNormalLocked     AllocationPolicy = 'N'
	AllocationPolicyNone                              = '-'
)

type Minor rune

const (
	MinorTrue  Minor = 'm'
	MinorFalse Minor = '-'
)

type State rune

const (
	StateActive                                State = 'a'
	StateSuspended                             State = 's'
	StateInvalidSnapshot                       State = 'I'
	StateSuspendedSnapshot                     State = 'S'
	StateSnapshotMergeFailed                   State = 'm'
	StateSuspendedSnapshotMergeFailed          State = 'M'
	StateMappedDevicePresentWithoutTables      State = 'd'
	StateMappedDevicePresentWithInactiveTables State = 'i'
	StateNone                                  State = '-'
	StateHistorical                            State = 'h'
	StateThinPoolCheckNeeded                   State = 'c'
	StateSuspendedThinPoolCheckNeeded          State = 'C'
	StateUnknown                               State = 'X'
)

type Open rune

const (
	OpenTrue    Open = 'o'
	OpenFalse   Open = '-'
	OpenUnknown Open = 'X'
)

type OpenTarget rune

const (
	OpenTargetMirror   = 'm'
	OpenTargetRaid     = 'r'
	OpenTargetSnapshot = 's'
	OpenTargetThin     = 't'
	OpenTargetUnknown  = 'u'
	OpenTargetVirtual  = 'v'
)

type Zero rune

const (
	ZeroTrue  Zero = 'z'
	ZeroFalse Zero = '-'
)

type VolumeHealth rune

const (
	VolumeHealthPartialActivation        = 'p'
	VolumeHealthUnknown                  = 'X'
	VolumeHealthMissing                  = '-'
	VolumeHealthRAIDRefreshNeeded        = 'r'
	VolumeHealthRAIDMismatchesExist      = 'm'
	VolumeHealthRAIDWriteMostly          = 'w'
	VolumeHealthRAIDReshaping            = 's'
	VolumeHealthRAIDReshapeRemoved       = 'R'
	VolumeHealthThinFailed               = 'F'
	VolumeHealthThinPoolOutOfDataSpace   = 'D'
	VolumeHealthThinPoolMetadataReadOnly = 'M'
	VolumeHealthWriteCacheError          = 'E'
)

// LvAttr has mapped lv_attr information, see https://linux.die.net/man/8/lvs
// It is a complete parsing of the entire attribute byte flags that is attached to each LV.
// This is useful when attaching logic to the state of an LV as the state of an LV can be determined
// from the Attributes, e.g. for determining whether an LV is considered a Thin-Pool or not.
type LvAttr struct {
	VolumeType
	Permissions
	AllocationPolicy
	Minor
	State
	Open
	OpenTarget
	Zero
	VolumeHealth
}

func ParsedLvAttr(raw string) (LvAttr, error) {
	if len(raw) != 10 {
		return LvAttr{}, fmt.Errorf("%s is an invalid length lv_attr", raw)
	}
	return LvAttr{
		VolumeType(raw[0]),
		Permissions(raw[1]),
		AllocationPolicy(raw[2]),
		Minor(raw[3]),
		State(raw[4]),
		Open(raw[5]),
		OpenTarget(raw[6]),
		Zero(raw[7]),
		VolumeHealth(raw[8]),
	}, nil
}

func (l LvAttr) String() string {
	return fmt.Sprintf(
		"%c%c%c%c%c%c%c%c%c",
		l.VolumeType,
		l.Permissions,
		l.AllocationPolicy,
		l.Minor,
		l.State,
		l.Open,
		l.OpenTarget,
		l.Zero,
		l.VolumeHealth,
	)
}

// VerifyHealth checks the health of the logical volume based on the attributes, mainly
// bit 9 (volume health indicator) based on bit 1 (volume type indicator)
// All failed known states are reported with an error message.
func (l LvAttr) VerifyHealth() error {
	if l.VolumeHealth == VolumeHealthPartialActivation {
		return fmt.Errorf("found partial activation of physical volumes, one or more physical volumes are setup incorrectly")
	}
	if l.VolumeHealth == VolumeHealthUnknown {
		return fmt.Errorf("unknown volume health reported, verification on the host system is required")
	}
	if l.VolumeHealth == VolumeHealthWriteCacheError {
		return fmt.Errorf("write cache error signifies that dm-writecache reports an error")
	}

	if l.VolumeType == VolumeTypeThinPool {
		switch l.VolumeHealth {
		case VolumeHealthThinFailed:
			return fmt.Errorf("thin pool encounters serious failures and hence no further I/O is permitted at all")
		case VolumeHealthThinPoolOutOfDataSpace:
			return fmt.Errorf("thin pool is out of data space, no further data can be written to the thin pool without extension")
		case VolumeHealthThinPoolMetadataReadOnly:
			return fmt.Errorf("metadata read only signifies that thin pool encounters certain types of failures, " +
				"but it's still possible to do data reads. However, no metadata changes are allowed")
		}
	}

	if l.VolumeType == VolumeTypeThinVolume {
		switch l.VolumeHealth {
		case VolumeHealthThinFailed:
			return fmt.Errorf("the underlying thin pool entered a failed state and no further I/O is permitted")
		}
	}

	if l.VolumeType == VolumeTypeRAID || l.VolumeType == VolumeTypeRAIDNoInitialSync {
		switch l.VolumeHealth {
		case VolumeHealthRAIDRefreshNeeded:
			return fmt.Errorf("RAID volume requires a refresh, one or more Physical Volumes have suffered a write error." +
				"This could be due to temporary failure of the Physical Volume or an indication it is failing. " +
				"The device should be refreshed or replaced")
		case VolumeHealthRAIDMismatchesExist:
			return fmt.Errorf("RAID volume has portions of the array that are not coherent. " +
				"Inconsistencies are detected by initiating a check RAID logical volume." +
				"The  scrubbing  operations, \"check\" and \"repair\", can be performed on a " +
				"RAID volume via the \"lvchange\" command.")
		case VolumeHealthRAIDReshaping:
			return fmt.Errorf("RAID volume is currently reshaping. " +
				"Reshaping signifies a RAID Logical Volume is either undergoing a stripe addition/removal, " +
				"a stripe size or RAID algorithm change")
		case VolumeHealthRAIDReshapeRemoved:
			return fmt.Errorf("RAID volume signifies freed raid images after reshaping")
		case VolumeHealthRAIDWriteMostly:
			return fmt.Errorf("RAID volume is marked as write-mostly. this signifies the devices in a RAID 1 logical volume have been marked write-mostly." +
				"This means that reading from this device will be avoided, and other devices will be preferred for reading (unless no other devices are available). " +
				"this minimizes the I/O to the specified device")
		}
	}

	switch l.State {
	case StateSuspended:
		fallthrough
	case StateSuspendedSnapshot:
		return fmt.Errorf("logical volume is in a suspended state, no I/O is permitted")
	case StateInvalidSnapshot:
		return fmt.Errorf("logical volume is an invalid snapshot, no I/O is permitted")
	case StateSuspendedSnapshotMergeFailed:
		fallthrough
	case StateSnapshotMergeFailed:
		return fmt.Errorf("snapshot merge failed, no I/O is permitted")
	case StateMappedDevicePresentWithInactiveTables:
		return fmt.Errorf("mapped device present with inactive tables, no I/O is permitted")
	case StateMappedDevicePresentWithoutTables:
		return fmt.Errorf("mapped device present without tables, no I/O is permitted")
	case StateSuspendedThinPoolCheckNeeded:
		fallthrough
	case StateThinPoolCheckNeeded:
		return fmt.Errorf("a thin pool check is needed")
	case StateUnknown:
		return fmt.Errorf("unknown volume state, verification on the host system is required")
	case StateHistorical:
		return fmt.Errorf("historical volume state (volume no longer exists but is kept around in logs), " +
			"verification on the host system is required")
	}

	switch l.Open {
	case OpenUnknown:
		return fmt.Errorf("logical volume underlying device state is unknown, " +
			"verification on the host system is required")
	}

	return nil
}
