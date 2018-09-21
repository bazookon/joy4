package main

import (
	"fmt"
	"sync"
	"io"
	"net/http"
	"github.com/nareix/joy4/format"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/av/avutil"
	"github.com/nareix/joy4/av/pubsub"
	"github.com/nareix/joy4/av/transcode"
	"github.com/nareix/joy4/format/rtmp"
	"github.com/nareix/joy4/format/flv"
	"github.com/nareix/joy4/cgo/ffmpeg"
)

func init() {
	format.RegisterAll()
}

type writeFlusher struct {
	httpflusher http.Flusher
	io.Writer
}

func (w writeFlusher) Flush() error {
	w.httpflusher.Flush()
	return nil
}

func findcodec(stream av.VideoCodecData, i int) (need bool, dec av.VideoDecoder, enc av.VideoEncoder, err error) {
	need = true
	dec, err = ffmpeg.NewVideoDecoder(stream)
	if err != nil {
		fmt.Println(err)
		return
	}
	if dec == nil {
		err = fmt.Errorf("Video decoder not found")
		return
	}

	enc, err = ffmpeg.NewVideoEncoderByCodecType(av.H264)
	if err != nil {
		fmt.Println(err)
		return
	}
	if enc == nil {
		err = fmt.Errorf("Video encoder not found")
		return
	}

	fpsNum, fpsDen := stream.Framerate()
	fmt.Println("input fps:", fpsNum, fpsDen)

	// Encoder config
	// Must be set from input stream
	enc.SetFramerate(fpsNum, fpsDen)
	// Configurable (can be set from input stream, or set by user and the input video will be converted before encoding)
	enc.SetResolution(352, 240)
	enc.SetPixelFormat(av.I420)
	// Must be configured by user
	enc.SetBitrate(1000000)
	enc.SetGopSize(fpsNum/fpsDen) // 1s gop
	return
}

func main() {
	fmt.Println("starting server")
	server := &rtmp.Server{}

	l := &sync.RWMutex{}
	type Channel struct {
		que *pubsub.Queue
	}
	channels := map[string]*Channel{}

	server.HandlePlay = func(conn *rtmp.Conn) {
		fmt.Println("HandlePlay()")
		l.RLock()
		ch := channels[conn.URL.Path]
		l.RUnlock()

		if ch != nil {
			cursor := ch.que.Latest()
			avutil.CopyFile(conn, cursor)
		}
	}

	server.HandlePublish = func(conn *rtmp.Conn) {
		fmt.Println("HandlePublish()")
		streams, _ := conn.Streams()

		l.Lock()
		ch := channels[conn.URL.Path]
		if ch == nil {
			ch = &Channel{}
			ch.que = pubsub.NewQueue()
			ch.que.WriteHeader(streams)
			channels[conn.URL.Path] = ch
		} else {
			ch = nil
		}
		l.Unlock()
		if ch == nil {
			return
		}

		trans := &transcode.Demuxer{
			Options: transcode.Options{
				FindVideoDecoderEncoder: findcodec,
			},
			Demuxer: conn,
		}

		avutil.CopyFile(ch.que, trans)

		fmt.Println("Leaving HandlePublish()")

		l.Lock()
		delete(channels, conn.URL.Path)
		l.Unlock()
		ch.que.Close()
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Request:", r.URL.Path)
		l.RLock()
		ch := channels[r.URL.Path]
		l.RUnlock()

		if ch != nil {
			w.Header().Set("Content-Type", "video/x-flv")
			w.Header().Set("Transfer-Encoding", "chunked")		
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(200)
			flusher := w.(http.Flusher)
			flusher.Flush()

			muxer := flv.NewMuxerWriteFlusher(writeFlusher{httpflusher: flusher, Writer: w})
			cursor := ch.que.Latest()

			avutil.CopyFile(muxer, cursor)
		} else {
			http.NotFound(w, r)
		}
	})

	http.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Request:", r.URL.Path)
		w.Header().Set("Content-Type", "video/x-flv")
		w.Header().Set("Transfer-Encoding", "chunked")		
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		flusher.Flush()

		file, _ := avutil.Open("/Users/Antoine/Documents/02-res/pointedugroin.mp4")

		trans := &transcode.Demuxer{
			Options: transcode.Options{
				FindVideoDecoderEncoder: findcodec,
			},
			Demuxer: file,
		}

		muxer := flv.NewMuxerWriteFlusher(writeFlusher{httpflusher: flusher, Writer: w})
		avutil.CopyFile(muxer, trans)
		file.Close()
	})

	go http.ListenAndServe(":8089", nil)
	server.ListenAndServe()
	fmt.Println("Done")

	// ffmpeg -re -i movie.flv -c copy -f flv rtmp://localhost/movie
	// ffmpeg -f avfoundation -i "0:0" .... -f flv rtmp://localhost/screen
	// ffplay http://localhost:8089/movie
	// ffplay http://localhost:8089/screen
}
