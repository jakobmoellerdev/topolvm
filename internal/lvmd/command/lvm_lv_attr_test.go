package command

import (
	"fmt"
	"strings"
	"testing"
)

func TestParsedLvAttr(t *testing.T) {
	NoError := func(t *testing.T, err error, args ...string) bool {
		if err != nil {
			t.Helper()
			out := fmt.Sprintf("received unexpected error: %v", err)
			if len(args) > 0 {
				out = fmt.Sprintf("%s, %v", out, strings.Join(args, ","))
			}
			t.Errorf(out)
		}

		return true
	}

	type args struct {
		raw string
	}
	tests := []struct {
		name    string
		args    args
		want    LvAttr
		wantErr func(*testing.T, error, ...string) bool
	}{
		{
			"RAID Config without Initial Sync",
			args{raw: "Rwi-a-r---"},
			LvAttr{
				VolumeType:       VolumeTypeRAIDNoInitialSync,
				Permissions:      PermissionsWriteable,
				AllocationPolicy: AllocationPolicyInherited,
				Minor:            MinorFalse,
				State:            StateActive,
				Open:             OpenFalse,
				OpenTarget:       OpenTargetRaid,
				Zero:             ZeroFalse,
				VolumeHealth:     VolumeHealthMissing,
			},
			NoError,
		},
		{
			"ThinPool with Zeroing",
			args{raw: "twi-a-tz--"},
			LvAttr{
				VolumeType:       VolumeTypeThinPool,
				Permissions:      PermissionsWriteable,
				AllocationPolicy: AllocationPolicyInherited,
				Minor:            MinorFalse,
				State:            StateActive,
				Open:             OpenFalse,
				OpenTarget:       OpenTargetThin,
				Zero:             ZeroTrue,
				VolumeHealth:     VolumeHealthMissing,
			},
			NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsedLvAttr(tt.args.raw)
			if !tt.wantErr(t, err, fmt.Sprintf("ParsedLvAttr(%v)", tt.args.raw)) {
				return
			}
			if tt.want != got {
				t.Errorf("ParsedLvAttr() = %v, want %v, raw %v", got, tt.want, tt.args.raw)
			}
		})
	}
}
