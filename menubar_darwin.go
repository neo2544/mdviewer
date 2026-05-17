//go:build darwin

package main

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

extern void RegisterMdViewerOpenHandler(void);
*/
import "C"

//export goEnqueueOpenFile
func goEnqueueOpenFile(cpath *C.char) {
	path := C.GoString(cpath)
	if path == "" {
		return
	}
	// Non-blocking send so a flood of Apple Events can't deadlock the
	// Cocoa main thread; the channel is buffered for normal bursts.
	select {
	case openFileChan <- path:
	default:
	}
}

func registerOpenHandler() {
	C.RegisterMdViewerOpenHandler()
}
