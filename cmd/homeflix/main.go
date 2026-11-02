// Command homeflix is a media server that speaks the Jellyfin client API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"homeflix/internal/config"
	"homeflix/internal/jellyfin"
	"homeflix/internal/store"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const defaultConfigPath = "/usr/local/etc/homeflix.toml"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "homeflix:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: homeflix <command> [flags]

Commands:
  serve     run the server
  version   print the version

Run "homeflix <command> -h" for the flags of a command.
`)
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return errors.New("missing command")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "version":
		fmt.Fprintf(stdout, "homeflix %s (%s, %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	level, _ := cfg.SlogLevel()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	serverID, err := db.ServerID(ctx)
	if err != nil {
		return fmt.Errorf("server ID: %w", err)
	}

	api := &jellyfin.Server{ServerID: serverID, ServerName: cfg.ServerName, Log: log}
	httpServer := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("homeflix started",
		"version", version,
		"listen", ln.Addr().String(),
		"server_name", cfg.ServerName,
		"server_id", serverID,
		"data_dir", cfg.DataDir,
		"config", *configPath)

	errc := make(chan error, 1)
	go func() { errc <- httpServer.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}
