//go:build windows

package main

import (
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// COM 接口 IID
var (
	_iidIDataObject = &syscall.GUID{0x0000010e, 0x0000, 0x0000, [8]byte{0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	_iidIDropSource = &syscall.GUID{0x00000121, 0x0000, 0x0000, [8]byte{0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	_iidIUnknownVal = syscall.GUID{0x00000000, 0x0000, 0x0000, [8]byte{0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

// 剪贴板格式
const (
	_cfHdrop        = 15
	_dropeffectCopy = 1
	_dropeffectLink = 4
)

// HRESULT / DragDrop
const (
	_sOk                        = 0x00000000
	_sFalse                     = 0x00000001
	_eNotimpl                   = 0x80004001
	_eInvalidarg                = 0x80070057
	_eOutofmemory               = 0x8007000E
	_dragdropSDrop              = 0x00040100
	_dragdropSCancel            = 0x00040101
	_dragdropSUsedefaultcursors = 0x00040102
)

// GMEM
const (
	_gmemMoveable = 0x0002
	_gmemZeroinit = 0x0040
)

// FORMATETC
type formatEtc struct {
	cfFormat uint16
	ptd      unsafe.Pointer
	dwAspect uint32
	lindex   int32
	tymed    uint32
}

// STGMEDIUM
type stgMedium struct {
	tymed          uint32
	union          unsafe.Pointer
	pUnkForRelease unsafe.Pointer
}

// DROPFILES
type dropFiles struct {
	pFiles uint32
	pt     struct{ x, y int32 }
	fNC    int32
	fWide  int32
}

// ==================== DLL ====================

var (
	_kernel32 = syscall.NewLazyDLL("kernel32.dll")
	_ole32    = syscall.NewLazyDLL("ole32.dll")

	_procGlobalAlloc  = _kernel32.NewProc("GlobalAlloc")
	_procGlobalLock   = _kernel32.NewProc("GlobalLock")
	_procGlobalUnlock = _kernel32.NewProc("GlobalUnlock")
	_procGlobalFree   = _kernel32.NewProc("GlobalFree")
	_procGlobalSize   = _kernel32.NewProc("GlobalSize")

	_procOleInitialize   = _ole32.NewProc("OleInitialize")
	_procOleUninitialize = _ole32.NewProc("OleUninitialize")
	_procDoDragDrop      = _ole32.NewProc("DoDragDrop")
)

// ==================== IDropSource ====================

type iDropSourceImpl struct {
	lpVtbl *iDropSourceVtbl
	ref    uint32
}

type iDropSourceVtbl struct {
	QueryInterface    uintptr
	AddRef            uintptr
	Release           uintptr
	QueryContinueDrag uintptr
	GiveFeedback      uintptr
}

func (d *iDropSourceImpl) vtable() *iDropSourceVtbl {
	return &iDropSourceVtbl{
		QueryInterface:    syscall.NewCallback(d.queryInterface),
		AddRef:            syscall.NewCallback(d.addRef),
		Release:           syscall.NewCallback(d.release),
		QueryContinueDrag: syscall.NewCallback(d.queryContinueDrag),
		GiveFeedback:      syscall.NewCallback(d.giveFeedback),
	}
}

func (d *iDropSourceImpl) queryInterface(this unsafe.Pointer, riid unsafe.Pointer, ppv unsafe.Pointer) uintptr {
	guid := (*syscall.GUID)(riid)
	if *guid == *_iidIDropSource || *guid == _iidIUnknownVal {
		*(*unsafe.Pointer)(ppv) = this
		d.addRef(this)
		return _sOk
	}
	*(*unsafe.Pointer)(ppv) = nil
	return _eNotimpl
}

func (d *iDropSourceImpl) addRef(this unsafe.Pointer) uintptr {
	d.ref++
	return uintptr(d.ref)
}

func (d *iDropSourceImpl) release(this unsafe.Pointer) uintptr {
	d.ref--
	return uintptr(d.ref)
}

func (d *iDropSourceImpl) queryContinueDrag(this unsafe.Pointer, fEscapePressed uintptr, grfKeyState uintptr) uintptr {
	if fEscapePressed != 0 {
		return _dragdropSCancel
	}
	if grfKeyState&0x0001 == 0 { // MK_LBUTTON 释放
		return _dragdropSDrop
	}
	return _sOk
}

func (d *iDropSourceImpl) giveFeedback(this unsafe.Pointer, dwEffect uintptr) uintptr {
	return _dragdropSUsedefaultcursors
}

// ==================== IDataObject ====================

type iDataObjectImpl struct {
	lpVtbl *iDataObjectVtbl
	ref    uint32
	hdrop  syscall.Handle
}

type iDataObjectVtbl struct {
	QueryInterface        uintptr
	AddRef                uintptr
	Release               uintptr
	GetData               uintptr
	GetDataHere           uintptr
	QueryGetData          uintptr
	GetCanonicalFormatEtc uintptr
	SetData               uintptr
	EnumFormatEtc         uintptr
	DAdvise               uintptr
	DUnadvise             uintptr
	EnumDAdvise           uintptr
}

func (d *iDataObjectImpl) vtable() *iDataObjectVtbl {
	return &iDataObjectVtbl{
		QueryInterface:        syscall.NewCallback(d.queryInterface),
		AddRef:                syscall.NewCallback(d.addRef),
		Release:               syscall.NewCallback(d.release),
		GetData:               syscall.NewCallback(d.getData),
		GetDataHere:           syscall.NewCallback(d.getDataHere),
		QueryGetData:          syscall.NewCallback(d.queryGetData),
		GetCanonicalFormatEtc: syscall.NewCallback(d.getCanonicalFormatEtc),
		SetData:               syscall.NewCallback(d.setData),
		EnumFormatEtc:         syscall.NewCallback(d.enumFormatEtc),
		DAdvise:               syscall.NewCallback(d.dAdvise),
		DUnadvise:             syscall.NewCallback(d.dUnadvise),
		EnumDAdvise:           syscall.NewCallback(d.enumDAdvise),
	}
}

func (d *iDataObjectImpl) queryInterface(this unsafe.Pointer, riid unsafe.Pointer, ppv unsafe.Pointer) uintptr {
	guid := (*syscall.GUID)(riid)
	if *guid == *_iidIDataObject || *guid == _iidIUnknownVal {
		*(*unsafe.Pointer)(ppv) = this
		d.addRef(this)
		return _sOk
	}
	*(*unsafe.Pointer)(ppv) = nil
	return _eNotimpl
}

func (d *iDataObjectImpl) addRef(this unsafe.Pointer) uintptr {
	d.ref++
	return uintptr(d.ref)
}

func (d *iDataObjectImpl) release(this unsafe.Pointer) uintptr {
	d.ref--
	if d.ref == 0 {
		if d.hdrop != 0 {
			_procGlobalFree.Call(uintptr(d.hdrop))
			d.hdrop = 0
		}
	}
	return uintptr(d.ref)
}

func (d *iDataObjectImpl) getData(this unsafe.Pointer, pFormatEtcIn unsafe.Pointer, pMedium unsafe.Pointer) uintptr {
	fe := (*formatEtc)(pFormatEtcIn)
	med := (*stgMedium)(pMedium)

	if fe.cfFormat != _cfHdrop || fe.tymed&1 == 0 { // TYMED_HGLOBAL
		return _eInvalidarg
	}

	size, _, _ := _procGlobalSize.Call(uintptr(d.hdrop))
	if size == 0 {
		return _eOutofmemory
	}

	gh, _, _ := _procGlobalAlloc.Call(_gmemMoveable|_gmemZeroinit, size)
	if gh == 0 {
		return _eOutofmemory
	}

	src, _, _ := _procGlobalLock.Call(uintptr(d.hdrop))
	dst, _, _ := _procGlobalLock.Call(gh)
	if src != 0 && dst != 0 {
		copy(unsafe.Slice((*byte)(unsafe.Pointer(dst)), size), unsafe.Slice((*byte)(unsafe.Pointer(src)), size))
	}
	if src != 0 {
		_procGlobalUnlock.Call(uintptr(d.hdrop))
	}
	if dst != 0 {
		_procGlobalUnlock.Call(gh)
	}

	med.tymed = 1 // TYMED_HGLOBAL
	med.union = unsafe.Pointer(gh)
	med.pUnkForRelease = nil
	return _sOk
}

func (d *iDataObjectImpl) getDataHere(this unsafe.Pointer, pFormatEtc unsafe.Pointer, pMedium unsafe.Pointer) uintptr {
	return _eNotimpl
}

func (d *iDataObjectImpl) queryGetData(this unsafe.Pointer, pFormatEtc unsafe.Pointer) uintptr {
	fe := (*formatEtc)(pFormatEtc)
	if fe.cfFormat == _cfHdrop {
		return _sOk
	}
	return _sFalse
}

func (d *iDataObjectImpl) getCanonicalFormatEtc(this unsafe.Pointer, pFormatEtcIn unsafe.Pointer, pFormatEtcOut unsafe.Pointer) uintptr {
	return _eNotimpl
}

func (d *iDataObjectImpl) setData(this unsafe.Pointer, pFormatEtc unsafe.Pointer, pMedium unsafe.Pointer, fRelease uintptr) uintptr {
	return _eNotimpl
}

func (d *iDataObjectImpl) enumFormatEtc(this unsafe.Pointer, dwDirection uintptr, ppEnumFormatEtc unsafe.Pointer) uintptr {
	*(*unsafe.Pointer)(ppEnumFormatEtc) = nil
	return _eNotimpl
}

func (d *iDataObjectImpl) dAdvise(this unsafe.Pointer, pFormatEtc unsafe.Pointer, advf uintptr, pAdvSink unsafe.Pointer, pdwConnection unsafe.Pointer) uintptr {
	return _eNotimpl
}

func (d *iDataObjectImpl) dUnadvise(this unsafe.Pointer, dwConnection uintptr) uintptr {
	return _eNotimpl
}

func (d *iDataObjectImpl) enumDAdvise(this unsafe.Pointer, ppEnumAdvise unsafe.Pointer) uintptr {
	*(*unsafe.Pointer)(ppEnumAdvise) = nil
	return _eNotimpl
}

// ==================== DROPFILES 构造 ====================

func makeDropFiles(filePath string) (syscall.Handle, error) {
	utf16Path, err := syscall.UTF16FromString(filePath)
	if err != nil {
		return 0, err
	}

	dfSize := uint32(unsafe.Sizeof(dropFiles{}))
	pathBytes := uint32(len(utf16Path) * 2)
	totalSize := dfSize + pathBytes + 2 // +2 for double null

	gh, _, err := _procGlobalAlloc.Call(_gmemMoveable|_gmemZeroinit, uintptr(totalSize))
	if gh == 0 {
		return 0, err
	}

	p, _, _ := _procGlobalLock.Call(gh)
	if p == 0 {
		_procGlobalFree.Call(gh)
		return 0, syscall.ENOMEM
	}

	df := (*dropFiles)(unsafe.Pointer(p))
	df.pFiles = dfSize
	df.fNC = 0
	df.fWide = 1

	pathStart := unsafe.Pointer(p + uintptr(dfSize))
	copy(unsafe.Slice((*byte)(pathStart), pathBytes), unsafe.Slice((*byte)(unsafe.Pointer(&utf16Path[0])), pathBytes))

	_procGlobalUnlock.Call(gh)
	return syscall.Handle(gh), nil
}

// ==================== 公开方法 ====================

var _dragInProgress atomic.Bool

// StartFileDrag 异步启动 Windows 原生文件拖拽，立即返回（防重入）
func (a *App) StartFileDrag(filePath string) {
	if _dragInProgress.Swap(true) {
		return // 已有拖拽进行中，跳过
	}
	go func() {
		defer _dragInProgress.Store(false)

		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		_procOleInitialize.Call(0)
		defer _procOleUninitialize.Call()

		hdrop, err := makeDropFiles(filePath)
		if err != nil {
			return
		}

		dataObj := &iDataObjectImpl{ref: 1, hdrop: hdrop}
		dataObj.lpVtbl = dataObj.vtable()

		dropSrc := &iDropSourceImpl{ref: 1}
		dropSrc.lpVtbl = dropSrc.vtable()

		var effect uint32
		_procDoDragDrop.Call(
			uintptr(unsafe.Pointer(dataObj)),
			uintptr(unsafe.Pointer(dropSrc)),
			uintptr(_dropeffectCopy),
			uintptr(unsafe.Pointer(&effect)),
		)

		dataObj.release(unsafe.Pointer(dataObj))
	}()
}
