package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"github.com/je4/filesystem/v3/pkg/vfsrw"
	"github.com/je4/mediaservermain/v2/config"
	"github.com/je4/mediaservermain/v2/pkg/web"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	"github.com/je4/miniresolver/v2/pkg/resolver"
	loaderConfig "github.com/je4/trustutil/v2/pkg/config"
	"github.com/je4/trustutil/v2/pkg/loader"
	configutil "github.com/je4/utils/v2/pkg/config"
	"github.com/je4/utils/v2/pkg/zLogger"
	ublogger "gitlab.switch.ch/ub-unibas/go-ublogger"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var configfile = flag.String("config", "", "location of toml configuration file")

func main() {
	flag.Parse()

	var cfgFS fs.FS
	var cfgFile string
	if *configfile != "" {
		cfgFS = os.DirFS(filepath.Dir(*configfile))
		cfgFile = filepath.Base(*configfile)
	} else {
		cfgFS = config.ConfigFS
		cfgFile = "mediaservermain.toml"
	}

	conf := &MediaserverMainConfig{
		LocalAddr: "localhost:8443",
		//ResolverTimeout: config.Duration(10 * time.Minute),
		ExternalAddr:            "https://localhost:8443",
		LogLevel:                "DEBUG",
		ResolverTimeout:         configutil.Duration(10 * time.Minute),
		ResolverNotFoundTimeout: configutil.Duration(10 * time.Second),
		ServerTLS: &loaderConfig.TLSConfig{
			Type: "DEV",
		},
		ClientTLS: &loaderConfig.TLSConfig{
			Type: "DEV",
		},
	}
	if err := LoadMediaserverMainConfig(cfgFS, cfgFile, conf); err != nil {
		log.Fatalf("cannot load toml from [%v] %s: %v", cfgFS, cfgFile, err)
	}

	// create logger instance
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("cannot get hostname: %v", err)
	}

	var loggerTLSConfig *tls.Config
	var loggerLoader io.Closer
	if conf.Log.Stash.TLS != nil {
		loggerTLSConfig, loggerLoader, err = loader.CreateClientLoader(conf.Log.Stash.TLS, nil)
		if err != nil {
			log.Fatalf("cannot create client loader: %v", err)
		}
		defer loggerLoader.Close()
	}

	_logger, _logstash, _logfile := ublogger.CreateUbMultiLoggerTLS(conf.Log.Level, conf.Log.File,
		ublogger.SetDataset(conf.Log.Stash.Dataset),
		ublogger.SetLogStash(conf.Log.Stash.LogstashHost, conf.Log.Stash.LogstashPort, conf.Log.Stash.Namespace, conf.Log.Stash.LogstashTraceLevel),
		ublogger.SetTLS(conf.Log.Stash.TLS != nil),
		ublogger.SetTLSConfig(loggerTLSConfig),
	)
	if _logstash != nil {
		defer _logstash.Close()
	}
	if _logfile != nil {
		defer _logfile.Close()
	}

	l2 := _logger.With().Str("host", hostname).Str("addr", conf.LocalAddr).Logger() //.Output(output)
	var logger zLogger.ZLogger = &l2

	l3 := _logger.With().Str("package", "vfsrw").Logger()
	vfs, err := vfsrw.NewFS(conf.VFS, &l3)
	if err != nil {
		logger.Panic().Err(err).Msg("cannot create vfs")
	}
	defer func() {
		if err := vfs.Close(); err != nil {
			logger.Error().Err(err).Msg("cannot close vfs")
		}
	}()

	webTLSConfig, webLoader, err := loader.CreateServerLoader(false, &conf.WebTLS, nil, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create server loader")
	}
	defer webLoader.Close()

	/*
		serverCert, serverLoader, err := loader.CreateServerLoader(false, conf.ServerTLS, nil, logger)
		if err != nil {
			logger.Panic().Msgf("cannot create server loader: %v", err)
		}
		defer serverLoader.Close()
	*/

	clientCert, clientLoader, err := loader.CreateClientLoader(conf.ClientTLS, logger)
	if err != nil {
		logger.Panic().Msgf("cannot create client loader: %v", err)
	}
	defer clientLoader.Close()

	logger.Info().Msgf("resolver address is %s", conf.ResolverAddr)
	resolverClient, err := resolver.NewMiniresolverClient(conf.ResolverAddr, conf.GRPCClient, clientCert, nil, time.Duration(conf.ResolverTimeout), time.Duration(conf.ResolverNotFoundTimeout), logger)
	if err != nil {
		logger.Fatal().Msgf("cannot create resolver client: %v", err)
	}
	defer resolverClient.Close()

	dbClient, err := resolver.NewClient[mediaserverproto.DatabaseClient](resolverClient, mediaserverproto.NewDatabaseClient, mediaserverproto.Database_ServiceDesc.ServiceName)
	if err != nil {
		logger.Panic().Msgf("cannot create mediaserverdb grpc client: %v", err)
	}
	resolver.DoPing(dbClient, logger)

	actionControllerClient, err := resolver.NewClient[mediaserverproto.ActionClient](resolverClient, mediaserverproto.NewActionClient, mediaserverproto.Action_ServiceDesc.ServiceName)
	if err != nil {
		logger.Panic().Msgf("cannot create mediaserveractioncontroller grpc client: %v", err)
	}
	resolver.DoPing(actionControllerClient, logger)

	ctrl, err := web.NewMainController(conf.LocalAddr, conf.ExternalAddr, webTLSConfig, conf.JWTAlg, conf.IIIF, conf.IIIFPrefix, dbClient, actionControllerClient, vfs, 200, 20, 10*time.Minute, logger)
	if err != nil {
		logger.Fatal().Msgf("cannot create controller: %v", err)
	}
	var wg = &sync.WaitGroup{}
	ctrl.Start(wg)

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	fmt.Println("press ctrl+c to stop server")
	s := <-done
	fmt.Println("got signal:", s)

	ctrl.GracefulStop()
	wg.Wait()
}
