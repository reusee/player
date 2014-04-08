package main

/*
#include <libavcodec/avcodec.h>
#include <SDL.h>
#include <SDL_ttf.h>
#cgo pkg-config: sdl2 SDL2_ttf

static inline Uint32 get_event_type(SDL_Event ev) {
	return ev.type;
}
static inline SDL_KeyboardEvent get_event_key(SDL_Event ev) {
	return ev.key;
}
static inline int get_userevent_code(SDL_Event ev) {
	return ev.user.code;
}

Uint32 ticker(Uint32 interval, void* param) {
	SDL_Event event;
	SDL_UserEvent userevent;
	userevent.type = SDL_USEREVENT;
	userevent.code = *((int*)param);
	userevent.data1 = NULL;
	userevent.data2 = NULL;
	event.type = SDL_USEREVENT;
	event.user = userevent;
	SDL_PushEvent(&event);
	return (interval);
}
int add_ticker(int interval) {
	int code = SDL_RegisterEvents(1);
	int* c = SDL_malloc(sizeof(int));
	*c = code;
	SDL_AddTimer(interval, ticker, c);
	return code;
}

static inline void audio_callback(void *userdata, Uint8 *stream, int len) {
	provideAudio(userdata, stream, len);
}
void setup_audio_callback(SDL_AudioSpec *spec, void *bufferp) {
	spec->callback = audio_callback;
	spec->userdata = bufferp;
}

*/
import "C"
import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

func init() {
	go http.ListenAndServe(":55559", nil)
}

type AudioBuf struct {
	*bytes.Buffer
	sync.Mutex
}

func main() {
	runtime.GOMAXPROCS(32)

	// sdl
	C.SDL_Init(C.SDL_INIT_AUDIO | C.SDL_INIT_VIDEO | C.SDL_INIT_TIMER)
	defer C.SDL_Quit()
	runtime.LockOSThread()
	window := C.SDL_CreateWindow(C.CString("play"), 0, 0, 1024, 768,
		C.SDL_WINDOW_BORDERLESS|C.SDL_WINDOW_RESIZABLE|C.SDL_WINDOW_MAXIMIZED)
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

	// open video
	video, err := NewVideo(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	if len(video.AudioStreams) == 0 || len(video.VideoStreams) == 0 {
		log.Fatal("no video or audio")
	}
	defer video.Close()

	// audio
	audioBuf := &AudioBuf{Buffer: new(bytes.Buffer)}

	// sdl audio
	var wantedSpec, spec C.SDL_AudioSpec
	aCodecCtx := video.AudioStreams[0].codec
	wantedSpec.freq = aCodecCtx.sample_rate
	switch aCodecCtx.sample_fmt {
	case C.AV_SAMPLE_FMT_FLTP:
		wantedSpec.format = C.AUDIO_F32SYS
	case C.AV_SAMPLE_FMT_S16P:
		wantedSpec.format = C.AUDIO_S16SYS
	default:
		panic("unknown audio sample format")
	}
	wantedSpec.channels = C.Uint8(aCodecCtx.channels)
	wantedSpec.samples = 4096
	C.setup_audio_callback(&wantedSpec, unsafe.Pointer(audioBuf))
	dev := C.SDL_OpenAudioDevice(nil, 0, &wantedSpec, &spec, C.SDL_AUDIO_ALLOW_ANY_CHANGE)
	if dev == 0 {
		fatalSDLError()
	}
	fmt.Printf("%v\n%v\n", wantedSpec, spec)
	defer C.SDL_CloseAudioDevice(dev)
	C.SDL_PauseAudioDevice(dev, 0)

	// decode
	timedFrames := make(chan *C.AVFrame)
	decoder := video.Decode(video.VideoStreams[0], video.AudioStreams[0],
		width, height,
		timedFrames,
		audioBuf)
	defer decoder.Close()

	// render
	running := true
	var ev C.SDL_Event
	fpsEventCode := C.add_ticker(1000)
	i := 0
	var fpsTexture *C.SDL_Texture
	var fpsColor C.SDL_Color
	var fpsSrc, fpsDst C.SDL_Rect
	fpsColor.r = 255
	fpsColor.g = 255
	fpsColor.b = 255
	fpsColor.a = 0
	for running {
		// render frame
		frame := <-timedFrames
		i++
		C.SDL_UpdateYUVTexture(texture, nil,
			(*C.Uint8)(unsafe.Pointer(frame.data[0])), frame.linesize[0],
			(*C.Uint8)(unsafe.Pointer(frame.data[1])), frame.linesize[1],
			(*C.Uint8)(unsafe.Pointer(frame.data[2])), frame.linesize[2])
		C.SDL_RenderCopy(renderer, texture, nil, nil)
		C.SDL_RenderCopy(renderer, fpsTexture, &fpsSrc, &fpsDst)
		C.SDL_RenderPresent(renderer)
		decoder.RecycleFrame(frame)

		// sdl event
		if C.SDL_PollEvent(&ev) == C.int(0) {
			continue
		}
		switch C.get_event_type(ev) {
		case C.SDL_QUIT:
			running = false
			os.Exit(0)
		case C.SDL_KEYDOWN:
			key := C.get_event_key(ev)
			switch key.keysym.sym {
			case C.SDLK_q:
				running = false
				os.Exit(0)
			}
		case C.SDL_USEREVENT:
			if C.get_userevent_code(ev) == fpsEventCode {
				cText := C.CString(fmt.Sprintf("%d", i))
				sur := C.TTF_RenderUTF8_Blended(font, cText, fpsColor)
				fpsSrc.w = sur.w
				fpsSrc.h = sur.h
				fpsDst.w = sur.w
				fpsDst.h = sur.h
				C.SDL_DestroyTexture(fpsTexture)
				fpsTexture = C.SDL_CreateTextureFromSurface(renderer, sur)
				C.SDL_FreeSurface(sur)
				C.free(unsafe.Pointer(cText))
				i = 0
			}
		}
	}
}

func fatalSDLError() {
	log.Fatal(C.GoString(C.SDL_GetError()))
}

func fatalTTFError() {
	log.Fatal(C.GoString(C.TTF_GetError()))
}
