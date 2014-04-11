package main

/*
#include <portaudio.h>
*/
import "C"
import (
	"io"
	"reflect"
	"unsafe"
)

//export pa_callback
func pa_callback(input, output unsafe.Pointer, frameCount C.ulong,
	timeInfo *C.PaStreamCallbackTimeInfo, flags C.PaStreamCallbackFlags, data unsafe.Pointer) C.int {
	var s []byte
	h := (*reflect.SliceHeader)(unsafe.Pointer(&s))
	h.Len = int(frameCount * 8)
	h.Cap = h.Len
	h.Data = uintptr(output)
	env := (*paEnv)(data)
	env.buf.Lock()
	io.ReadFull(env.buf, s)
	env.buf.Unlock()
	curTime := timeInfo.currentTime
	outputTime := timeInfo.outputBufferDacTime
	p("%f %f %f\n", curTime, outputTime, outputTime-curTime)
	return C.paContinue
}
