package csi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/kd14/ranger/internal/core"
)

// State is the CSI picture handed to the dashboard.
type State struct {
	Active  bool     `json:"active"`
	Listen  string   `json:"listen"`
	Reason  string   `json:"reason,omitempty"`
	Sensors []Sensor `json:"sensors"`
}

// Sensor is one ESP32's live analysis.
type Sensor struct {
	ID       string    `json:"id"`
	RSSI     int       `json:"rssi"`
	Channel  int       `json:"channel"`
	FPS      float64   `json:"fps"`
	Loss     float64   `json:"loss"`
	LastSeen time.Time `json:"lastSeen"`
	Reading  Reading   `json:"reading"`
}

// ServiceConfig configures the ingest server.
type ServiceConfig struct {
	// Listen is the UDP address the ESP32 sends CSI to.
	Listen string
	// Analyzer tunes the DSP.
	Analyzer Config
	// MaxSensors bounds how many distinct sensor IDs are tracked, so a spoofed
	// stream of IDs cannot grow memory without limit.
	MaxSensors int
	// SensorTTL drops a sensor that has gone silent.
	SensorTTL time.Duration
	// PublishInterval throttles how often the aggregated state is broadcast.
	PublishInterval time.Duration
}

func (c ServiceConfig) withDefaults() ServiceConfig {
	if c.Listen == "" {
		c.Listen = "0.0.0.0:5566"
	}
	if c.MaxSensors <= 0 {
		c.MaxSensors = 16
	}
	if c.SensorTTL <= 0 {
		c.SensorTTL = 15 * time.Second
	}
	if c.PublishInterval <= 0 {
		c.PublishInterval = time.Second
	}
	return c
}

// Service listens for CSI datagrams and publishes derived presence, motion,
// and breathing state on the event bus.
//
// The socket accepts unauthenticated UDP from the local network. Parse rejects
// malformed packets, MaxSensors bounds memory, and the analyzer's ring buffers
// are size-capped, so a hostile sender can waste CPU but not exhaust the host.
type Service struct {
	cfg ServiceConfig
	bus *core.Bus
	log *slog.Logger

	mu        sync.Mutex
	sensors   map[string]*sensorState
	state     State
	listenErr error
	bound     string
	readyCh   chan struct{}
}

type sensorState struct {
	analyzer *analyzer
	rssi     int
	channel  int
	lastSeen time.Time
	lastSeq  uint16
	haveSeq  bool
	frames   int
	lost     int
	windowAt time.Time
	windowN  int
	fps      float64
	loss     float64
}

// NewService builds a CSI ingest service.
func NewService(cfg ServiceConfig, bus *core.Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	cfg = cfg.withDefaults()
	return &Service{
		cfg:     cfg,
		bus:     bus,
		log:     log,
		sensors: make(map[string]*sensorState),
		readyCh: make(chan struct{}),
		state:   State{Listen: cfg.Listen, Reason: "waiting for the first CSI packet", Sensors: []Sensor{}},
	}
}

// BoundAddr returns the address actually listened on, empty until Run binds it.
func (s *Service) BoundAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bound
}

// Ready is closed once the socket is bound.
func (s *Service) Ready() <-chan struct{} { return s.readyCh }

// State returns the latest published picture.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Run listens until the context is cancelled.
func (s *Service) Run(ctx context.Context) {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		s.fail(fmt.Sprintf("invalid CSI listen address %q: %v", s.cfg.Listen, err))
		return
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		s.fail(fmt.Sprintf("cannot listen on %s: %v", s.cfg.Listen, err))
		return
	}
	defer conn.Close()

	s.mu.Lock()
	s.bound = conn.LocalAddr().String()
	s.mu.Unlock()
	close(s.readyCh)

	s.log.Info("csi ingest listening", "addr", conn.LocalAddr().String())
	s.setActive()

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	go s.publishLoop(ctx)
	s.readLoop(ctx, conn)
}

func (s *Service) readLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, MaxPacket)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A single bad read should not kill ingest.
			s.log.Debug("csi read error", "err", err)
			continue
		}
		frame, err := Parse(buf[:n])
		if err != nil {
			s.log.Debug("csi parse rejected", "err", err, "bytes", n)
			continue
		}
		s.ingest(frame, time.Now())
	}
}

// ingest folds one frame into its sensor's analyzer, creating the sensor if it
// is new and there is room.
func (s *Service) ingest(f Frame, now time.Time) {
	s.mu.Lock()
	st, ok := s.sensors[f.SensorID]
	if !ok {
		if len(s.sensors) >= s.cfg.MaxSensors {
			s.mu.Unlock()
			return
		}
		st = &sensorState{analyzer: newAnalyzer(s.cfg.Analyzer), windowAt: now}
		s.sensors[f.SensorID] = st
	}

	if st.haveSeq {
		// uint16 wrap is fine: the gap of a single lost frame stays small.
		gap := int(f.Seq - st.lastSeq)
		if gap > 1 && gap < 1000 {
			st.lost += gap - 1
		}
	}
	st.lastSeq = f.Seq
	st.haveSeq = true
	st.rssi = f.RSSI
	st.channel = f.Channel
	st.lastSeen = now
	st.frames++
	st.windowN++
	analyzer := st.analyzer
	s.mu.Unlock()

	// The analyzer has its own lock; keep it out of the service lock so a slow
	// DFT never blocks ingest.
	analyzer.add(f, now)
}

func (s *Service) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PublishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.publish(now)
		}
	}
}

// publish rebuilds the aggregate state and broadcasts it.
func (s *Service) publish(now time.Time) {
	s.mu.Lock()

	for id, st := range s.sensors {
		if now.Sub(st.lastSeen) > s.cfg.SensorTTL {
			delete(s.sensors, id)
		}
	}

	sensors := make([]Sensor, 0, len(s.sensors))
	for id, st := range s.sensors {
		elapsed := now.Sub(st.windowAt).Seconds()
		if elapsed >= 1 {
			st.fps = float64(st.windowN) / elapsed
			total := st.windowN + st.lost
			if total > 0 {
				st.loss = float64(st.lost) / float64(total)
			}
			st.windowAt = now
			st.windowN = 0
			st.lost = 0
		}
		sensors = append(sensors, Sensor{
			ID:       id,
			RSSI:     st.rssi,
			Channel:  st.channel,
			FPS:      round1(st.fps),
			Loss:     round2(st.loss),
			LastSeen: st.lastSeen,
			Reading:  st.analyzer.reading(),
		})
	}
	sort.Slice(sensors, func(i, j int) bool { return sensors[i].ID < sensors[j].ID })

	reason := ""
	if len(sensors) == 0 {
		reason = "no CSI sensors reporting; flash the firmware in firmware/esp32-csi"
	}
	s.state = State{Active: true, Listen: s.cfg.Listen, Reason: reason, Sensors: sensors}
	snapshot := s.state
	s.mu.Unlock()

	s.broadcast(snapshot)
}

func (s *Service) setActive() {
	s.mu.Lock()
	s.state = State{Active: true, Listen: s.cfg.Listen, Reason: "no CSI sensors reporting yet", Sensors: []Sensor{}}
	snapshot := s.state
	s.mu.Unlock()
	s.broadcast(snapshot)
}

func (s *Service) fail(reason string) {
	s.log.Warn("csi ingest unavailable", "reason", reason)
	s.mu.Lock()
	s.listenErr = fmt.Errorf("%s", reason)
	s.state = State{Active: false, Listen: s.cfg.Listen, Reason: reason, Sensors: []Sensor{}}
	snapshot := s.state
	s.mu.Unlock()
	s.broadcast(snapshot)
}

func (s *Service) broadcast(state State) {
	payload, err := json.Marshal(state)
	if err != nil {
		s.log.Error("encode csi state", "err", err)
		return
	}
	s.bus.Publish(core.Event{Type: core.EventCSI, Data: payload})
}
