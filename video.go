package main

/*
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libswscale/swscale.h>
#cgo pkg-config: libavutil libavformat libavcodec libswscale
*/
import "C"
import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"runtime"
	"time"
	"unsafe"
)

func init() {
	C.av_register_all()
}

type Video struct {
	FormatContext *C.AVFormatContext
	Streams       []*C.AVStream
	VideoStreams  []*C.AVStream
	AudioStreams  []*C.AVStream
	Codecs        []*C.AVCodec
}

func NewVideo(filename string) (*Video, error) {
	video := new(Video)

	// format context
	var formatContext *C.AVFormatContext
	if C.avformat_open_input(&formatContext, C.CString(filename), nil, nil) != C.int(0) {
		return nil, errors.New(fmt.Sprintf("can't open %d", filename))
	}
	if C.avformat_find_stream_info(formatContext, nil) < 0 {
		return nil, errors.New("find stream info error")
	}
	C.av_dump_format(formatContext, 0, C.CString(filename), 0)
	video.FormatContext = formatContext

	// streams
	var streams []*C.AVStream
	header := (*reflect.SliceHeader)(unsafe.Pointer(&streams))
	header.Cap = int(formatContext.nb_streams)
	header.Len = int(formatContext.nb_streams)
	header.Data = uintptr(unsafe.Pointer(formatContext.streams))
	video.Streams = streams
	for _, stream := range streams {
		switch stream.codec.codec_type {
		case C.AVMEDIA_TYPE_VIDEO:
			video.VideoStreams = append(video.VideoStreams, stream)
		case C.AVMEDIA_TYPE_AUDIO:
			video.AudioStreams = append(video.AudioStreams, stream)
		default: //TODO other stream
		}
	}

	// codecs
	for _, stream := range video.Streams {
		codec := C.avcodec_find_decoder(stream.codec.codec_id)
		if codec == nil {
			return nil, errors.New(fmt.Sprintf("no decoder for %v", stream.codec))
		}
		var options *C.AVDictionary
		if C.avcodec_open2(stream.codec, codec, &options) < 0 {
			return nil, errors.New(fmt.Sprintf("open codec error %v", stream.codec))
		}
	}

	return video, nil
}

func (self *Video) Close() {
	C.avformat_close_input(&self.FormatContext)
	for _, stream := range self.Streams {
		C.avcodec_close(stream.codec)
	}
}

type Decoder struct {
	Timer     *Timer
	frames    []*C.AVFrame
	buffers   []*C.uint8_t
	running   bool
	frameChan chan *C.AVFrame
	pool      chan *C.AVFrame
	seek_req  time.Duration
}

func (self *Video) Decode(videoStream, audioStream *C.AVStream,
	scaleWidth, scaleHeight C.int,
	videoFrameChan chan *C.AVFrame,
	audioBuffer *AudioBuf) *Decoder {

	decoder := &Decoder{
		running: true,
	}
	vCodecCtx := videoStream.codec
	aCodecCtx := audioStream.codec

	// frame pool
	poolSize := 16
	pool := make(chan *C.AVFrame, poolSize)
	decoder.pool = pool
	numBytes := C.size_t(C.avpicture_get_size(C.PIX_FMT_YUV420P, scaleWidth, scaleHeight))
	for i := 0; i < poolSize; i++ {
		frame := C.av_frame_alloc()
		decoder.frames = append(decoder.frames, frame)
		buffer := (*C.uint8_t)(unsafe.Pointer(C.av_malloc(numBytes)))
		decoder.buffers = append(decoder.buffers, buffer)
		C.avpicture_fill((*C.AVPicture)(unsafe.Pointer(frame)), buffer, C.PIX_FMT_YUV420P,
			scaleWidth, scaleHeight)
		pool <- frame
	}

	// decode
	frameChan := make(chan *C.AVFrame, 512)
	decoder.frameChan = frameChan
	var afterSeek bool // set to true after seek succeed
	go func() {
		runtime.LockOSThread()

		// scale context
		scaleContext := C.sws_getCachedContext(nil, vCodecCtx.width, vCodecCtx.height, vCodecCtx.pix_fmt,
			scaleWidth, scaleHeight, C.PIX_FMT_YUV420P, C.SWS_LANCZOS, nil, nil, nil)
		if scaleContext == nil {
			log.Fatal("get scale context failed")
		}

		var packet C.AVPacket
		var frameFinished, planeSize C.int
		var pts C.double
		vFrame := C.av_frame_alloc()
		aFrame := C.av_frame_alloc()
		videoIndex := videoStream.index
		audioIndex := audioStream.index

		decoder.Timer = NewTimer()
		var seekPosMilli int64
		var expectPosMilli int64
		// decode
		for decoder.running {
			if decoder.seek_req > 0 { // seek request
				seekPosMilli = int64(decoder.Timer.Now() / time.Millisecond)
				expectPosMilli = int64((decoder.Timer.Now() + decoder.seek_req) / time.Millisecond)
				fmt.Printf("expected %d\n", expectPosMilli)
			}
		seek:
			if decoder.seek_req > 0 {
				seekPosMilli += int64(decoder.seek_req/time.Millisecond) / 2
				if C.av_seek_frame(self.FormatContext, -1, C.int64_t(seekPosMilli/1000*C.AV_TIME_BASE),
					C.AVSEEK_FLAG_BACKWARD) < 0 {
					log.Fatal("seek error")
				}
				for _, stream := range self.Streams {
					C.avcodec_flush_buffers(stream.codec)
				}
			}

			if C.av_read_frame(self.FormatContext, &packet) < 0 { // read packet
				log.Fatal("read frame error")
			}

			if packet.stream_index == videoIndex { // video
				if C.avcodec_decode_video2(vCodecCtx, vFrame, &frameFinished, &packet) < 0 { // bad packet
					goto next
				}
				if packet.dts != C.AV_NOPTS_VALUE { // get pts
					pts = C.double(packet.dts)
				} else {
					panic("FIXME: no pts info") //TODO
				}
				pts *= C.av_q2d(videoStream.time_base) * 1000
				if decoder.seek_req > 0 { // check whether seek succeed
					if int64(pts) < expectPosMilli {
						fmt.Printf("got %d, seek again\n", int64(pts))
						goto seek // seek again
					} else {
						fmt.Printf("seek done at %d\n", int64(pts))
						decoder.seek_req = 0 // seek succeed
						decoder.Timer.Jump(time.Duration(pts)*time.Millisecond - decoder.Timer.Now())
						afterSeek = true
					}
				}
				if frameFinished > 0 { // got frame
					bufFrame := <-pool        // get frame buffer
					C.sws_scale(scaleContext, // scale
						&vFrame.data[0], &vFrame.linesize[0], 0, vCodecCtx.height,
						&bufFrame.data[0], &bufFrame.linesize[0]) //TODO bug here
					bufFrame.pts = C.int64_t(pts) // set pts
					frameChan <- bufFrame         // push to queue
				}

			} else if packet.stream_index == audioIndex { // audio
			decode:
				l := C.avcodec_decode_audio4(aCodecCtx, aFrame, &frameFinished, &packet)
				if l < 0 {
					goto next
				}
				if packet.dts != C.AV_NOPTS_VALUE { // get pts
					pts = C.double(packet.dts)
				} else {
					panic("FIXME: no pts info") //TODO
				}
				pts *= C.av_q2d(audioStream.time_base) * 1000
				if decoder.seek_req > 0 { // check whether seek succeed
					if int64(pts) < expectPosMilli {
						fmt.Printf("got %d, seek again\n", int64(pts))
						goto seek // seek again
					} else {
						fmt.Printf("seek done at %d\n", int64(pts))
						decoder.seek_req = 0 // seek succeed
						decoder.Timer.Jump(time.Duration(pts)*time.Millisecond - decoder.Timer.Now())
						afterSeek = true
					}
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
					for i := 0; i < int(planeSize); i += scalarSize {
						for ch := 0; ch < int(aCodecCtx.channels); ch++ {
							audioBuffer.Lock()
							audioBuffer.Write(planes[ch][i : i+scalarSize])
							audioBuffer.Unlock()
						}
					}
				}
				if l != packet.size { // multiple frame packet
					packet.size -= l
					packet.data = (*C.uint8_t)(unsafe.Pointer(uintptr(unsafe.Pointer(packet.data)) + uintptr(l)))
					goto decode
				}
			}

		next:
			C.av_free_packet(&packet)
		}
	}()

	// sync video
	go func() {
		//TODO jump timer when delta is bigger than max
		for frame := range frameChan {
			delta := time.Duration(frame.pts) - decoder.Timer.Now()/time.Millisecond
			//fmt.Printf("pts %d, timer %d, delta %d\n", frame.pts, decoder.Timer.Now()/time.Millisecond, delta)
			if delta > 0 {
				if afterSeek { // first frame after seek
					decoder.Timer.Jump(delta * time.Millisecond)
					print("timer jumped\n")
					afterSeek = false
				} else {
					time.Sleep(delta * time.Millisecond)
				}
			} else if delta < 0 { // drop frame
				decoder.RecycleFrame(frame)
				continue
			}
			videoFrameChan <- frame
		}
	}()

	return decoder
}

func (self *Decoder) Close() {
	for _, frame := range self.frames {
		C.av_frame_free(&frame)
	}
	for _, buffer := range self.buffers {
		C.av_free(unsafe.Pointer(buffer))
	}
	close(self.frameChan)
	self.running = false
}

func (self *Decoder) RecycleFrame(frame *C.AVFrame) {
	self.pool <- frame
}

func (self *Decoder) Seek(t time.Duration) {
	self.seek_req = t
}
