package main

/*
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libswscale/swscale.h>
#include <SDL.h>
#include <SDL_ttf.h>
#cgo pkg-config: libavformat libavcodec libavutil libswscale sdl2 SDL2_ttf

Uint32 get_event_type(SDL_Event ev) {
	return ev.type;
}
SDL_KeyboardEvent get_event_key(SDL_Event ev) {
	return ev.key;
}
int get_userevent_code(SDL_Event ev) {
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

*/
import "C"
import (
	"fmt"
	"log"
	"os"
	"reflect"
	"runtime"
	"unsafe"
)

func main() {
	runtime.GOMAXPROCS(32)

	// ffmpeg
	C.av_register_all()

	fileName := C.CString(os.Args[1])

	var formatCtx *C.AVFormatContext
	if C.avformat_open_input(&formatCtx, fileName, nil, nil) != C.int(0) {
		log.Fatal("cannot open input")
	}
	if C.avformat_find_stream_info(formatCtx, nil) < 0 {
		log.Fatal("no stream")
	}
	C.av_dump_format(formatCtx, 0, fileName, 0)
	defer C.avformat_close_input(&formatCtx)

	var streams []*C.AVStream
	header := (*reflect.SliceHeader)(unsafe.Pointer(&streams))
	header.Cap = int(formatCtx.nb_streams)
	header.Len = int(formatCtx.nb_streams)
	header.Data = uintptr(unsafe.Pointer(formatCtx.streams))

	videoIndex := C.int(-1)
	for i, stream := range streams {
		if stream.codec.codec_type == C.AVMEDIA_TYPE_VIDEO {
			videoIndex = C.int(i)
			break
		}
	}
	if videoIndex < 0 {
		log.Fatal("no video stream")
	}

	codecCtx := streams[videoIndex].codec
	defer C.avcodec_close(codecCtx)
	codec := C.avcodec_find_decoder(codecCtx.codec_id)
	if codec == nil {
		log.Fatal("codec not found")
	}
	var options *C.AVDictionary
	if C.avcodec_open2(codecCtx, codec, &options) < 0 {
		log.Fatal("open codec error")
	}

	// sdl
	C.SDL_Init(C.SDL_INIT_AUDIO | C.SDL_INIT_VIDEO | C.SDL_INIT_TIMER)
	defer C.SDL_Quit()
	runtime.LockOSThread()
	window := C.SDL_CreateWindow(C.CString("play"), 0, 0, codecCtx.width, codecCtx.height,
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
	texture := C.SDL_CreateTexture(renderer,
		C.SDL_PIXELFORMAT_YV12,
		C.SDL_TEXTUREACCESS_STREAMING,
		codecCtx.width, codecCtx.height)
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

	running := true

	// decoder
	poolSize := 16
	framePool := make(chan *C.AVFrame, poolSize)
	frames := make(chan *C.AVFrame, poolSize)
	numBytes := C.size_t(C.avpicture_get_size(C.PIX_FMT_YUV420P, codecCtx.width, codecCtx.height))
	for i := 0; i < poolSize; i++ {
		frame := C.av_frame_alloc()
		buffer := (*C.uint8_t)(unsafe.Pointer(C.av_malloc(numBytes)))
		C.avpicture_fill((*C.AVPicture)(unsafe.Pointer(frame)), buffer, C.PIX_FMT_YUV420P,
			codecCtx.width, codecCtx.height)
		framePool <- frame
	}
	go func() {
		runtime.LockOSThread()
		swsCtx := C.sws_getContext(codecCtx.width, codecCtx.height, codecCtx.pix_fmt,
			codecCtx.width, codecCtx.height,
			C.PIX_FMT_YUV420P, C.SWS_BILINEAR,
			nil, nil, nil)
		if swsCtx == nil {
			log.Fatal("sws_getContext")
		}
		var packet C.AVPacket
		var frameFinished C.int
		frame := C.av_frame_alloc()
		for C.av_read_frame(formatCtx, &packet) >= 0 && running {
			if packet.stream_index != videoIndex {
				C.av_free_packet(&packet)
				continue
			}
			if C.avcodec_decode_video2(codecCtx, frame, &frameFinished, &packet) < C.int(0) {
				log.Fatal("decode error")
			}
			if frameFinished == C.int(0) {
				C.av_free_packet(&packet)
				continue
			}
			// scale
			vframe := <-framePool
			C.sws_scale(swsCtx,
				&frame.data[0], &frame.linesize[0], 0, codecCtx.height,
				&vframe.data[0], &vframe.linesize[0])
			frames <- vframe
			C.av_free_packet(&packet)
		}
	}()

	// render
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
		vframe := <-frames
		i++
		C.SDL_UpdateYUVTexture(texture, nil,
			(*C.Uint8)(unsafe.Pointer(vframe.data[0])), vframe.linesize[0],
			(*C.Uint8)(unsafe.Pointer(vframe.data[1])), vframe.linesize[1],
			(*C.Uint8)(unsafe.Pointer(vframe.data[2])), vframe.linesize[2])
		C.SDL_RenderCopy(renderer, texture, nil, nil)
		C.SDL_RenderCopy(renderer, fpsTexture, &fpsSrc, &fpsDst)
		C.SDL_RenderPresent(renderer)
		framePool <- vframe

		// sdl event
		if C.SDL_PollEvent(&ev) == C.int(0) {
			continue
		}
		switch C.get_event_type(ev) {
		case C.SDL_QUIT:
			running = false
		case C.SDL_KEYDOWN:
			key := C.get_event_key(ev)
			switch key.keysym.sym {
			case C.SDLK_q:
				running = false
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
