package main

import "C"
import (
	"io"
	"reflect"
	"unsafe"
)

var reader *AudioBuf
var header *reflect.SliceHeader

//export provideAudio
func provideAudio(readerP unsafe.Pointer, stream unsafe.Pointer, l C.int) {
	reader = (*AudioBuf)(readerP)
	var s []byte
	header = (*reflect.SliceHeader)(unsafe.Pointer(&s))
	header.Len = int(l)
	header.Cap = int(l)
	header.Data = uintptr(stream)
	reader.Lock()
	defer reader.Unlock()
	io.ReadFull(reader, s)
}
