// Command ranger surveys nearby wireless devices and serves a live dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/kd14/ranger/internal/core"
	"github.com/kd14/ranger/internal/csi"
	"github.com/kd14/ranger/internal/insight"
	"github.com/kd14/ranger/internal/place"
	"github.com/kd14/ranger/internal/scan"
	"github.com/kd14/ranger/internal/server"
	"github.com/kd14/ranger/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ranger:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listen      = flag.String("listen", "127.0.0.1:8787", "address to serve the dashboard on")
		allowRemote = flag.Bool("allow-remote", false, "permit binding a non-loopback address (exposes your surroundings to the network)")
		token       = flag.String("token", "", "access token; generated when empty")
		dev         = flag.Bool("dev", false, "serve dashboard assets from ./web instead of the embedded copy")
		fake        = flag.Bool("fake", false, "emit synthetic devices instead of scanning real radios")
		ttl         = flag.Duration("ttl", 90*time.Second, "how long a device survives without being seen again")
		openBrowser = flag.Bool("open", false, "open the dashboard in your browser on startup")
		verbose     = flag.Bool("v", false, "enable debug logging")

		monitor      = flag.Bool("monitor", false, "capture raw 802.11 frames to detect Wi-Fi clients (needs root and a -tags pcap build)")
		monitorIface = flag.String("monitor-interface", "", "capture interface for monitor mode")
		anonymize    = flag.Bool("anonymize-macs", true, "replace the device half of captured client MAC addresses with a salted hash")

		places          = flag.Bool("places", true, "learn and recognise locations from the surrounding radio environment")
		placesPath      = flag.String("places-file", "", "where learned places are stored; defaults to your user config directory")
		placeThreshold  = flag.Float64("place-threshold", 0.62, "similarity above which a signature counts as the same place")
		placeMinAnchors = flag.Int("place-min-anchors", 3, "fewest stable anchors needed before a place can be judged")

		csiEnable = flag.Bool("csi", false, "ingest Channel State Information from an ESP32 for device-free presence, motion, and breathing sensing")
		csiListen = flag.String("csi-listen", "0.0.0.0:5566", "UDP address to receive CSI packets on")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	assets, err := assetFS(*dev)
	if err != nil {
		return err
	}

	registry := core.NewRegistry(core.Config{TTL: *ttl}, core.NewBus())

	scanners := buildScanners(*fake, scan.Options{
		Monitor:          *monitor,
		MonitorInterface: *monitorIface,
		AnonymizeMACs:    *anonymize,
	})
	if len(scanners) == 0 {
		return fmt.Errorf("no scanners enabled")
	}
	manager := scan.NewManager(registry, log, scanners...)

	placeService, err := buildPlaceService(*places, *placesPath, *placeThreshold, *placeMinAnchors, registry, log)
	if err != nil {
		return err
	}

	srv, err := server.New(server.Config{
		Addr:        *listen,
		Token:       *token,
		AllowRemote: *allowRemote,
		Assets:      assets,
		Log:         log,
	}, registry, manager, placeService)
	if err != nil {
		return err
	}

	var csiService *csi.Service
	if *csiEnable {
		csiService = csi.NewService(csi.ServiceConfig{Listen: *csiListen}, registry.Bus(), log)
		srv.SetCSI(csiService)
	}

	go registry.Run(ctx)
	go manager.Run(ctx)
	if csiService != nil {
		go csiService.Run(ctx)
	}

	// Closed once learned places have been written, so shutdown cannot race
	// the final save.
	placesSaved := make(chan struct{})
	if placeService != nil {
		go func() {
			defer close(placesSaved)
			placeService.Run(ctx)
		}()
	} else {
		close(placesSaved)
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe(ctx) }()

	// Wait for the listener so the printed URL carries the real port, which
	// matters when -listen asks for an ephemeral one.
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return <-errc
	case <-srv.Ready():
	}

	url := srv.URL()
	fmt.Fprintf(os.Stderr, "\n  Ranger is live:  %s\n\n", url)
	if *allowRemote {
		fmt.Fprintf(os.Stderr, "  WARNING: listening on a non-loopback address. Anyone who reaches\n"+
			"  this port and holds the token can see every device around you.\n\n")
	}
	if *monitor {
		fmt.Fprintf(os.Stderr, "  WARNING: monitor mode captures 802.11 frames from devices that have not\n"+
			"  consented to being observed. Only run it on networks you are authorised to\n"+
			"  survey, and check your local law before you do.\n\n")
		if !*anonymize {
			fmt.Fprintf(os.Stderr, "  Client MAC anonymisation is OFF; real hardware addresses will be shown.\n\n")
		}
	}
	if *csiEnable {
		fmt.Fprintf(os.Stderr, "  CSI ingest is listening on %s (UDP). This accepts packets from the\n"+
			"  local network; only expose it on a trusted LAN. Flash firmware/esp32-csi\n"+
			"  to an ESP32 and point it at this host.\n\n", *csiListen)
	}
	if *openBrowser {
		launchBrowser(ctx, url, log)
	}

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		serveErr := <-errc
		select {
		case <-placesSaved:
		case <-time.After(3 * time.Second):
			log.Warn("timed out saving learned places")
		}
		return serveErr
	}
}

// buildPlaceService prepares location learning, loading anything remembered
// from previous runs.
func buildPlaceService(enabled bool, path string, threshold float64, minAnchors int, registry *core.Registry, log *slog.Logger) (*insight.PlaceService, error) {
	if !enabled {
		return nil, nil
	}
	if path == "" {
		path = place.DefaultPath()
	}

	engine := place.NewEngine(place.Config{
		Threshold:  threshold,
		MinAnchors: minAnchors,
		Path:       path,
	})
	// A corrupt or unreadable store should not stop a scan, so it only warns.
	if err := engine.Load(); err != nil {
		log.Warn("could not load learned places", "err", err)
	}
	return insight.NewPlaceService(registry, engine, log), nil
}

// buildScanners assembles the enabled backends. Backends that cannot run on
// this machine still get registered so the dashboard can explain why.
func buildScanners(fake bool, opts scan.Options) []scan.Scanner {
	if fake {
		return []scan.Scanner{scan.NewFake()}
	}
	return scan.Platform(opts)
}

func assetFS(dev bool) (fs.FS, error) {
	if !dev {
		return web.Assets, nil
	}
	const dir = "web"
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("-dev requires a ./%s directory: %w", dir, err)
	}
	return os.DirFS(dir), nil
}

// launchBrowser opens the dashboard. The URL is server-generated, never
// user-supplied, and is passed as a single argv entry rather than through a
// shell.
func launchBrowser(ctx context.Context, url string, log *slog.Logger) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url)
	case "linux":
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	default:
		log.Warn("cannot open browser automatically", "os", runtime.GOOS)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Warn("open browser", "err", err)
	}
}
