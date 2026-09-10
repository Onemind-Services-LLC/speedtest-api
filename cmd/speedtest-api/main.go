package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Onemind-Services-LLC/speedtest-api/internal/server"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	c, err := server.ConfigFromEnv()
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: c.LogLevel})))
	app, err := server.New(c)
	if err != nil {
		return err
	}
	if err := app.StartPacketLoss(); err != nil {
		return err
	}
	defer app.ClosePacketLoss()
	srv := app.HTTPServer()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- srv.ListenAndServe() }()
	slog.Info("speedtest API starting", "address", c.Address, "udp_address", c.WebRTCAddress, "region", c.RegionID, "protocol", server.ProtocolVersion, "log_level", c.LogLevel.String())
	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		app.SetReady(false)
		shutdown, cancel := context.WithTimeout(context.Background(), c.RequestTimeout+time.Second)
		defer cancel()
		slog.Info("draining active transfers")
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return err
		}
		slog.Info("speedtest API stopped", "region", c.RegionID)
		return nil
	}
}
