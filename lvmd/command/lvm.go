package command

import (
	"context"
	"errors"
	"fmt"
	"path"
)

const (
	nsenter = "/usr/bin/nsenter"
	lvm     = "/sbin/lvm"
)

var Containerized = false

// ErrNotFound is returned when a VG or LV is not found.
var ErrNotFound = errors.New("not found")

// LVInfo is a map of lv attributes to values.
type LVInfo map[string]string

// VolumeGroup represents a volume group of linux lvm.
type VolumeGroup struct {
	state vg
	// internal lvs for use with getLVMState, should not be used otherwise as fields are fetched dynamically
	reportLvs []lv
}

// getLVMState returns the current state of lvm lvs for the given volume group.
// If lvname is empty, all lvs are returned. Otherwise, only the lv with the given name is returned or an error if not found.
func getLVs(ctx context.Context, vg *VolumeGroup, lvname string) ([]lv, error) {
	if len(vg.reportLvs) > 0 {
		if lvname != "" {
			for _, reportLv := range vg.reportLvs {
				if reportLv.name == lvname {
					return []lv{reportLv}, nil
				}
			}
			return nil, ErrNotFound
		}
		return vg.reportLvs, nil
	}

	var res = new(LvReport)

	name := vg.state.name
	if lvname != "" {
		name += "/" + lvname
	}

	args := []string{
		"lvs",
		name,
		"-o",
		"lv_uuid,lv_name,lv_full_name,lv_path,lv_size," +
			"lv_kernel_major,lv_kernel_minor,origin,origin_size,pool_lv,lv_tags," +
			"lv_attr,vg_name,data_percent,metadata_percent,pool_lv",
		"--units",
		"b",
		"--nosuffix",
		"--reportformat",
		"json",
	}
	err := callLVMInto(ctx, res, args...)

	if len(res.Report) == 0 {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	lvs := res.Report[0].LV

	if len(lvs) == 0 {
		return nil, ErrNotFound
	}

	return lvs, nil
}

func (vg *VolumeGroup) Update(ctx context.Context) error {
	newVG, err := FindVolumeGroup(ctx, vg.Name())
	if err != nil {
		return err
	}
	vg.reportLvs = nil
	vg.state = newVG.state
	return nil
}

// Name returns the volume group name.
func (vg *VolumeGroup) Name() string {
	return vg.state.name
}

// Size returns the capacity of the volume group in bytes.
func (vg *VolumeGroup) Size() (uint64, error) {
	return vg.state.size, nil
}

// Free returns the free space of the volume group in bytes.
func (vg *VolumeGroup) Free() (uint64, error) {
	return vg.state.free, nil
}

// FindVolumeGroup finds a named volume group.
// name is volume group name to look up.
func FindVolumeGroup(ctx context.Context, name string) (*VolumeGroup, error) {
	res := new(VgReport)
	args := []string{
		"vgs", name, "-o", "vg_uuid,vg_name,vg_size,vg_free", "--units", "b", "--nosuffix", "--reportformat", "json",
	}
	err := callLVMInto(ctx, res, args...)

	vgFound := false
	volumeGroup := VolumeGroup{}
	for _, report := range res.Report {
		for _, vg := range report.VG {
			if vg.name == name {
				volumeGroup.state = vg
				vgFound = true
				break
			}
		}
	}

	if err != nil {
		return &VolumeGroup{}, err
	}
	if !vgFound {
		return nil, ErrNotFound
	}
	return &volumeGroup, nil
}

func SearchVolumeGroupList(vgs []*VolumeGroup, name string) (*VolumeGroup, error) {
	for _, vg := range vgs {
		if vg.state.name == name {
			return vg, nil
		}
	}
	return nil, ErrNotFound
}

func filter_lv(vg_name string, lvs []lv) []lv {
	var filtered []lv
	for _, l := range lvs {
		if l.vgName == vg_name {
			filtered = append(filtered, l)
		}
	}
	return filtered
}

// ListVolumeGroups lists all volume groups and logical volumes through the lvm state, which
// is more efficient than calling vgs / lvs for every command.
func ListVolumeGroups(ctx context.Context) ([]*VolumeGroup, error) {
	vgs, lvs, err := getLVMState(ctx)
	if err != nil {
		return nil, err
	}

	var groups []*VolumeGroup
	for _, vg := range vgs {
		groups = append(groups, &VolumeGroup{state: vg, reportLvs: filter_lv(vg.name, lvs)})
	}
	return groups, nil
}

// FindVolume finds a named logical volume in this volume group.
func (vg *VolumeGroup) FindVolume(ctx context.Context, name string) (*LogicalVolume, error) {
	volumes, err := vg.ListVolumes(ctx, name)
	if err != nil {
		return nil, err
	}
	vol, ok := volumes[name]
	if !ok {
		return nil, ErrNotFound
	}

	return vol, nil
}

// ListVolumes lists all logical volumes in this volume group.
func (vg *VolumeGroup) ListVolumes(ctx context.Context, name string) (map[string]*LogicalVolume, error) {
	ret := map[string]*LogicalVolume{}

	lvs, err := getLVs(ctx, vg, name)
	if err != nil {
		return nil, err
	}

	for i, lv := range lvs {
		if !lv.isThinPool() {
			ret[lv.name] = vg.convertLV(&lvs[i])
		}
	}
	return ret, nil
}

func (vg *VolumeGroup) convertLV(lv *lv) *LogicalVolume {
	size := lv.size

	var origin *string
	if len(lv.origin) > 0 {
		origin = &lv.origin
	}

	var pool *string
	if len(lv.poolLV) > 0 {
		pool = &lv.poolLV
	}

	if origin != nil && pool == nil {
		// this volume is a snapshot, but not a thin volume.
		size = lv.originSize
	}

	return newLogicalVolume(
		lv.name,
		lv.path,
		vg,
		size,
		origin,
		pool,
		uint32(lv.major),
		uint32(lv.minor),
		lv.tags,
	)
}

// CreateVolume creates logical volume in this volume group.
// name is a name of creating volume. size is volume size in bytes. volTags is a
// list of tags to add to the volume.
// lvcreateOptions are additional arguments to pass to lvcreate.
func (vg *VolumeGroup) CreateVolume(ctx context.Context, name string, size uint64, tags []string, stripe uint, stripeSize string,
	lvcreateOptions []string) error {
	lvcreateArgs := []string{"lvcreate", "-n", name, "-L", fmt.Sprintf("%vb", size), "-W", "y", "-y"}
	for _, tag := range tags {
		lvcreateArgs = append(lvcreateArgs, "--addtag")
		lvcreateArgs = append(lvcreateArgs, tag)
	}
	if stripe != 0 {
		lvcreateArgs = append(lvcreateArgs, "-i", fmt.Sprintf("%d", stripe))

		if stripeSize != "" {
			lvcreateArgs = append(lvcreateArgs, "-I", stripeSize)
		}
	}
	lvcreateArgs = append(lvcreateArgs, lvcreateOptions...)
	lvcreateArgs = append(lvcreateArgs, vg.Name())

	return callLVM(ctx, lvcreateArgs...)
}

// FindPool finds a named thin pool in this volume group.
func (vg *VolumeGroup) FindPool(ctx context.Context, name string) (*ThinPool, error) {
	pools, err := getLVs(ctx, vg, name)
	if err != nil {
		return nil, err
	}
	pool := pools[0]
	return newThinPool(pool.name, vg, pool), nil
}

// ListPools lists all thin pool volumes in this volume group.
func (vg *VolumeGroup) ListPools(ctx context.Context) ([]*ThinPool, error) {
	var ret []*ThinPool
	lvs, err := getLVs(ctx, vg, "")
	if err != nil {
		return nil, err
	}
	for _, lv := range lvs {
		if lv.isThinPool() {
			ret = append(ret, newThinPool(lv.name, vg, lv))
		}
	}
	return ret, nil
}

// CreatePool creates a pool for thin-provisioning volumes.
func (vg *VolumeGroup) CreatePool(ctx context.Context, name string, size uint64) (*ThinPool, error) {
	if err := callLVM(ctx, "lvcreate", "-T", fmt.Sprintf("%v/%v", vg.Name(), name),
		"--size", fmt.Sprintf("%vb", size)); err != nil {
		return nil, err
	}
	return vg.FindPool(ctx, name)
}

// ThinPool represents a lvm thin pool.
type ThinPool struct {
	vg    *VolumeGroup
	state lv
}

// ThinPoolUsage holds current usage of lvm thin pool
type ThinPoolUsage struct {
	DataPercent     float64
	MetadataPercent float64
	VirtualBytes    uint64
	SizeBytes       uint64
}

func fullName(name string, vg *VolumeGroup) string {
	return fmt.Sprintf("%v/%v", vg.Name(), name)
}

func newThinPool(name string, vg *VolumeGroup, lvm_lv lv) *ThinPool {
	return &ThinPool{
		vg,
		lvm_lv,
	}
}

// Name returns thin pool name.
func (t *ThinPool) Name() string {
	return t.state.name
}

// FullName returns a VG prefixed name.
func (t *ThinPool) FullName() string {
	return t.state.fullName
}

// VG returns a volume group in which the thin pool is.
func (t *ThinPool) VG() *VolumeGroup {
	return t.vg
}

// Size returns a size of the thin pool.
func (t *ThinPool) Size() uint64 {
	return t.state.size
}

// Resize the thin pool capacity.
func (t *ThinPool) Resize(ctx context.Context, newSize uint64) error {
	if t.state.size == newSize {
		return nil
	}
	if err := callLVM(ctx, "lvresize", "-f", "-L", fmt.Sprintf("%vb", newSize), t.state.fullName); err != nil {
		return err
	}

	// now we need to update the size of this volume, as it might slightly differ from the creation argument due to rounding
	vol, err := t.vg.FindVolume(ctx, t.Name())
	if err != nil {
		return err
	}
	t.state.size = vol.size

	return nil
}

// ListVolumes lists all volumes in this thin pool.
func (t *ThinPool) ListVolumes(ctx context.Context) (map[string]*LogicalVolume, error) {
	volumes, err := t.vg.ListVolumes(ctx, "")
	filteredVolumes := make(map[string]*LogicalVolume, len(volumes))
	for _, volume := range volumes {
		if volume.pool != nil && *volume.pool == t.Name() {
			filteredVolumes[volume.Name()] = volume
		}
	}
	if err != nil {
		return nil, err
	}
	return filteredVolumes, nil
}

// FindVolume finds a named logical volume in this thin pool
func (t *ThinPool) FindVolume(ctx context.Context, name string) (*LogicalVolume, error) {
	volumeCandidate, err := t.vg.FindVolume(ctx, name)
	if err != nil {
		return nil, err
	}
	// volume exists in vg but is in no pool or different pool
	if volumeCandidate.pool == nil || *volumeCandidate.pool != t.Name() {
		return nil, ErrNotFound
	}
	return volumeCandidate, nil
}

// CreateVolume creates a thin volume from this pool.
func (t *ThinPool) CreateVolume(ctx context.Context, name string, size uint64, tags []string, stripe uint, stripeSize string, lvcreateOptions []string) error {
	lvcreateArgs := []string{
		"lvcreate",
		"-T",
		t.FullName(),
		"-n",
		name,
		"-V",
		fmt.Sprintf("%vb", size),
		"-W",
		"y",
		"-y",
	}
	for _, tag := range tags {
		lvcreateArgs = append(lvcreateArgs, "--addtag")
		lvcreateArgs = append(lvcreateArgs, tag)
	}
	if stripe != 0 {
		lvcreateArgs = append(lvcreateArgs, "-i", fmt.Sprintf("%d", stripe))

		if stripeSize != "" {
			lvcreateArgs = append(lvcreateArgs, "-I", stripeSize)
		}
	}
	lvcreateArgs = append(lvcreateArgs, lvcreateOptions...)

	return callLVM(ctx, lvcreateArgs...)
}

// Free on a thinpool returns used data, metadata percentages,
// sum of virtualsizes of all thinlvs and size of thinpool
func (t *ThinPool) Free(ctx context.Context) (*ThinPoolUsage, error) {
	tpu := &ThinPoolUsage{}
	tpu.DataPercent = t.state.dataPercent
	tpu.MetadataPercent = t.state.metaDataPercent
	tpu.SizeBytes = t.state.size
	lvs, err := t.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}

	for _, l := range lvs {
		tpu.VirtualBytes += l.size
	}
	return tpu, nil
}

// LogicalVolume represents a logical volume.
type LogicalVolume struct {
	fullname string
	// name is equivalent for LogicalVolume CRD UID
	name     string
	path     string
	vg       *VolumeGroup
	size     uint64
	origin   *string
	pool     *string
	devMajor uint32
	devMinor uint32
	tags     []string
}

func newLogicalVolume(name, path string, vg *VolumeGroup, size uint64, origin, pool *string, major, minor uint32, tags []string) *LogicalVolume {
	fullname := fullName(name, vg)
	return &LogicalVolume{
		fullname,
		name,
		path,
		vg,
		size,
		origin,
		pool,
		major,
		minor,
		tags,
	}
}

// Name returns a volume name.
func (l *LogicalVolume) Name() string {
	return l.name
}

// FullName returns a vg prefixed volume name.
func (l *LogicalVolume) FullName() string {
	return l.fullname
}

// Path returns a path to the logical volume.
func (l *LogicalVolume) Path() string {
	return l.path
}

// VG returns a volume group in which the volume is.
func (l *LogicalVolume) VG() *VolumeGroup {
	return l.vg
}

// Size returns a size of the volume.
func (l *LogicalVolume) Size() uint64 {
	return l.size
}

// IsSnapshot checks if the volume is snapshot or not.
func (l *LogicalVolume) IsSnapshot() bool {
	return l.origin != nil
}

// Origin returns logical volume instance if this is a snapshot, or nil if not.
func (l *LogicalVolume) Origin(ctx context.Context) (*LogicalVolume, error) {
	if l.origin == nil {
		return nil, nil
	}
	return l.vg.FindVolume(ctx, *l.origin)
}

// IsThin checks if the volume is thin volume or not.
func (l *LogicalVolume) IsThin() bool {
	return l.pool != nil
}

// Pool returns thin pool if this is a thin pool, or nil if not.
func (l *LogicalVolume) Pool(ctx context.Context) (*ThinPool, error) {
	if l.pool == nil {
		return nil, nil
	}
	return l.vg.FindPool(ctx, *l.pool)
}

// MajorNumber returns the device major number.
func (l *LogicalVolume) MajorNumber() uint32 {
	return l.devMajor
}

// MinorNumber returns the device minor number.
func (l *LogicalVolume) MinorNumber() uint32 {
	return l.devMinor
}

// Tags returns the tags member.
func (l *LogicalVolume) Tags() []string {
	return l.tags
}

// ThinSnapshot takes a thin snapshot of a volume.
// The volume must be thinly-provisioned.
// snapshots can be created unconditionally.
func (l *LogicalVolume) ThinSnapshot(ctx context.Context, name string, tags []string) error {
	if !l.IsThin() {
		return fmt.Errorf("cannot take snapshot of non-thin volume: %s", l.fullname)
	}

	lvcreateArgs := []string{"lvcreate", "-s", "-k", "n", "-n", name, l.fullname}

	for _, tag := range tags {
		lvcreateArgs = append(lvcreateArgs, "--addtag")
		lvcreateArgs = append(lvcreateArgs, tag)
	}

	return callLVM(ctx, lvcreateArgs...)
}

// Activate activates the logical volume for desired access.
func (l *LogicalVolume) Activate(ctx context.Context, access string) error {
	var lvchangeArgs []string
	switch access {
	case "ro":
		lvchangeArgs = []string{"lvchange", "-p", "r", l.path}
	case "rw":
		lvchangeArgs = []string{"lvchange", "-k", "n", "-a", "y", l.path}
	default:
		return fmt.Errorf("unknown access: %s for LogicalVolume %s", access, l.fullname)
	}

	return callLVM(ctx, lvchangeArgs...)
}

// Resize this volume.
// newSize is a new size of this volume in bytes.
func (l *LogicalVolume) Resize(ctx context.Context, newSize uint64) error {
	if l.size > newSize {
		return fmt.Errorf("volume cannot be shrunk")
	}
	if l.size == newSize {
		return nil
	}
	if err := callLVM(ctx, "lvresize", "-L", fmt.Sprintf("%vb", newSize), l.fullname); err != nil {
		return err
	}

	// now we need to update the size of this volume, as it might slightly differ from the creation argument due to rounding
	vol, err := l.vg.FindVolume(ctx, l.name)
	if err != nil {
		return err
	}
	l.size = vol.size

	return nil
}

// Remove this volume.
func (l *LogicalVolume) Remove(ctx context.Context) error {
	return callLVM(ctx, "lvremove", "-f", l.path)
}

// Rename this volume.
// This method also updates properties such as Name() or Path().
func (l *LogicalVolume) Rename(ctx context.Context, name string) error {
	if err := callLVM(ctx, "lvrename", l.vg.Name(), l.name, name); err != nil {
		return err
	}
	l.fullname = fullName(name, l.vg)
	l.name = name
	l.path = path.Join(path.Dir(l.path), l.name)
	return nil
}
