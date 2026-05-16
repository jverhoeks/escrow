package main

import (
	"context"
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
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/cargo"
	"github.com/jverhoeks/escrow/internal/handler/composer"
	"github.com/jverhoeks/escrow/internal/handler/gomod"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/handler/pypi"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/server"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/jverhoeks/escrow/internal/upstream"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfgPath := "sentinel.toml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	generated, msg, err := config.GenerateIfMissing(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to generate config")
	}
	if generated {
		fmt.Println(msg)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
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

	evLog := eventlog.New(500)

	var signals []trust.Signal
	if cfg.Policy != nil {
		if cfg.Policy.Age != nil {
			signals = append(signals, trust.NewAgeSignal(cfg.Policy.Age.MinDays, nil))
		}
		if cfg.Policy.OSV != nil {
			signals = append(signals, trust.NewOSVSignal(cfg.Policy.OSV.MinSeverity, httpClient, c, ""))
		}
		if cfg.Policy.Publisher != nil {
			signals = append(signals, trust.NewPublisherSignal(cfg.Policy.Publisher.MaxAccountAgeDays, httpClient, "", ""))
		}
		if cfg.Policy.Popularity != nil {
			signals = append(signals, trust.NewPopularitySignal(cfg.Policy.Popularity.SpikeFactor, httpClient, c, "", ""))
		}
	}
	trustEngine := trust.NewEngine(signals...)

	var wh *alerts.Webhook
	if cfg.Alerts.WebhookURL != "" {
		wh = alerts.NewWebhook(cfg.Alerts.WebhookURL, nil)
		log.Info().Str("url", cfg.Alerts.WebhookURL).Msg("webhook alerts enabled")
	}

	srv := server.New(cfg.Server.Host, cfg.Server.Port, log.Logger, cfg.Storage.Backend)
	r := srv.Router()

	if cfg.Ecosystems.NPM {
		h := npm.New(httpClient, "https://registry.npmjs.org", trustEngine, polEngine, c, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.PyPI {
		blockSdist := cfg.Policy != nil && cfg.Policy.PyPI != nil && cfg.Policy.PyPI.BlockSdist
		h := pypi.New(httpClient, "https://pypi.org", trustEngine, polEngine, c, blockSdist, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}

	if cfg.Ecosystems.Go {
		h := gomod.New(httpClient, "https://proxy.golang.org", trustEngine, polEngine, c, evLog)
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
		h := composer.New(httpClient, "https://repo.packagist.org", trustEngine, polEngine, c, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
		log.Info().Msg("composer proxy enabled at /composer/")
	}

	if cfg.Dashboard.Enabled {
		dash := dashboard.New(cfg.Dashboard, evLog, log.Logger, allowList)
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
