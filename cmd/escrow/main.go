package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/jverhoeks/escrow/internal/alerts"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/cireport"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/cargo"
	"github.com/jverhoeks/escrow/internal/handler/composer"
	"github.com/jverhoeks/escrow/internal/handler/gomod"
	"github.com/jverhoeks/escrow/internal/handler/maven"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/handler/nuget"
	"github.com/jverhoeks/escrow/internal/handler/pypi"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/server"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/jverhoeks/escrow/internal/upstream"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"
var version = "dev"

func main() {
	// Handle subcommands before flag parsing so they get their own flags.
	if len(os.Args) > 1 && os.Args[1] == "ci-report" {
		runCIReport(os.Args[2:])
		return
	}

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfgPath    := flag.String("config", "escrow.toml", "config file path")
	hostFlag   := flag.String("host", "", "listen host (overrides config; use 0.0.0.0 for all interfaces, default 127.0.0.1)")
	clearCache := flag.Bool("clear-cache", false, "flush all cached metadata and blobs on startup before serving")
	// Signal overrides — each flag disables the corresponding policy check regardless of config.
	noAge       := flag.Bool("no-age",       false, "disable the age gate (ignore policy.age in config)")
	noOSV       := flag.Bool("no-osv",       false, "disable OSV vulnerability scan (ignore policy.osv in config)")
	noPublisher := flag.Bool("no-publisher", false, "disable publisher account age check (ignore policy.publisher in config)")
	noPopularity:= flag.Bool("no-popularity",false, "disable popularity spike detection (ignore policy.popularity in config)")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("escrow %s\n", version)
		return
	}

	if flag.NArg() > 0 { // backward-compat: escrow [config-path]
		*cfgPath = flag.Arg(0)
	}

	generated, msg, err := config.GenerateIfMissing(*cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to generate config")
	}
	if generated {
		fmt.Println(msg)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	if *hostFlag != "" {
		cfg.Server.Host = *hostFlag
	}
	log.Info().Str("version", version).Msg("escrow starting")
	for _, err := range cfg.Validate() {
		log.Fatal().Err(err).Msg("invalid configuration")
	}
	for _, w := range cfg.Warnings() {
		log.Warn().Msg(w)
	}

	var c cache.Cache
	switch cfg.Storage.Backend {
	case "memory":
		c = cache.NewMemory()
	case "s3":
		c, err = cache.NewS3(cfg.Storage.S3.Bucket, cfg.Storage.S3.Region, cfg.Storage.S3.Endpoint)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init S3 cache")
		}
	default:
		diskPath := config.ExpandPath(cfg.Storage.Disk.Path)
		var maxBytes int64
		if cfg.Storage.Disk.MaxSizeGB > 0 {
			maxBytes = int64(cfg.Storage.Disk.MaxSizeGB) << 30
		}
		purgeM := cfg.Storage.Disk.PurgeIntervalM
		if purgeM == 0 {
			purgeM = 60
		}
		purgeInterval := time.Duration(purgeM) * time.Minute
		c, err = cache.NewDiskWithMax(diskPath, maxBytes, purgeInterval)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init disk cache")
		}
		logEvt := log.Info().Str("path", diskPath).Int("purge_interval_m", purgeM)
		if maxBytes > 0 {
			logEvt = logEvt.Int("max_size_gb", cfg.Storage.Disk.MaxSizeGB)
		}
		logEvt.Msg("disk cache initialised")
	}
	defer c.Close()

	if *clearCache {
		if err := c.Flush(); err != nil {
			log.Fatal().Err(err).Msg("failed to flush cache")
		}
		log.Info().Msg("cache flushed")
	}

	httpClient := upstream.New()
	polEngine := policy.New(cfg.Policy)

	allowList, err := allow.New(config.ExpandPath(cfg.AllowlistPath))
	if err != nil {
		log.Fatal().Err(err).Str("path", cfg.AllowlistPath).Msg("failed to load allowlist")
	}
	if cfg.AllowlistPath == "" {
		log.Warn().Msg("allowlist_path not configured — allow list entries will not persist across restarts")
	}
	polEngine.WithAllowList(allowList)

	blockList, err := block.New(config.ExpandPath(cfg.BlocklistPath))
	if err != nil {
		log.Fatal().Err(err).Str("path", cfg.BlocklistPath).Msg("failed to load blocklist")
	}
	if cfg.BlocklistPath == "" {
		log.Warn().Msg("blocklist_path not configured — block list entries will not persist across restarts")
	}
	polEngine.WithBlockList(blockList)

	var evLog *eventlog.Log
	if cfg.EventLogPath != "" {
		evLog, err = eventlog.NewWithPath(5000, config.ExpandPath(cfg.EventLogPath))
		if err != nil {
			log.Fatal().Err(err).Str("path", cfg.EventLogPath).Msg("failed to open event log file")
		}
		defer evLog.Close()
		log.Info().Str("path", cfg.EventLogPath).Msg("event log persistence enabled")
	} else {
		evLog = eventlog.New(5000)
	}

	var signals []trust.Signal
	if cfg.Policy != nil {
		if cfg.Policy.Age != nil && !*noAge {
			signals = append(signals, trust.NewAgeSignal(cfg.Policy.Age.MinDays, nil))
			log.Info().Int("min_days", cfg.Policy.Age.MinDays).Str("action", cfg.Policy.Age.Action).
				Msg("age gate enabled")
		} else if *noAge {
			log.Warn().Msg("age gate disabled via --no-age flag")
		}
		if cfg.Policy.OSV != nil && !*noOSV {
			signals = append(signals, trust.NewOSVSignal(cfg.Policy.OSV.MinSeverity, httpClient, c, ""))
			log.Info().Str("min_severity", cfg.Policy.OSV.MinSeverity).Str("action", cfg.Policy.OSV.Action).
				Msg("OSV vulnerability scan enabled")
		} else if *noOSV {
			log.Warn().Msg("OSV vulnerability scan disabled via --no-osv flag")
		}
		if cfg.Policy.Publisher != nil && !*noPublisher {
			signals = append(signals, trust.NewPublisherSignal(cfg.Policy.Publisher.MaxAccountAgeDays, httpClient, c, "", ""))
			log.Info().Int("max_account_age_days", cfg.Policy.Publisher.MaxAccountAgeDays).Str("action", cfg.Policy.Publisher.Action).
				Msg("publisher account age check enabled")
		} else if *noPublisher {
			log.Warn().Msg("publisher check disabled via --no-publisher flag")
		}
		if cfg.Policy.Popularity != nil && !*noPopularity {
			signals = append(signals, trust.NewPopularitySignal(cfg.Policy.Popularity.SpikeFactor, httpClient, c, "", ""))
			log.Info().Float64("spike_factor", cfg.Policy.Popularity.SpikeFactor).Str("action", cfg.Policy.Popularity.Action).
				Msg("popularity spike detection enabled")
		} else if *noPopularity {
			log.Warn().Msg("popularity check disabled via --no-popularity flag")
		}
	} else {
		log.Warn().Msg("no policy configured — proxying transparently (no age gate, no vulnerability scan)")
	}
	trustEngine := trust.NewEngine(signals...)

	// Age-only engine for index listing — avoids per-version OSV/publisher network
	// calls when building the Simple API for large packages (starlette, pydantic-core, etc.).
	// OSV and publisher checks still run at download time via the full trustEngine.
	var listingSignals []trust.Signal
	if cfg.Policy != nil && cfg.Policy.Age != nil && !*noAge {
		listingSignals = append(listingSignals, trust.NewAgeSignal(cfg.Policy.Age.MinDays, nil))
	}
	listingEngine := trust.NewEngine(listingSignals...)

	var wh *alerts.Webhook
	if cfg.Alerts.WebhookURL != "" {
		wh = alerts.NewWebhook(cfg.Alerts.WebhookURL, nil)
		log.Info().Str("url", cfg.Alerts.WebhookURL).Msg("webhook alerts enabled")
	}

	// Collect active upstream URLs for /healthz probes.
	// Must be fully populated BEFORE server.New so the health handler has the complete map.
	upstreamURLs := make(map[string]string)
	if cfg.Ecosystems.NPM {
		upstreamURLs["npm"] = cfg.Ecosystems.EffectiveNPMUpstream()
	}
	if cfg.Ecosystems.PyPI {
		upstreamURLs["pypi"] = cfg.Ecosystems.EffectivePyPIUpstream()
	}
	if cfg.Ecosystems.Go {
		upstreamURLs["go"] = cfg.Ecosystems.EffectiveGoUpstream()
	}
	if cfg.Ecosystems.Cargo {
		upstreamURLs["cargo"] = "https://crates.io"
	}
	if cfg.Ecosystems.Composer {
		upstreamURLs["composer"] = cfg.Ecosystems.EffectiveComposerUpstream()
	}
	if cfg.Ecosystems.NuGet {
		upstreamURLs["nuget"] = cfg.Ecosystems.EffectiveNuGetUpstream()
	}
	if cfg.Ecosystems.Maven {
		upstreamURLs["maven"] = cfg.Ecosystems.EffectiveMavenUpstream()
	}

	cacheDir := ""
	if cfg.Storage.Backend == "disk" {
		cacheDir = config.ExpandPath(cfg.Storage.Disk.Path)
	}
	srv := server.New(server.Options{
		Version:                  version,
		Host:                     cfg.Server.Host,
		Port:                     cfg.Server.Port,
		StorageBackend:           cfg.Storage.Backend,
		CacheDir:                 cacheDir,
		WriteTimeoutSeconds:      cfg.Server.WriteTimeoutSeconds,
		ReadHeaderTimeoutSeconds: cfg.Server.ReadHeaderTimeoutSeconds,
		IdleTimeoutSeconds:       cfg.Server.IdleTimeoutSeconds,
		TLSCertFile:              cfg.Server.TLSCertFile,
		TLSKeyFile:               cfg.Server.TLSKeyFile,
		ProxyRateLimitPerMin:     cfg.Server.ProxyRateLimitPerMin,
		UpstreamURLs:             upstreamURLs,
	}, log.Logger)
	r := srv.Router()

	if cfg.Ecosystems.NPM {
		h := npm.New(httpClient, cfg.Ecosystems.EffectiveNPMUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.PyPI {
		blockSdist := cfg.Policy != nil && cfg.Policy.PyPI != nil && cfg.Policy.PyPI.BlockSdist
		h := pypi.New(httpClient, cfg.Ecosystems.EffectivePyPIUpstream(), trustEngine, polEngine, c, blockSdist, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.Go {
		h := gomod.New(httpClient, cfg.Ecosystems.EffectiveGoUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("go modules proxy enabled at /go/")
	}
	if cfg.Ecosystems.Cargo {
		h := cargo.New(httpClient, trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("cargo sparse registry enabled at /cargo/")
	}
	if cfg.Ecosystems.Composer {
		h := composer.New(httpClient, cfg.Ecosystems.EffectiveComposerUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("composer proxy enabled at /composer/")
	}
	if cfg.Ecosystems.NuGet {
		h := nuget.New(httpClient, cfg.Ecosystems.EffectiveNuGetUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if cfg.Ecosystems.NuGetFlatcontainerURL != "" {
			h.SetFlatcontainerURL(cfg.Ecosystems.NuGetFlatcontainerURL)
		}
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("nuget proxy enabled at /nuget/")
	}
	if cfg.Ecosystems.Maven {
		h := maven.New(httpClient, cfg.Ecosystems.EffectiveMavenUpstream(), trustEngine, polEngine, c, evLog).
			WithListingEngine(listingEngine)
		if wh != nil {
			h.WithWebhook(wh)
		}
		if cfg.Ecosystems.MavenSnapshotUpstream != "" {
			h.SetSnapshotURL(cfg.Ecosystems.EffectiveMavenSnapshotUpstream())
			log.Info().Str("url", cfg.Ecosystems.MavenSnapshotUpstream).Msg("maven snapshot upstream configured")
		}
		h.Mount(r)
		log.Info().Msg("maven/gradle proxy enabled at /maven2/")
	}

	cireport.New(evLog).Mount(r)

	if cfg.Dashboard.Enabled {
		dash := dashboard.New(cfg.Dashboard, evLog, log.Logger, allowList, blockList, c)
		dash.Mount(r)
		log.Info().Str("path", cfg.Dashboard.Path).Msg("dashboard enabled")
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if err := srv.Start(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("server stopped unexpectedly")
	}
}

// runCIReport fetches the CI report from a running escrow proxy and prints it to stdout.
// Intended for use in GitHub Actions: `escrow ci-report >> $GITHUB_STEP_SUMMARY`
func runCIReport(args []string) {
	fs := flag.NewFlagSet("ci-report", flag.ExitOnError)
	port := fs.Int("port", 7888, "escrow proxy port")
	n := fs.Int("n", 200, "max packages to show in the table")
	fs.Parse(args) //nolint:errcheck

	url := fmt.Sprintf("http://127.0.0.1:%d/ci-report?n=%d", *port, *n)
	resp, err := http.Get(url) //nolint:gosec — localhost only
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow ci-report: could not reach proxy on port %d: %v\n", *port, err)
		return
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body) //nolint:errcheck
}
