package main

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"
	xphone "github.com/x-phone/xphone-go"
)

// audioHandler manages speaker output and microphone input for a call.
type audioHandler struct {
	mu      sync.Mutex
	call    xphone.Call
	ctx     *malgo.AllocatedContext
	speaker *malgo.Device
	mic     *malgo.Device
	stop    chan struct{}

	speakerActive atomic.Bool
	micActive     atomic.Bool
	echoActive    atomic.Bool

	// Speaker sample queue (protected by sqMu).
	// Kept small to minimize latency — holds raw int16 samples.
	sqMu  sync.Mutex
	sqBuf []int16
}

const (
	pcmRate     = 8000
	pcmChannels = 1
	frameSize   = 160 // 20ms at 8kHz
	// Max speaker queue: ~80ms (4 frames). Keeps latency tight.
	sqMaxSamples = frameSize * 4
)

func newAudioHandler(call xphone.Call) *audioHandler {
	a := &audioHandler{
		call:  call,
		stop:  make(chan struct{}),
		sqBuf: make([]int16, 0, sqMaxSamples),
	}
	// Speaker ON by default
	a.speakerActive.Store(true)

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		if prog != nil {
			prog.Send(msgDebugLog("WRN audio: failed to init context: " + err.Error()))
		}
		return a
	}
	a.ctx = ctx

	a.setupSpeaker()
	a.setupMic()
	a.startPCMReader()

	return a
}

// setupSpeaker initializes the speaker output device.
func (a *audioHandler) setupSpeaker() {
	if a.ctx == nil {
		return
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = pcmChannels
	deviceConfig.SampleRate = pcmRate
	deviceConfig.PeriodSizeInFrames = frameSize

	device, err := malgo.InitDevice(a.ctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, frameCount uint32) {
			if !a.speakerActive.Load() {
				for i := range outputSamples {
					outputSamples[i] = 0
				}
				return
			}
			need := int(frameCount)
			a.sqMu.Lock()
			have := len(a.sqBuf)
			n := need
			if n > have {
				n = have
			}
			// Copy available samples directly as int16 LE
			for i := 0; i < n; i++ {
				idx := i * 2
				if idx+1 < len(outputSamples) {
					binary.LittleEndian.PutUint16(outputSamples[idx:], uint16(a.sqBuf[i]))
				}
			}
			// Silence for any remaining
			for i := n; i < need; i++ {
				idx := i * 2
				if idx+1 < len(outputSamples) {
					outputSamples[idx] = 0
					outputSamples[idx+1] = 0
				}
			}
			// Compact consumed samples to reclaim backing array
			if n > 0 {
				copy(a.sqBuf, a.sqBuf[n:])
				a.sqBuf = a.sqBuf[:len(a.sqBuf)-n]
			}
			a.sqMu.Unlock()
		},
	})
	if err != nil {
		if prog != nil {
			prog.Send(msgDebugLog("WRN audio: speaker init failed: " + err.Error()))
		}
		return
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		if prog != nil {
			prog.Send(msgDebugLog("WRN audio: speaker start failed: " + err.Error()))
		}
		return
	}

	a.speaker = device
}

// setupMic initializes the microphone input device.
func (a *audioHandler) setupMic() {
	if a.ctx == nil {
		return
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = pcmChannels
	deviceConfig.SampleRate = pcmRate
	deviceConfig.PeriodSizeInFrames = frameSize

	var frameBuf []int16

	device, err := malgo.InitDevice(a.ctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, frameCount uint32) {
			if !a.micActive.Load() {
				return
			}
			for i := 0; i < int(frameCount); i++ {
				idx := i * 2
				if idx+1 < len(inputSamples) {
					sample := int16(binary.LittleEndian.Uint16(inputSamples[idx:]))
					frameBuf = append(frameBuf, sample)
				}
				if len(frameBuf) >= frameSize {
					frame := make([]int16, frameSize)
					copy(frame, frameBuf[:frameSize])
					copy(frameBuf, frameBuf[frameSize:])
					frameBuf = frameBuf[:len(frameBuf)-frameSize]
					select {
					case a.call.PCMWriter() <- frame:
					default:
					}
				}
			}
		},
	})
	if err != nil {
		if prog != nil {
			prog.Send(msgDebugLog("WRN audio: mic init failed: " + err.Error()))
		}
		return
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		if prog != nil {
			prog.Send(msgDebugLog("WRN audio: mic start failed: " + err.Error()))
		}
		return
	}

	a.mic = device
}

// startPCMReader reads PCM from the call and feeds the speaker queue
// and echo buffer.
func (a *audioHandler) startPCMReader() {
	pcmIn := a.call.PCMReader()
	pcmOut := a.call.PCMWriter()

	go func() {
		defer func() {
			if r := recover(); r != nil && prog != nil {
				prog.Send(msgDebugLog(fmt.Sprintf("WRN audio: panic: %v", r)))
			}
		}()

		var echoBuf [][]int16

		for {
			select {
			case <-a.stop:
				return
			case frame, ok := <-pcmIn:
				if !ok {
					return
				}

				// Feed speaker queue (direct int16, no float conversion)
				if a.speakerActive.Load() {
					a.sqMu.Lock()
					a.sqBuf = append(a.sqBuf, frame...)
					// Cap queue to prevent unbounded growth
					if len(a.sqBuf) > sqMaxSamples {
						a.sqBuf = a.sqBuf[len(a.sqBuf)-sqMaxSamples:]
					}
					a.sqMu.Unlock()
				}

				// Echo with delay
				if a.echoActive.Load() {
					echoBuf = append(echoBuf, frame)
					if len(echoBuf) > echoDelay {
						delayed := echoBuf[0]
						copy(echoBuf, echoBuf[1:])
						echoBuf = echoBuf[:len(echoBuf)-1]
						select {
						case pcmOut <- delayed:
						default:
						}
					}
				} else if len(echoBuf) > 0 {
					echoBuf = nil
				}
			}
		}
	}()
}

// SetSpeaker toggles speaker output.
func (a *audioHandler) SetSpeaker(on bool) {
	a.speakerActive.Store(on)
	if !on {
		// Flush the queue to avoid stale audio when re-enabled
		a.sqMu.Lock()
		a.sqBuf = a.sqBuf[:0]
		a.sqMu.Unlock()
	}
}

// SetMic toggles microphone input. Disables echo if enabling mic.
func (a *audioHandler) SetMic(on bool) {
	a.micActive.Store(on)
	if on {
		a.echoActive.Store(false)
	}
}

// SetEcho toggles echo mode. Disables mic if enabling echo.
func (a *audioHandler) SetEcho(on bool) {
	a.echoActive.Store(on)
	if on {
		a.micActive.Store(false)
	}
}

// SpeakerActive returns whether the speaker is active.
func (a *audioHandler) SpeakerActive() bool { return a.speakerActive.Load() }

// MicActive returns whether the mic is active.
func (a *audioHandler) MicActive() bool { return a.micActive.Load() }

// EchoActive returns whether echo mode is active.
func (a *audioHandler) EchoActive() bool { return a.echoActive.Load() }

// Stop cleans up audio devices.
func (a *audioHandler) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	select {
	case <-a.stop:
		return
	default:
		close(a.stop)
	}

	if a.speaker != nil {
		a.speaker.Uninit()
		a.speaker = nil
	}
	if a.mic != nil {
		a.mic.Uninit()
		a.mic = nil
	}
	if a.ctx != nil {
		_ = a.ctx.Uninit()
		a.ctx.Free()
		a.ctx = nil
	}
}
