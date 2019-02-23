package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/fukata/golang-stats-api-handler"
	"github.com/jessevdk/go-flags"
	"github.com/kazeburo/chocon/accesslog"
	"github.com/kazeburo/chocon/pidfile"
	"github.com/kazeburo/chocon/proxy"
	"github.com/kazeburo/chocon/upstream"
	"github.com/lestrrat/go-server-starter-listener"
	statsHTTP "go.mercari.io/go-httpstats"
	"go.uber.org/zap"
)

var (
	// Version chocon version
	Version string
)

type cmdOpts struct {
	Listen           string `short:"l" long:"listen" default:"0.0.0.0" description:"address to bind"`
	Port             string `short:"p" long:"port" default:"3000" description:"Port number to bind"`
	LogDir           string `long:"access-log-dir" default:"" description:"directory to store logfiles"`
	LogRotate        int64  `long:"access-log-rotate" default:"30" description:"Number of day before remove logs"`
	Version          bool   `short:"v" long:"version" description:"Show version"`
	PidFile          string `long:"pid-file" default:"" description:"filename to store pid. disabled by default"`
	KeepaliveConns   int    `short:"c" default:"2" long:"keepalive-conns" description:"maximum keepalive connections for upstream"`
	MaxConnsPerHost  int    `long:"max-conns-per-host" default:"0" description:"maximum connections per host"`
	ReadTimeout      int    `long:"read-timeout" default:"30" description:"timeout of reading request"`
	WriteTimeout     int    `long:"write-timeout" default:"90" description:"timeout of writing response"`
	ProxyReadTimeout int    `long:"proxy-read-timeout" default:"60" description:"timeout of reading response from upstream"`
	Upstream         string `long:"upstream" default:"" description:"upstream server: http://upstream-server/"`
	StatsBufsize     int    `long:"stsize" default:"1000" description:"buffer size for http stats"`
	StatsSpfactor    int    `long:"spfactor" default:"3" description:"sampling factor for http stats"`
}

func addStatsHandler(h http.Handler, mw *statsHTTP.Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Index(r.URL.Path, "/.api/stats") == 0 {
			stats_api.Handler(w, r)
		} else if strings.Index(r.URL.Path, "/.api/http-stats") == 0 {
			d, err := mw.Data()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			if err := json.NewEncoder(w).Encode(d); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		} else {
			h.ServeHTTP(w, r)
		}
	})
}

func wrapLogHandler(h http.Handler, logDir string, logRotate int64, logger *zap.Logger) http.Handler {
	al, err := accesslog.New(logDir, logRotate)
	if err != nil {
		logger.Fatal("could not init accesslog", zap.Error(err))
	}
	return al.WrapHandleFunc(h)
}

func wrapStatsHandler(h http.Handler, mw *statsHTTP.Metrics) http.Handler {
	return mw.WrapHandleFunc(h)
}

func makeTransport(keepaliveConns int, maxConnsPerHost int, proxyReadTimeout int) http.RoundTripper {
	return &http.Transport{
		// inherited http.DefaultTransport
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// self-customized values
		MaxIdleConnsPerHost:   keepaliveConns,
		MaxConnsPerHost:       maxConnsPerHost,
		ResponseHeaderTimeout: time.Duration(proxyReadTimeout) * time.Second,
	}
}

func printVersion() {
	fmt.Printf(`chocon %s
Compiler: %s %s
`,
		Version,
		runtime.Compiler,
		runtime.Version())
}

func main() {
	opts := cmdOpts{}
	psr := flags.NewParser(&opts, flags.Default)
	_, err := psr.Parse()
	if err != nil {
		os.Exit(1)
	}

	if opts.Version {
		printVersion()
		return
	}

	logger, _ := zap.NewProduction()
	upstream, err := upstream.New(opts.Upstream, logger)
	if err != nil {
		log.Fatal(err)
	}

	if opts.PidFile != "" {
		err = pidfile.WritePid(opts.PidFile)
		if err != nil {
			log.Fatal(err)
		}
	}

	transport := makeTransport(opts.KeepaliveConns, opts.MaxConnsPerHost, opts.ProxyReadTimeout)
	var handler http.Handler = proxy.New(&transport, Version, upstream, logger)

	statsChocon, err := statsHTTP.NewCapa(opts.StatsBufsize, opts.StatsSpfactor)
	if err != nil {
		log.Fatal(err)
	}
	handler = addStatsHandler(handler, statsChocon)
	handler = wrapLogHandler(handler, opts.LogDir, opts.LogRotate, logger)
	handler = wrapStatsHandler(handler, statsChocon)

	server := http.Server{
		Handler:      handler,
		ReadTimeout:  time.Duration(opts.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(opts.WriteTimeout) * time.Second,
	}

	l, err := ss.NewListener()
	if l == nil || err != nil {
		// Fallback if not running under Server::Starter
		l, err = net.Listen("tcp", fmt.Sprintf("%s:%s", opts.Listen, opts.Port))
		if err != nil {
			logger.Fatal("Failed to listen to port",
				zap.Error(err),
				zap.String("listen", opts.Listen),
				zap.String("port", opts.Port))
		}
	}
	server.Serve(l)
}
