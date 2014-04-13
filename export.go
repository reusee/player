package main

/*
#include <portaudio.h>
*/
import "C"
import (
	"io"
	"reflect"
	"time"
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
	env.decoder.audios.Lock()
	io.ReadFull(env.decoder.audios, s)
	env.decoder.audios.Unlock()
	curTime := timeInfo.currentTime
	outputTime := timeInfo.outputBufferDacTime
	p("need audio %v later\n", time.Duration(float64(time.Second)*float64(outputTime-curTime)))
	return C.paContinue
}
