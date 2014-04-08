package main

/*
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libswscale/swscale.h>
#include <SDL.h>
#include <SDL_ttf.h>
#cgo pkg-config: libavformat libavcodec libavutil libswscale sdl2 SDL2_ttf

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
	"reflect"
	"runtime"
	"sync"
	"unsafe"
)

type AudioBuf struct {
	*bytes.Buffer
	sync.Mutex
}

func init() {
	go http.ListenAndServe(":55559", nil)
}

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
	audioIndex := C.int(-1)
	for i, stream := range streams {
		if stream.codec.codec_type == C.AVMEDIA_TYPE_VIDEO && videoIndex == -1 {
			videoIndex = C.int(i)
		} else if stream.codec.codec_type == C.AVMEDIA_TYPE_AUDIO && audioIndex == -1 {
			audioIndex = C.int(i)
		}
	}
	if videoIndex == -1 {
		log.Fatal("no video stream")
	}
	if audioIndex == -1 {
		log.Fatal("no audio stream")
	}

	// video codec
	vCodecCtx := streams[videoIndex].codec
	defer C.avcodec_close(vCodecCtx)
	codec := C.avcodec_find_decoder(vCodecCtx.codec_id)
	if codec == nil {
		log.Fatal("codec not found")
	}
	var options *C.AVDictionary
	if C.avcodec_open2(vCodecCtx, codec, &options) < 0 {
		log.Fatal("open codec error")
	}

	// audio codec
	aCodecCtx := streams[audioIndex].codec
	aCodec := C.avcodec_find_decoder(aCodecCtx.codec_id)
	if aCodec == nil {
		log.Fatal("audio codec not found")
	}
	if C.avcodec_open2(aCodecCtx, aCodec, &options) < 0 {
		log.Fatal("open codec error")
	}

	// sdl
	C.SDL_Init(C.SDL_INIT_AUDIO | C.SDL_INIT_VIDEO | C.SDL_INIT_TIMER)
	defer C.SDL_Quit()
	runtime.LockOSThread()
	window := C.SDL_CreateWindow(C.CString("play"), 0, 0, vCodecCtx.width, vCodecCtx.height,
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
		vCodecCtx.width, vCodecCtx.height)
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

	// sdl audio
	var wantedSpec, spec C.SDL_AudioSpec
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
	audioBuf := &AudioBuf{Buffer: new(bytes.Buffer)}
	C.setup_audio_callback(&wantedSpec, unsafe.Pointer(audioBuf))
	dev := C.SDL_OpenAudioDevice(nil, 0, &wantedSpec, &spec, C.SDL_AUDIO_ALLOW_ANY_CHANGE)
	if dev == 0 {
		fatalSDLError()
	}
	fmt.Printf("%v\n%v\n", wantedSpec, spec)
	defer C.SDL_CloseAudioDevice(dev)
	C.SDL_PauseAudioDevice(dev, 0)

	running := true

	// decoder
	poolSize := 16
	framePool := make(chan *C.AVFrame, poolSize)
	frames := make(chan *C.AVFrame, poolSize)
	numBytes := C.size_t(C.avpicture_get_size(C.PIX_FMT_YUV420P, vCodecCtx.width, vCodecCtx.height))
	for i := 0; i < poolSize; i++ {
		frame := C.av_frame_alloc()
		buffer := (*C.uint8_t)(unsafe.Pointer(C.av_malloc(numBytes)))
		C.avpicture_fill((*C.AVPicture)(unsafe.Pointer(frame)), buffer, C.PIX_FMT_YUV420P,
			vCodecCtx.width, vCodecCtx.height)
		framePool <- frame
	}
	go func() {
		runtime.LockOSThread()
		swsCtx := C.sws_getContext(vCodecCtx.width, vCodecCtx.height, vCodecCtx.pix_fmt,
			vCodecCtx.width, vCodecCtx.height,
			C.PIX_FMT_YUV420P, C.SWS_BILINEAR,
			nil, nil, nil)
		if swsCtx == nil {
			log.Fatal("sws_getContext")
		}
		var packet C.AVPacket
		var frameFinished, planeSize C.int
		frame := C.av_frame_alloc()
		aFrame := C.av_frame_alloc()
		for C.av_read_frame(formatCtx, &packet) >= 0 && running {
			// video packet
			if packet.stream_index == videoIndex {
				if C.avcodec_decode_video2(vCodecCtx, frame, &frameFinished, &packet) < C.int(0) {
					log.Fatal("video decode error")
				}
				if frameFinished > 0 {
					vframe := <-framePool
					C.sws_scale(swsCtx,
						&frame.data[0], &frame.linesize[0], 0, vCodecCtx.height,
						&vframe.data[0], &vframe.linesize[0])
					frames <- vframe
				}
				// audio packet
			} else if packet.stream_index == audioIndex {
			decode:
				l := C.avcodec_decode_audio4(aCodecCtx, aFrame, &frameFinished, &packet)
				if l < 0 {
					log.Fatal("audio decode error")
				}
				if frameFinished > 0 {
					C.av_samples_get_buffer_size(&planeSize, aCodecCtx.channels, aFrame.nb_samples,
						int32(aCodecCtx.sample_fmt), 1)
					var planePointers []*byte
					var planes [][]byte
					h := (*reflect.SliceHeader)(unsafe.Pointer(&planePointers))
					h.Len = int(aCodecCtx.channels)
					h.Cap = h.Len
					h.Data = uintptr(unsafe.Pointer(aFrame.extended_data))
					for i := 0; i < int(aCodecCtx.channels); i++ {
						var plane []byte
						h := (*reflect.SliceHeader)(unsafe.Pointer(&plane))
						h.Len = int(planeSize)
						h.Cap = h.Len
						h.Data = uintptr(unsafe.Pointer(planePointers[i]))
						planes = append(planes, plane)
					}
					var scalarSize int
					switch aCodecCtx.sample_fmt {
					case C.AV_SAMPLE_FMT_FLTP:
						scalarSize = 4
					case C.AV_SAMPLE_FMT_S16P:
						scalarSize = 2
					default:
						panic("unknown audio sample format") //TODO
					}
					audioBuf.Lock()
					for i := 0; i < int(planeSize); i += scalarSize {
						for ch := 0; ch < int(aCodecCtx.channels); ch++ {
							audioBuf.Write(planes[ch][i : i+scalarSize])
						}
					}
					audioBuf.Unlock()
				}
				if l != packet.size { // multiple frame packet
					packet.size -= l
					packet.data = (*C.uint8_t)(unsafe.Pointer(uintptr(unsafe.Pointer(packet.data)) + uintptr(l)))
					goto decode
				}
			}
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
