package driver

import (
	"fmt"
	"testing"

	"github.com/topolvm/topolvm"
)

func Test_convertRequestCapacityBytes(t *testing.T) {
	_, err := convertRequestCapacityBytes(-1, 10)
	if err == nil {
		t.Error("should be error")
	}
	if err.Error() != "required capacity must not be negative" {
		t.Error("should report invalid required capacity")
	}

	_, err = convertRequestCapacityBytes(10, -1)
	if err == nil {
		t.Error("should be error")
	}
	if err.Error() != "capacity limit must not be negative" {
		t.Error("should report invalid capacity limit")
	}

	_, err = convertRequestCapacityBytes(20, 10)
	if err == nil {
		t.Error("should be error")
	}
	if err.Error() != "requested capacity exceeds limit capacity: request=20 limit=10" {
		t.Error("should report capacity limit exceeded")
	}

	v, err := convertRequestCapacityBytes(0, topolvm.MinimumSectorSize-1)
	if err == nil {
		t.Errorf("should be error")
	}
	if err.Error() != RoundingTo0Error(0, topolvm.MinimumSectorSize-1).Error() {
		t.Errorf("should report rounding error: %v", err)
	}

	v, err = convertRequestCapacityBytes(0, topolvm.MinimumSectorSize+1)
	if err != nil {
		t.Error("should not be error")
	}
	if v != topolvm.MinimumSectorSize {
		t.Errorf("should be nearest rounded up multiple of sector size if 0 is supplied and limit is larger than sector-size: %d", v)
	}

	v, err = convertRequestCapacityBytes(0, 2<<30)
	if err != nil {
		t.Error("should not be error")
	}
	if v != 1<<30 {
		t.Errorf("should be at least 1 Gi requested by default if 0 is supplied: %d", v)
	}

	v, err = convertRequestCapacityBytes(1, 0)
	if err == nil {
		t.Errorf("should be error")
	}
	if err.Error() != RoundingTo0Error(0, 0).Error() {
		t.Errorf("should report rounding error: %v", err)
	}

	v, err = convertRequestCapacityBytes(1<<30, 1<<30)
	if err != nil {
		t.Error("should not be error")
	}
	if v != 1<<30 {
		t.Errorf("should be 1073741824 in byte precision: %d", v)
	}

	_, err = convertRequestCapacityBytes(1<<30+1, 1<<30+1)
	if err != nil {
		t.Error("should be error")
	}
	if v != 1<<30 {
		t.Errorf("should be 1073741824 in byte precision: %d", v)
	}

	v, err = convertRequestCapacityBytes(0, 0)
	if err != nil {
		t.Error("should not be error")
	}
	if v != 1<<30 {
		t.Errorf("should be 1073741825 in byte precision: %d", v)
	}

	v, err = convertRequestCapacityBytes(1, topolvm.MinimumSectorSize*2)
	if err == nil {
		t.Errorf("should be error")
	}
	if err.Error() != RoundingTo0Error(0, topolvm.MinimumSectorSize*2).Error() {
		t.Errorf("should report rounding error: %v", err)
	}

}

func Test_roundUp(t *testing.T) {
	testCases := []struct {
		size     int64
		multiple int64
		expected int64
	}{
		{12, 4, 12},
		{11, 4, 12},
		{13, 4, 16},
		{0, 4, 0},
	}

	for _, tc := range testCases {
		name := fmt.Sprintf("nearest rounded up multiple of %d from %d should be %d", tc.multiple, tc.size, tc.expected)
		t.Run(name, func(t *testing.T) {
			rounded := roundDown(tc.size, tc.multiple)
			if rounded != tc.expected {
				t.Errorf("%s, but was %d", name, rounded)
			}
		})
	}
}
