package main

/*
#include <portaudio.h>
*/
import "C"
import (
	"reflect"
	"time"
	"unsafe"
)

//export pa_callback
func pa_callback(input, output unsafe.Pointer, frameCount C.ulong,
	timeInfo *C.PaStreamCallbackTimeInfo, flags C.PaStreamCallbackFlags, data unsafe.Pointer) C.int {
	env := (*paEnv)(data)

	// set output slice
	bytesToRead := int(frameCount * 8)
	var s []byte
	h := (*reflect.SliceHeader)(unsafe.Pointer(&s))
	h.Len = bytesToRead
	h.Cap = bytesToRead
	h.Data = uintptr(output)

	//now := env.decoder.Timer.Now()
	//expected := now + time.Duration(float64(time.Second)*float64(timeInfo.outputBufferDacTime-timeInfo.currentTime))

	cachedFrame := env.decoder.cachedAudioFrame
	if cachedFrame != nil { // read from cache
		n := copyFromFrame(cachedFrame, env.decoder, s)
		bytesToRead -= n
		s = s[n:]
		if len(cachedFrame.data) == 0 {
			env.decoder.cachedAudioFrame = nil
		}
	}

	if bytesToRead > 0 {
		frame := <-env.decoder.audioFrames
		if len(frame.data) < len(s) {
			panic("FIXME")
		}
		n := copyFromFrame(frame, env.decoder, s)
		bytesToRead -= n
		s = s[n:]
		if len(frame.data) > 0 { // cache
			env.decoder.cachedAudioFrame = frame
		}
	}

	return C.paContinue
}

func copyFromFrame(frame *AudioFrame, decoder *Decoder, s []byte) int {
	bytesToCopy := len(s)
	if len(frame.data) < bytesToCopy {
		bytesToCopy = len(frame.data)
	}
	copy(s[:bytesToCopy], frame.data[:bytesToCopy])
	s = s[bytesToCopy:]
	frame.data = frame.data[bytesToCopy:]
	frame.time += time.Duration(bytesToCopy/8) * decoder.durationPerSample
	return bytesToCopy
}
