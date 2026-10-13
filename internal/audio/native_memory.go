package audio

/*
#include <stdlib.h>
*/
import "C"

import "unsafe"

// freeNativeBuffer releases a temporary C buffer allocated by malgo.DeviceID.Pointer.
// The audio backend copies the selected device ID during InitDevice, so the
// temporary buffer can be freed as soon as InitDevice returns.
func freeNativeBuffer(ptr unsafe.Pointer) {
	if ptr != nil {
		C.free(ptr)
	}
}
