package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/coreos/pkg/capnslog"
	"github.com/coreos/pkg/flagutil"

	web "github.com/coreos/coreos-baremetal/bootcfg/http"
	"github.com/coreos/coreos-baremetal/bootcfg/rpc"
	"github.com/coreos/coreos-baremetal/bootcfg/server"
	"github.com/coreos/coreos-baremetal/bootcfg/sign"
	"github.com/coreos/coreos-baremetal/bootcfg/storage"
	"github.com/coreos/coreos-baremetal/bootcfg/version"
	"github.com/coreos/coreos-baremetal/bootcfg/tlsutil"
)

var (
	log = capnslog.NewPackageLogger("github.com/coreos/coreos-baremetal/cmd/bootcfg", "main")
)

func main() {
	flags := struct {
		address     string
		rpcAddress  string
		dataPath    string
		assetsPath  string
		logLevel    string
		certFile string
		keyFile string
		keyRingPath string
		version     bool
		help        bool
	}{}
	flag.StringVar(&flags.address, "address", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&flags.rpcAddress, "rpc-address", "", "RPC listen address")
	flag.StringVar(&flags.dataPath, "data-path", "/var/lib/bootcfg", "Path to data directory")
	flag.StringVar(&flags.assetsPath, "assets-path", "/var/lib/bootcfg/assets", "Path to static assets")
	
	// Log levels https://godoc.org/github.com/coreos/pkg/capnslog#LogLevel
	flag.StringVar(&flags.logLevel, "log-level", "info", "Set the logging level")

	// gRPC Server TLS
	flag.StringVar(&flags.certFile, "cert-file", "/etc/bootcfg/server.crt", "Path to the server TLS certificate file")
	flag.StringVar(&flags.keyFile, "key-file", "/etc/bootcfg/server.key", "Path to the server TLS key file")

	// Signing
	flag.StringVar(&flags.keyRingPath, "key-ring-path", "", "Path to a private keyring file")
	
	// subcommands
	flag.BoolVar(&flags.version, "version", false, "print version and exit")
	flag.BoolVar(&flags.help, "help", false, "print usage and exit")

	// parse command-line and environment variable arguments
	flag.Parse()
	if err := flagutil.SetFlagsFromEnv(flag.CommandLine, "BOOTCFG"); err != nil {
		log.Fatal(err.Error())
	}
	// restrict OpenPGP passphrase to pass via environment variable only
	passphrase := os.Getenv("BOOTCFG_PASSPHRASE")

	if flags.version {
		fmt.Println(version.Version)
		return
	}

	if flags.help {
		flag.Usage()
		return
	}

	// validate arguments
	if url, err := url.Parse(flags.address); err != nil || url.String() == "" {
		log.Fatal("A valid HTTP listen address is required")
	}
	if finfo, err := os.Stat(flags.dataPath); err != nil || !finfo.IsDir() {
		log.Fatal("A valid -data-path is required")
	}
	if flags.assetsPath != "" {
		if finfo, err := os.Stat(flags.assetsPath); err != nil || !finfo.IsDir() {
			log.Printf("Provide a valid -assets-path or '' to disable asset serving: %s", flags.assetsPath)
		}
	}

	if flags.rpcAddress != "" {
		if flags.certFile == "" || flags.keyFile == "" {
			log.Fatalf("Provide a server -cert and -key to be able to use the gRPC API")
		}
	}

	// logging setup
	lvl, err := capnslog.ParseLevel(strings.ToUpper(flags.logLevel))
	if err != nil {
		log.Fatalf("invalid log-level: %v", err)
	}
	capnslog.SetGlobalLogLevel(lvl)
	capnslog.SetFormatter(capnslog.NewPrettyFormatter(os.Stdout, false))

	// (optional) signing
	var signer, armoredSigner sign.Signer
	if flags.keyRingPath != "" {
		entity, err := sign.LoadGPGEntity(flags.keyRingPath, passphrase)
		if err != nil {
			log.Fatal(err)
		}
		signer = sign.NewGPGSigner(entity)
		armoredSigner = sign.NewArmoredGPGSigner(entity)
	}

	// storage
	store := storage.NewFileStore(&storage.Config{
		Root: flags.dataPath,
	})

	// core logic
	server := server.NewServer(&server.Config{
		Store: store,
	})

	// gRPC Server (feature disabled by default)
	if flags.rpcAddress != "" {
		log.Infof("starting bootcfg gRPC server on %s", flags.rpcAddress)
		lis, err := net.Listen("tcp", flags.rpcAddress)
		if err != nil {
			log.Fatalf("failed to start listening: %v", err)
		}
		tlsinfo := tlsutil.TLSInfo{
			CertFile: flags.certFile,
			KeyFile: flags.keyFile,
		}
		tlscfg, err := tlsinfo.ServerConfig()
		if err != nil {
			log.Fatalf("Invalid TLS credentials", err)
		}
		grpcServer := rpc.NewServer(server, tlscfg)
		go grpcServer.Serve(lis)
		defer grpcServer.Stop()
	}

	// HTTP Server
	config := &web.Config{
		Store:         store,
		AssetsPath:    flags.assetsPath,
		Signer:        signer,
		ArmoredSigner: armoredSigner,
	}
	httpServer := web.NewServer(config)
	log.Infof("starting bootcfg HTTP server on %s", flags.address)
	err = http.ListenAndServe(flags.address, httpServer.HTTPHandler())
	if err != nil {
		log.Fatalf("failed to start listening: %v", err)
	}
}
