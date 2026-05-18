package main

import (
	"context"
	"flag"
	"fmt"
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

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfgPath := flag.String("config", "escrow.toml", "config file path")
	hostFlag := flag.String("host", "", "listen host (overrides config; use 0.0.0.0 for all interfaces, default 127.0.0.1)")
	flag.Parse()
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
		c, err = cache.NewDisk(cfg.Storage.Disk.Path)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init disk cache")
		}
	}
	defer c.Close()

	httpClient := upstream.New()
	polEngine := policy.New(cfg.Policy)

	allowList, err := allow.New(cfg.AllowlistPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", cfg.AllowlistPath).Msg("failed to load allowlist")
	}
	if cfg.AllowlistPath == "" {
		log.Warn().Msg("allowlist_path not configured — allow list entries will not persist across restarts")
	}
	polEngine.WithAllowList(allowList)

	blockList, err := block.New(cfg.BlocklistPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", cfg.BlocklistPath).Msg("failed to load blocklist")
	}
	if cfg.BlocklistPath == "" {
		log.Warn().Msg("blocklist_path not configured — block list entries will not persist across restarts")
	}
	polEngine.WithBlockList(blockList)

	var evLog *eventlog.Log
	if cfg.EventLogPath != "" {
		evLog, err = eventlog.NewWithPath(5000, cfg.EventLogPath)
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
		if cfg.Policy.Age != nil {
			signals = append(signals, trust.NewAgeSignal(cfg.Policy.Age.MinDays, nil))
			log.Info().Int("min_days", cfg.Policy.Age.MinDays).Str("action", cfg.Policy.Age.Action).
				Msg("age gate enabled")
		}
		if cfg.Policy.OSV != nil {
			signals = append(signals, trust.NewOSVSignal(cfg.Policy.OSV.MinSeverity, httpClient, c, ""))
			log.Info().Str("min_severity", cfg.Policy.OSV.MinSeverity).Str("action", cfg.Policy.OSV.Action).
				Msg("OSV vulnerability scan enabled")
		}
		if cfg.Policy.Publisher != nil {
			signals = append(signals, trust.NewPublisherSignal(cfg.Policy.Publisher.MaxAccountAgeDays, httpClient, c, "", ""))
			log.Info().Int("max_account_age_days", cfg.Policy.Publisher.MaxAccountAgeDays).Str("action", cfg.Policy.Publisher.Action).
				Msg("publisher account age check enabled")
		}
		if cfg.Policy.Popularity != nil {
			signals = append(signals, trust.NewPopularitySignal(cfg.Policy.Popularity.SpikeFactor, httpClient, c, "", ""))
			log.Info().Float64("spike_factor", cfg.Policy.Popularity.SpikeFactor).Str("action", cfg.Policy.Popularity.Action).
				Msg("popularity spike detection enabled")
		}
	} else {
		log.Warn().Msg("no policy configured — proxying transparently (no age gate, no vulnerability scan)")
	}
	trustEngine := trust.NewEngine(signals...)

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
		cacheDir = cfg.Storage.Disk.Path
	}
	srv := server.New(server.Options{
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
		h := npm.New(httpClient, cfg.Ecosystems.EffectiveNPMUpstream(), trustEngine, polEngine, c, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.PyPI {
		blockSdist := cfg.Policy != nil && cfg.Policy.PyPI != nil && cfg.Policy.PyPI.BlockSdist
		h := pypi.New(httpClient, cfg.Ecosystems.EffectivePyPIUpstream(), trustEngine, polEngine, c, blockSdist, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.Go {
		h := gomod.New(httpClient, cfg.Ecosystems.EffectiveGoUpstream(), trustEngine, polEngine, c, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("go modules proxy enabled at /go/")
	}
	if cfg.Ecosystems.Cargo {
		h := cargo.New(httpClient, trustEngine, polEngine, c, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("cargo sparse registry enabled at /cargo/")
	}
	if cfg.Ecosystems.Composer {
		h := composer.New(httpClient, cfg.Ecosystems.EffectiveComposerUpstream(), trustEngine, polEngine, c, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("composer proxy enabled at /composer/")
	}
	if cfg.Ecosystems.NuGet {
		h := nuget.New(httpClient, cfg.Ecosystems.EffectiveNuGetUpstream(), trustEngine, polEngine, c, evLog)
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
		h := maven.New(httpClient, cfg.Ecosystems.EffectiveMavenUpstream(), trustEngine, polEngine, c, evLog)
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
