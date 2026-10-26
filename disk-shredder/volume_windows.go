//go:build windows

package main

// Windows 卷枚举：盘符位图 → 逐个问类型 / 容量 / 文件系统 / 是否 SSD。
// 全用 kernel32 现成 API，不引第三方依赖，也不需要管理员权限。
//
// SSD 判定用 IOCTL_STORAGE_QUERY_PROPERTY 的 StorageDeviceSeekPenaltyProperty：
// 「无寻道惩罚」= 闪存。这是微软官方推荐的非特权判定法，比看型号字符串靠谱。
// 该 IOCTL 在部分 U 盘 / 网络盘 / 老系统上会失败，失败就返回 unknown——宁可说不知道。

import (
	"encoding/binary"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	k32                  = syscall.NewLazyDLL("kernel32.dll")
	pGetLogicalDrives    = k32.NewProc("GetLogicalDrives")
	pGetDriveTypeW       = k32.NewProc("GetDriveTypeW")
	pGetDiskFreeSpaceExW = k32.NewProc("GetDiskFreeSpaceExW")
	pGetVolumeInfoW      = k32.NewProc("GetVolumeInformationW")
	pCreateFileW         = k32.NewProc("CreateFileW")
	pDeviceIoControl     = k32.NewProc("DeviceIoControl")
	pCloseHandle         = k32.NewProc("CloseHandle")
)

const (
	// CTL_CODE(IOCTL_STORAGE_BASE=0x2d, 0x0500, METHOD_BUFFERED=0, FILE_ANY_ACCESS=0)
	ioctlStorageQueryProperty = 0x002D1400
	// CTL_CODE(IOCTL_VOLUME_BASE=0x56, 0, METHOD_BUFFERED, FILE_ANY_ACCESS)
	ioctlVolumeGetDiskExtents = 0x00560000

	storageDeviceSeekPenaltyProperty = 7 // STORAGE_PROPERTY_ID
	storageDeviceTrimProperty        = 8 // STORAGE_PROPERTY_ID（是否支持 TRIM）
	propertyStandardQuery            = 0 // STORAGE_QUERY_TYPE

	driveUnknown   = 0
	driveNoRootDir = 1
	driveRemovable = 2
	driveFixed     = 3
	driveRemote    = 4
	driveCDROM     = 5
	driveRAMDisk   = 6
)

// storagePropertyQuery 对应 C 的 STORAGE_PROPERTY_QUERY（12 字节：4+4+1+padding）。
type storagePropertyQuery struct {
	PropertyID           uint32
	QueryType            uint32
	AdditionalParameters [1]byte
}

func utf16Ptr(s string) *uint16 {
	w := utf16.Encode([]rune(s + "\x00"))
	return &w[0]
}

func utf16ToString(p *uint16, max int) string {
	if p == nil {
		return ""
	}
	out := make([]uint16, 0, max)
	for i := 0; i < max; i++ {
		c := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(i*2)))
		if c == 0 {
			break
		}
		out = append(out, c)
	}
	return string(utf16.Decode(out))
}

func listVolumes() []Volume {
	mask, _, _ := pGetLogicalDrives.Call()
	vs := make([]Volume, 0, 8)
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + ":\\"
		v := Volume{Path: root}

		t, _, _ := pGetDriveTypeW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))))
		switch t {
		case driveFixed:
			v.Kind = "fixed"
		case driveRemovable:
			v.Kind = "removable"
		case driveRemote:
			v.Kind = "network"
		case driveCDROM:
			v.Kind = "cdrom"
		case driveRAMDisk:
			v.Kind = "ramdisk"
		default:
			v.Kind = "unknown"
		}

		var freeBytes, totalBytes, totalFree uint64
		ok, _, _ := pGetDiskFreeSpaceExW.Call(
			uintptr(unsafe.Pointer(utf16Ptr(root))),
			uintptr(unsafe.Pointer(&freeBytes)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFree)),
		)
		if ok != 0 {
			v.FreeBytes = freeBytes
			v.TotalBytes = totalBytes
		}

		var serial, maxComp, flags uint32
		labelBuf := make([]uint16, 261)
		fsBuf := make([]uint16, 261)
		if ok, _, _ := pGetVolumeInfoW.Call(
			uintptr(unsafe.Pointer(utf16Ptr(root))),
			uintptr(unsafe.Pointer(&labelBuf[0])), uintptr(len(labelBuf)),
			uintptr(unsafe.Pointer(&serial)), uintptr(unsafe.Pointer(&maxComp)), uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&fsBuf[0])), uintptr(len(fsBuf)),
		); ok != 0 {
			v.Name = strings.TrimSpace(utf16ToString(&labelBuf[0], len(labelBuf)))
			v.FileSystem = strings.TrimSpace(utf16ToString(&fsBuf[0], len(fsBuf)))
		}
		if v.Name == "" {
			v.Name = root
		}

		// 光盘 / 网络盘不测介质：一个没有磁道概念，一个问不到本地物理属性
		if v.Kind == "fixed" || v.Kind == "removable" || v.Kind == "ramdisk" {
			v.Media = seekPenaltyMedia(root)
		} else {
			v.Media = "unknown"
		}
		if v.Kind == "cdrom" || v.Kind == "network" {
			v.Writable = false
		} else {
			v.Writable = probeWritable(root)
		}
		vs = append(vs, v)
	}
	return vs
}

// seekPenaltyMedia 打开 \\.\X: 问「有没有寻道惩罚」，false → ssd，true → hdd。
// 问不到就退一步问「支不支持 TRIM」（StorageDeviceTrimProperty）——支持 TRIM 的基本都是闪存；
// 两者都失败才判 unknown（部分 USB 硬盘盒 / RAID 卡会吞掉这两个 IOCTL）。
func seekPenaltyMedia(root string) string {
	if len(root) < 2 {
		return "unknown"
	}
	dev := `\\.\` + root[:2]
	handle, _, _ := pCreateFileW.Call(
		uintptr(unsafe.Pointer(utf16Ptr(dev))),
		0,                     // dwDesiredAccess：只做 IOCTL，不需要读写权限
		0x00000001|0x00000002, // FILE_SHARE_READ | FILE_SHARE_WRITE
		0,                     // 默认安全属性
		3,                     // OPEN_EXISTING
		0,
		0,
	)
	if handle == uintptr(syscall.InvalidHandle) || handle == 0 {
		qdLogWarn("介质判定：打不开 %s（GetLastError=%v）", dev, syscall.GetLastError())
		return "unknown"
	}
	defer pCloseHandle.Call(handle)

	// 先直接问卷句柄
	if m := mediaFromHandle(handle); m != "unknown" {
		return m
	}
	// 卷句柄不转发 storage IOCTL 时（多半是机械盘，ERROR_INVALID_FUNCTION），
	// 反查物理磁盘号，再拿 \\.\PhysicalDriveN 问一次
	if m := mediaViaPhysicalDrive(handle, dev); m != "unknown" {
		return m
	}
	if qdThrottled("media:"+dev, 5*time.Minute) {
		qdLogWarn("介质判定：%s 两个属性都查不到（lastErr=%v），按未知处理", dev, lastErr)
	}
	return "unknown"
}

// mediaFromHandle 在同一个句柄上依次问「寻道惩罚」与「TRIM」。
func mediaFromHandle(handle uintptr) string {
	// 第一问：寻道惩罚（微软推荐的判定法）
	if b, ok := queryStorageProperty(handle, storageDeviceSeekPenaltyProperty); ok && len(b) > 8 {
		if b[8] == 0 {
			return "ssd"
		}
		return "hdd"
	}
	// 第二问：TRIM（支持即闪存；不支持不能反推是机械盘，老 SSD 也不支持）
	if b, ok := queryStorageProperty(handle, storageDeviceTrimProperty); ok && len(b) > 8 {
		if b[8] != 0 {
			return "ssd"
		}
	}
	return "unknown"
}

// mediaViaPhysicalDrive 用 IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS 拿到磁盘号，
// 再打开 \\.\PhysicalDriveN 问一遍。打开物理盘通常要管理员，打不开就老实返回 unknown。
//
// 布局按 C 的对齐来，别按字段宽度硬算：DISK_EXTENT 里有 LARGE_INTEGER，
// 对齐是 8 字节，所以 sizeof(DISK_EXTENT)=24、第一个 extent 落在 offset 8。
// （按 4+4+8+8=20 读会读到 padding 的 0，把所有卷都认成磁盘 0 —— 踩过一次。）
func mediaViaPhysicalDrive(volHandle uintptr, volDev string) string {
	out := make([]byte, 8+24*8) // NumberOfDiskExtents(4) + padding(4) + 若干 DISK_EXTENT(24)
	var returned uint32
	ok, _, errno := pDeviceIoControl.Call(
		volHandle,
		uintptr(ioctlVolumeGetDiskExtents),
		0, 0,
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)),
		uintptr(unsafe.Pointer(&returned)),
		0,
	)
	if ok == 0 || returned < 32 {
		lastErr = errno
		return "unknown"
	}
	count := binary.LittleEndian.Uint32(out[0:])
	if count == 0 {
		return "unknown"
	}
	if count > 1 {
		// 跨区卷 / 动态卷横跨多块盘：取哪一块都不能代表这个卷，判 unknown
		if qdThrottled("span:"+volDev, 5*time.Minute) {
			qdLogWarn("介质判定：%s 跨 %d 块盘，无法用单一介质代表", volDev, count)
		}
		return "unknown"
	}
	disk := binary.LittleEndian.Uint32(out[8:]) // DISK_EXTENT.DiskNumber
	dev := fmt.Sprintf(`\\.\PhysicalDrive%d`, disk)

	h, _, errno := pCreateFileW.Call(
		uintptr(unsafe.Pointer(utf16Ptr(dev))),
		0, 0x00000001|0x00000002, 0, 3, 0, 0,
	)
	if h == uintptr(syscall.InvalidHandle) || h == 0 {
		lastErr = errno
		return "unknown"
	}
	defer pCloseHandle.Call(h)
	m := mediaFromHandle(h)
	if qdThrottled("extent:"+volDev, 5*time.Minute) {
		qdLogInfo("介质判定：%s 落在 %s（磁盘号 %d）→ %s", volDev, dev, disk, m)
	}
	return m
}

// queryStorageProperty 发一次 IOCTL_STORAGE_QUERY_PROPERTY，返回输出缓冲。
// 注意：errno 必须取 Call 的第三返回值——事后再调 GetLastError 会被运行时的其他系统调用覆盖。
func queryStorageProperty(handle uintptr, propertyID uint32) ([]byte, bool) {
	in := storagePropertyQuery{PropertyID: propertyID, QueryType: propertyStandardQuery}
	out := make([]byte, 1024)
	var returned uint32
	ok, _, errno := pDeviceIoControl.Call(
		handle,
		uintptr(ioctlStorageQueryProperty),
		uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Sizeof(in)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)),
		uintptr(unsafe.Pointer(&returned)),
		0,
	)
	if ok == 0 {
		lastErr = errno
		return nil, false
	}
	if returned < 9 {
		lastErr = syscall.Errno(0)
		return nil, false
	}
	return out[:returned], true
}

// lastErr 记录最近一次失败的 errno，只为了在日志里带出来。
var lastErr error

// volumeOf 取路径所在卷（Windows 只看盘符）。
func volumeOf(path string) *Volume {
	if len(path) < 2 || path[1] != ':' {
		return nil
	}
	root := strings.ToUpper(path[:1]) + ":\\"
	for _, v := range listVolumesSorted() {
		if strings.EqualFold(v.Path, root) {
			vv := v
			return &vv
		}
	}
	return nil
}
