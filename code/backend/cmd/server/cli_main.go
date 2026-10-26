//go:build !desktop

package main

import (
	"context"
	"flag"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	host := flag.String("host", "", "listening host")
	port := flag.Int("port", 0, "listening TCP port")
	configPath := flag.String("config", "", "enterprise JSON configuration file (default: toolbox.json beside executable)")
	flag.Parse()
	manual := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "host" || f.Name == "port" {
			manual = true
		}
		if f.Name == "port" && (*port < 1 || *port > 65535) {
			panic("port must be between 1 and 65535")
		}
	})
	config, err := openEnterpriseConfig(*configPath)
	if err != nil {
		panic(err)
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			config.overrides["listen_port"] = "--port"
		}
		if f.Name == "host" {
			config.overrides["listen_host"] = "--host"
		}
	})
	if err = config.startupEnv(); err != nil {
		panic(err)
	}
	if err := runServer(ctx, serverOptions{Config: config, Host: *host, Port: *port, ManualPort: manual}); err != nil {
		panic(err)
	}
}
