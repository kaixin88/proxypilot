package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// SysProxy 管理 Windows 系统代理（即「设置 → 网络和 Internet → 代理」）。
// 通过直接读写注册表 HKCU\...\Internet Settings 实现，避免引入第三方依赖。
var (
	advapi32           = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyEx   = advapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueEx = advapi32.NewProc("RegQueryValueExW")
	procRegSetValueEx  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValue = advapi32.NewProc("RegDeleteValueW")
	procRegCloseKey    = advapi32.NewProc("RegCloseKey")

	wininet            = syscall.NewLazyDLL("wininet.dll")
	procInternetSetOpt = wininet.NewProc("InternetSetOptionW")
)

const (
	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
	keySetValue     = 0x0002

	regSZ      = 1
	regDWORD   = 4

	internetOptionRefresh         = 37
	internetOptionSettingsChanged = 39

	regSubKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
)

type regKey struct{ h uintptr }

func regOpen() (regKey, error) {
	var h uintptr
	sub, err := syscall.UTF16PtrFromString(regSubKey)
	if err != nil {
		return regKey{}, err
	}
	ret, _, _ := procRegOpenKeyEx.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(sub)),
		0,
		uintptr(keyQueryValue|keySetValue),
		uintptr(unsafe.Pointer(&h)),
	)
	if ret != 0 {
		return regKey{}, fmt.Errorf("打开注册表失败, code=%d", ret)
	}
	return regKey{h}, nil
}

func (k regKey) close() {
	if k.h != 0 {
		procRegCloseKey.Call(k.h)
	}
}

func (k regKey) getString(name string) (string, bool) {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return "", false
	}
	var typ uint32
	var size uint32 = 4096
	buf := make([]uint16, size/2)
	ret, _, _ := procRegQueryValueEx.Call(
		k.h, uintptr(unsafe.Pointer(n)), 0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 || typ != regSZ {
		return "", false
	}
	return syscall.UTF16ToString(buf), true
}

func (k regKey) getDWORD(name string) (uint32, bool) {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, false
	}
	var typ uint32
	var val uint32
	size := uint32(4)
	ret, _, _ := procRegQueryValueEx.Call(
		k.h, uintptr(unsafe.Pointer(n)), 0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&val)),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 || typ != regDWORD {
		return 0, false
	}
	return val, true
}

func (k regKey) setString(name, value string) error {
	n, _ := syscall.UTF16PtrFromString(name)
	v, err := syscall.UTF16FromString(value)
	if err != nil {
		return err
	}
	ret, _, _ := procRegSetValueEx.Call(
		k.h, uintptr(unsafe.Pointer(n)), 0,
		uintptr(regSZ),
		uintptr(unsafe.Pointer(&v[0])),
		uintptr(len(v)*2),
	)
	if ret != 0 {
		return fmt.Errorf("写入注册表 %s 失败, code=%d", name, ret)
	}
	return nil
}

func (k regKey) setDWORD(name string, value uint32) error {
	n, _ := syscall.UTF16PtrFromString(name)
	v := value
	ret, _, _ := procRegSetValueEx.Call(
		k.h, uintptr(unsafe.Pointer(n)), 0,
		uintptr(regDWORD),
		uintptr(unsafe.Pointer(&v)),
		uintptr(4),
	)
	if ret != 0 {
		return fmt.Errorf("写入注册表 %s 失败, code=%d", name, ret)
	}
	return nil
}

func (k regKey) deleteValue(name string) {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return
	}
	procRegDeleteValue.Call(k.h, uintptr(unsafe.Pointer(n)))
}

// ---- 对外 API ----

type SysProxy struct{ mu sync.Mutex }

func NewSysProxy() *SysProxy { return &SysProxy{} }

const (
	valEnable   = "ProxyEnable"
	valServer   = "ProxyServer"
	valBypass   = "ProxyOverride"
	valAutoCfg  = "AutoConfigURL"
)

// ProxySnapshot 记录开启代理前的原始设置，用于关闭时精确还原。
type ProxySnapshot struct {
	Enable      uint32
	Server      string
	Bypass      string
	AutoCfg     string
	HasEnable   bool
	HasServer   bool
	HasBypass   bool
	HasAuto     bool
}

func (s *SysProxy) Snapshot() ProxySnapshot {
	var snap ProxySnapshot
	k, err := regOpen()
	if err != nil {
		return snap
	}
	defer k.close()

	if v, ok := k.getDWORD(valEnable); ok {
		snap.Enable, snap.HasEnable = v, true
	}
	if v, ok := k.getString(valServer); ok {
		snap.Server, snap.HasServer = v, true
	}
	if v, ok := k.getString(valBypass); ok {
		snap.Bypass, snap.HasBypass = v, true
	}
	if v, ok := k.getString(valAutoCfg); ok {
		snap.AutoCfg, snap.HasAuto = v, true
	}
	return snap
}

// Restore 还原到快照状态。
func (s *SysProxy) Restore(snap ProxySnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, err := regOpen()
	if err != nil {
		return err
	}
	defer k.close()

	if snap.HasServer {
		_ = k.setString(valServer, snap.Server)
	} else {
		k.deleteValue(valServer)
	}
	if snap.HasBypass {
		_ = k.setString(valBypass, snap.Bypass)
	} else {
		k.deleteValue(valBypass)
	}
	if snap.HasAuto {
		_ = k.setString(valAutoCfg, snap.AutoCfg)
	} else {
		k.deleteValue(valAutoCfg)
	}
	if snap.HasEnable {
		_ = k.setDWORD(valEnable, snap.Enable)
	} else {
		k.deleteValue(valEnable)
	}
	flushWininet()
	return nil
}

// SetGlobal 模式一：除本地与内网外，全部走 HTTP 代理。
func (s *SysProxy) SetGlobal(hostPort string, extraBypass []string) error {
	bypass := defaultBypass()
	bypass = append(bypass, extraBypass...)
	s.mu.Lock()
	defer s.mu.Unlock()

	k, err := regOpen()
	if err != nil {
		return err
	}
	defer k.close()

	k.deleteValue(valAutoCfg)
	if err := k.setString(valServer, hostPort); err != nil {
		return err
	}
	if len(bypass) > 0 {
		_ = k.setString(valBypass, strings.Join(bypass, ";"))
	}
	if err := k.setDWORD(valEnable, 1); err != nil {
		return err
	}
	flushWininet()
	return nil
}

// SetPAC 模式二/三：通过 PAC 脚本按规则决定是否走代理。
func (s *SysProxy) SetPAC(pacURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, err := regOpen()
	if err != nil {
		return err
	}
	defer k.close()

	k.deleteValue(valServer)
	_ = k.setDWORD(valEnable, 0)
	if err := k.setString(valAutoCfg, pacURL); err != nil {
		return err
	}
	flushWininet()
	return nil
}

// Off 关闭系统代理。
func (s *SysProxy) Off() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, err := regOpen()
	if err != nil {
		return err
	}
	defer k.close()

	_ = k.setDWORD(valEnable, 0)
	k.deleteValue(valAutoCfg)
	flushWininet()
	return nil
}

// Current 读取当前系统代理状态，用于界面回显。
func (s *SysProxy) Current() map[string]any {
	k, err := regOpen()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer k.close()
	enable, _ := k.getDWORD(valEnable)
	server, _ := k.getString(valServer)
	bypass, _ := k.getString(valBypass)
	autoCfg, _ := k.getString(valAutoCfg)
	return map[string]any{
		"enable":  enable == 1,
		"server":  server,
		"bypass":  bypass,
		"autoCfg": autoCfg,
	}
}

func flushWininet() {
	procInternetSetOpt.Call(0, uintptr(internetOptionSettingsChanged), 0, 0)
	procInternetSetOpt.Call(0, uintptr(internetOptionRefresh), 0, 0)
}

// defaultBypass 本地/内网直连例外列表。
func defaultBypass() []string {
	return []string{
		"localhost", "127.*", "10.*", "172.16.*", "172.17.*", "172.18.*", "172.19.*",
		"172.20.*", "172.21.*", "172.22.*", "172.23.*", "172.24.*", "172.25.*", "172.26.*",
		"172.27.*", "172.28.*", "172.29.*", "172.30.*", "172.31.*", "192.168.*", "169.254.*",
		"<local>",
	}
}
