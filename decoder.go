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

type Decoder struct {
	// demux
	FormatContext *C.AVFormatContext
	Streams       []*C.AVStream
	VideoStreams  []*C.AVStream
	AudioStreams  []*C.AVStream
	Codecs        []*C.AVCodec
	openedCodecs  []*C.AVCodecContext

	// decode
	Timer     *Timer
	frames    []*C.AVFrame
	buffers   []*C.uint8_t
	running   bool
	frameChan chan *C.AVFrame
	pool      chan *C.AVFrame
	duration  time.Duration

	// seek
	seekTarget time.Duration
	seekStep   time.Duration
	seekNext   time.Duration
}

func NewDecoder(filename string) (*Decoder, error) {
	self := new(Decoder)

	// format context
	var formatContext *C.AVFormatContext
	if C.avformat_open_input(&formatContext, C.CString(filename), nil, nil) != C.int(0) {
		return nil, errors.New(fmt.Sprintf("can't open %d", filename))
	}
	if C.avformat_find_stream_info(formatContext, nil) < 0 {
		return nil, errors.New("find stream info error")
	}
	C.av_dump_format(formatContext, 0, C.CString(filename), 0)
	self.FormatContext = formatContext

	// streams
	var streams []*C.AVStream
	header := (*reflect.SliceHeader)(unsafe.Pointer(&streams))
	header.Cap = int(formatContext.nb_streams)
	header.Len = int(formatContext.nb_streams)
	header.Data = uintptr(unsafe.Pointer(formatContext.streams))
	self.Streams = streams
	for _, stream := range streams {
		switch stream.codec.codec_type {
		case C.AVMEDIA_TYPE_VIDEO:
			self.VideoStreams = append(self.VideoStreams, stream)
		case C.AVMEDIA_TYPE_AUDIO:
			self.AudioStreams = append(self.AudioStreams, stream)
		default: //TODO other stream
		}
	}

	// codecs
	for _, stream := range self.Streams {
		codec := C.avcodec_find_decoder(stream.codec.codec_id)
		if codec == nil {
			continue
		}
		var options *C.AVDictionary
		if C.avcodec_open2(stream.codec, codec, &options) < 0 {
			return nil, errors.New(fmt.Sprintf("open codec error %v", stream.codec))
		}
		self.openedCodecs = append(self.openedCodecs, stream.codec)
	}

	return self, nil
}

func (self *Decoder) Close() {
	C.avformat_close_input(&self.FormatContext)
	for _, stream := range self.Streams {
		C.avcodec_close(stream.codec)
	}
	for _, frame := range self.frames {
		C.av_frame_free(&frame)
	}
	for _, buffer := range self.buffers {
		C.av_free(unsafe.Pointer(buffer))
	}
	close(self.frameChan)
	self.running = false
}

func (self *Decoder) Start(videoStream, audioStream *C.AVStream,
	scaleWidth, scaleHeight C.int,
	videoFrameChan chan *C.AVFrame,
	audioBuffer *AudioBuf) *Decoder {

	self.running = true
	self.Timer = NewTimer()
	self.duration = time.Duration(self.FormatContext.duration * C.AV_TIME_BASE / 1000)
	vCodecCtx := videoStream.codec
	aCodecCtx := audioStream.codec

	// frame pool
	poolSize := 16
	pool := make(chan *C.AVFrame, poolSize)
	self.pool = pool
	numBytes := C.size_t(C.avpicture_get_size(C.PIX_FMT_YUV420P, scaleWidth, scaleHeight))
	for i := 0; i < poolSize; i++ {
		frame := C.av_frame_alloc()
		self.frames = append(self.frames, frame)
		buffer := (*C.uint8_t)(unsafe.Pointer(C.av_malloc(numBytes)))
		self.buffers = append(self.buffers, buffer)
		C.avpicture_fill((*C.AVPicture)(unsafe.Pointer(frame)), buffer, C.PIX_FMT_YUV420P,
			scaleWidth, scaleHeight)
		pool <- frame
	}

	// decode
	self.frameChan = make(chan *C.AVFrame, 512)
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
		var pts int64
		var packetTime time.Duration
		vFrame := C.av_frame_alloc()
		aFrame := C.av_frame_alloc()
		videoIndex := videoStream.index
		audioIndex := audioStream.index

		// decode
		for self.running {

			// seek
			if self.seekTarget > 0 {
				if C.av_seek_frame(self.FormatContext, -1,
					C.int64_t(float64(self.seekNext)/float64(time.Second)*float64(C.AV_TIME_BASE)),
					C.AVSEEK_FLAG_BACKWARD) < 0 {
					log.Fatal("seek error")
				}
				for _, codecCtx := range self.openedCodecs {
					C.avcodec_flush_buffers(codecCtx)
				}
				p("frame seek done\n")
			}

		read_packet:
			// read packet
			C.av_free_packet(&packet)
			if C.av_read_frame(self.FormatContext, &packet) < 0 { // read packet
				log.Fatal("read frame error") //TODO stop gracefully
			}

			// get packet time
			if packet.dts != C.AV_NOPTS_VALUE {
				pts = int64(packet.dts)
			} else {
				pts = 0
			}
			if packet.stream_index == videoIndex {
				packetTime = time.Duration(float64(pts) * float64(C.av_q2d(videoStream.time_base)) * float64(time.Second))
			} else if packet.stream_index == audioIndex {
				packetTime = time.Duration(float64(pts) * float64(C.av_q2d(audioStream.time_base)) * float64(time.Second))
			} else { // ignore packet
				goto read_packet
			}
			p("packet time %v\n", packetTime)

			// check seek
			if self.seekTarget > 0 && packetTime > 0 { // if packet time cannot determined, skip
				if packetTime < self.seekTarget { // seek again
					self.seekNext += self.seekStep
					p("seek again %v\n", self.seekNext)
				} else { // no further seek
					p("seek ok\n")
					self.seekTarget = 0
				}
			}

			// decode
			if packet.stream_index == videoIndex { // decode video
				if C.avcodec_decode_video2(vCodecCtx, vFrame, &frameFinished, &packet) < 0 {
					continue // bad packet
				}
				if frameFinished <= 0 {
					goto read_packet // frame not complete
				}
				bufFrame := <-pool        // get frame buffer
				C.sws_scale(scaleContext, // scale
					&vFrame.data[0], &vFrame.linesize[0], 0, vCodecCtx.height,
					&bufFrame.data[0], &bufFrame.linesize[0]) //TODO bug here
				bufFrame.pts = C.int64_t(packetTime) // set packet time
				self.frameChan <- bufFrame        // push to queue

			} else if packet.stream_index == audioIndex { // decode audio
			decode_audio_packet:
				l := C.avcodec_decode_audio4(aCodecCtx, aFrame, &frameFinished, &packet)
				if l < 0 {
					continue // bad packet
				}
				if frameFinished <= 0 {
					goto read_packet // frame not complete
				}
				if frameFinished > 0 { // frame finished
					var planePointers []*byte
					h := (*reflect.SliceHeader)(unsafe.Pointer(&planePointers))
					h.Len = int(aCodecCtx.channels)
					h.Cap = h.Len
					h.Data = uintptr(unsafe.Pointer(aFrame.extended_data))
					var scalarSize int
					switch aCodecCtx.sample_fmt {
					case C.AV_SAMPLE_FMT_FLTP: // planar format
						scalarSize = 4
						fallthrough
					case C.AV_SAMPLE_FMT_S16P:
						scalarSize = 2
						C.av_samples_get_buffer_size(&planeSize, aCodecCtx.channels, aFrame.nb_samples,
							int32(aCodecCtx.sample_fmt), 1)
						var planes [][]byte
						for i := 0; i < int(aCodecCtx.channels); i++ {
							var plane []byte
							h := (*reflect.SliceHeader)(unsafe.Pointer(&plane))
							h.Len = int(planeSize)
							h.Cap = h.Len
							h.Data = uintptr(unsafe.Pointer(planePointers[i]))
							planes = append(planes, plane)
						}
						audioBuffer.Lock()
						for i := 0; i < int(planeSize); i += scalarSize {
							for ch := 0; ch < int(aCodecCtx.channels); ch++ {
								audioBuffer.Write(planes[ch][i : i+scalarSize])
							}
						}
						audioBuffer.Unlock()
					case C.AV_SAMPLE_FMT_S32:
						audioBuffer.Lock()
						audioBuffer.Write(C.GoBytes(unsafe.Pointer(planePointers[0]), aFrame.linesize[0]))
						audioBuffer.Unlock()
					default:
						panic("unknown audio sample format") //TODO
					}
				}
				if l != packet.size { // multiple frame packet
					packet.size -= l
					packet.data = (*C.uint8_t)(unsafe.Pointer(uintptr(unsafe.Pointer(packet.data)) + uintptr(l)))
					goto decode_audio_packet
				}

			} else { // other stream
				goto read_packet
			}

		}
	}()

	// sync video
	go func() {
		maxDelta := (time.Second / time.Duration(C.av_q2d(videoStream.r_frame_rate)/2))
		for frame := range self.frameChan {
			delta := time.Duration(frame.pts) - self.Timer.Now()
			p("frame %v, delta %v, max delta %v\n", time.Duration(frame.pts), delta, maxDelta)
			if delta > 0 {
				if delta > maxDelta {
					self.Timer.Jump(delta)
					print("timer jumped\n")
				} else {
					time.Sleep(delta)
				}
			} else if delta < 0 { // drop frame
				self.RecycleFrame(frame)
				continue
			}
			videoFrameChan <- frame
		}
	}()

	return self
}

func (self *Decoder) RecycleFrame(frame *C.AVFrame) {
	self.pool <- frame
}

func (self *Decoder) Seek(t time.Duration) {
	self.seekStep = t / 2
	self.seekTarget = self.Timer.Now() + t
	if self.seekTarget > self.duration {
		self.seekTarget = 0
		return
	}
	self.seekNext = self.seekTarget
	p("start seek to %v, step %v, next %v\n", self.seekTarget, self.seekStep, self.seekNext)
}
