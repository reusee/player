package main

/*
#include <libavcodec/avcodec.h>
#include <SDL.h>
#include <SDL_ttf.h>
#cgo pkg-config: sdl2 SDL2_ttf

static inline Uint32 get_event_type(SDL_Event *ev) {
	return ev->type;
}
static inline SDL_KeyboardEvent get_event_key(SDL_Event *ev) {
	return ev->key;
}
static inline Uint32 get_userevent_code(SDL_Event *ev) {
	return ev->user.code;
}
static inline set_userevent(SDL_Event *ev, SDL_UserEvent ue) {
	ev->type = SDL_USEREVENT;
	ev->user = ue;
}

*/
import "C"
import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"time"
	"unsafe"
)

func init() {
	go http.ListenAndServe(":55559", nil)
}

func main() {
	runtime.GOMAXPROCS(32)

	// sdl
	C.SDL_Init(C.SDL_INIT_AUDIO | C.SDL_INIT_VIDEO | C.SDL_INIT_TIMER)
	defer C.SDL_Quit()
	runtime.LockOSThread()
	window := C.SDL_CreateWindow(C.CString("play"), 0, 0, 1680, 1050,
		C.SDL_WINDOW_BORDERLESS|C.SDL_WINDOW_RESIZABLE|C.SDL_WINDOW_MAXIMIZED|C.SDL_WINDOW_OPENGL)
	if window == nil {
		fatalSDLError()
	}
	defer C.SDL_DestroyWindow(window)
	C.SDL_DisableScreenSaver()
	renderer := C.SDL_CreateRenderer(window, -1, C.SDL_RENDERER_ACCELERATED)
	if renderer == nil {
		fatalSDLError()
	}
	defer C.SDL_DestroyRenderer(renderer)
	var width, height C.int
	C.SDL_GetWindowSize(window, &width, &height)
	texture := C.SDL_CreateTexture(renderer,
		C.SDL_PIXELFORMAT_YV12,
		C.SDL_TEXTUREACCESS_STREAMING,
		width, height)
	if texture == nil {
		fatalSDLError()
	}
	defer C.SDL_DestroyTexture(texture)

	// sdl ttf
	if C.TTF_Init() == C.int(-1) {
		log.Fatal("sdl ttf init failed")
	}
	defer C.TTF_Quit()
	font := C.TTF_OpenFont(C.CString("/home/reus/font.ttf"), 32)
	if font == nil {
		fatalTTFError()
	}
	defer C.TTF_CloseFont(font)

	// open decoder
	decoder, err := NewDecoder(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	if len(decoder.AudioStreams) == 0 || len(decoder.VideoStreams) == 0 {
		log.Fatal("no video or audio")
	}
	defer decoder.Close()

	// audio
	aCodecCtx := decoder.AudioStreams[0].codec
	setupAudioOutput(int(aCodecCtx.sample_rate), int(aCodecCtx.channels), decoder)

	// call closure in sdl thread
	callEventCode := C.SDL_RegisterEvents(1)
	callbacks := make(chan func(Env), 1024)
	call := func(f func(env Env)) {
		var event C.SDL_Event
		var userevent C.SDL_UserEvent
		userevent._type = C.SDL_USEREVENT
		userevent.code = C.Sint32(callEventCode)
		C.set_userevent(&event, userevent)
		callbacks <- f
		C.SDL_PushEvent(&event)
	}

	// show fps
	nFrames := 0
	var fpsTexture *C.SDL_Texture
	var fpsColor C.SDL_Color
	var fpsSrc, fpsDst C.SDL_Rect
	fpsColor.r = 255
	fpsColor.g = 255
	fpsColor.b = 255
	fpsColor.a = 0
	go func() {
		for _ = range time.NewTicker(time.Second * 1).C {
			call(func(env Env) {
				cText := C.CString(fmt.Sprintf("%d", nFrames))
				sur := C.TTF_RenderUTF8_Blended(font, cText, fpsColor)
				fpsSrc.w = sur.w
				fpsSrc.h = sur.h
				fpsDst.w = sur.w
				fpsDst.h = sur.h
				C.SDL_DestroyTexture(fpsTexture)
				fpsTexture = C.SDL_CreateTextureFromSurface(env.renderer, sur)
				C.SDL_FreeSurface(sur)
				C.free(unsafe.Pointer(cText))
				nFrames = 0
			})
		}
	}()

	// render
	go func() {
		for {
			frame := <-decoder.timedFrames
			nFrames++
			call(func(env Env) {
				C.SDL_UpdateYUVTexture(env.texture, nil,
					(*C.Uint8)(unsafe.Pointer(frame.data[0])), frame.linesize[0],
					(*C.Uint8)(unsafe.Pointer(frame.data[1])), frame.linesize[1],
					(*C.Uint8)(unsafe.Pointer(frame.data[2])), frame.linesize[2])
				C.SDL_RenderCopy(env.renderer, env.texture, nil, nil)
				C.SDL_RenderCopy(env.renderer, fpsTexture, &fpsSrc, &fpsDst)
				C.SDL_RenderPresent(env.renderer)
				decoder.RecycleFrame(frame)
			})
		}
	}()

	// start decode
	decoder.Start(decoder.VideoStreams[0], decoder.AudioStreams[0], width, height)

	// main loop
	var ev C.SDL_Event
	env := Env{
		window:   window,
		renderer: renderer,
		texture:  texture,
	}
	for {
		if C.SDL_WaitEvent(&ev) == C.int(0) {
			fatalSDLError()
		}
		switch C.get_event_type(&ev) {
		case C.SDL_QUIT:
			os.Exit(0)
		case C.SDL_KEYDOWN:
			key := C.get_event_key(&ev)
			switch key.keysym.sym {
			case C.SDLK_q: // quit
				os.Exit(0)
			case C.SDLK_f: // forward
				decoder.Seek(time.Second * 60)
			}
		case C.SDL_USEREVENT:
			if C.get_userevent_code(&ev) == callEventCode {
				(<-callbacks)(env)
			}
		}
	}
}

type Env struct {
	window   *C.SDL_Window
	renderer *C.SDL_Renderer
	texture  *C.SDL_Texture
}

func fatalSDLError() {
	log.Fatal(C.GoString(C.SDL_GetError()))
}

func fatalTTFError() {
	log.Fatal(C.GoString(C.TTF_GetError()))
}
