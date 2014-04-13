package main

/*
#include <portaudio.h>
#cgo pkg-config: portaudio-2.0

extern int pa_callback(const void*, void*, unsigned long, const PaStreamCallbackTimeInfo*,
	PaStreamCallbackFlags, void*);

PaStream* pa_open_stream(int chans, int rate, void* env) {
	PaStream *stream;
	PaError err = Pa_OpenDefaultStream(&stream, 0, chans, paFloat32, rate, 256,
		pa_callback, env);
	if (err != paNoError) {
		return 0;
	}
	return stream;
}

*/
import "C"
import (
	"log"
	"unsafe"
)

type paEnv struct {
	decoder *Decoder
	stream  unsafe.Pointer
}

var env *paEnv

func setupAudioOutput(rate int, nChannels int, decoder *Decoder) {
	if err := C.Pa_Initialize(); err != C.paNoError {
		fatalPAError(err)
	}
	env = &paEnv{
		decoder: decoder,
	}
	stream := C.pa_open_stream(C.int(nChannels), C.int(rate), unsafe.Pointer(env))
	if stream == nil {
		log.Fatal("pa open stream")
	}
	env.stream = stream
	err := C.Pa_StartStream(unsafe.Pointer(stream))
	if err != C.paNoError {
		fatalPAError(err)
	}
}

func fatalPAError(err C.PaError) {
	log.Fatal(C.GoString(C.Pa_GetErrorText(err)))
}
