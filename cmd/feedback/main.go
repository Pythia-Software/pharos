// feedback is the independent, scale-to-zero feedback intake and admin service.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gbdubs/pharos/internal/uifeedback"
	"golang.org/x/oauth2/google"
)

func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		panic(name + " is required")
	}
	return value
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	key, err := base64.StdEncoding.DecodeString(required("FEEDBACK_COOKIE_KEY"))
	if err != nil {
		return fmt.Errorf("FEEDBACK_COOKIE_KEY must be base64: %w", err)
	}
	origin := required("FEEDBACK_ORIGIN")
	a, err := uifeedback.NewAdminAuth(uifeedback.AuthConfig{Issuer: "https://auth.pythia.software", Project: required("FEEDBACK_AUTH_PROJECT"), Origin: origin, CookieKey: key})
	if err != nil {
		return err
	}
	// The JSON API requires full_control for metadata PATCH, including tags and
	// notification receipts. Bucket IAM still restricts this identity to objects.
	client, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/devstorage.full_control")
	if err != nil {
		return err
	}
	client.Timeout = 30 * time.Second
	store := &uifeedback.GCS{Client: client, Bucket: required("FEEDBACK_BUCKET")}
	notifications := &uifeedback.Notifications{Store: store, Email: uifeedback.Email{KeySID: required("TWILIO_API_KEY_SID"), KeySecret: required("TWILIO_API_KEY_SECRET"), From: required("FEEDBACK_EMAIL_FROM"), Origin: origin}}
	assets, err := fs.Sub(uifeedback.Assets, "assets")
	if err != nil {
		return err
	}
	service := &uifeedback.Server{Store: store, Auth: a, Assets: assets, Event: notifications.Handle, EventAccount: required("FEEDBACK_PUSH_ACCOUNT"), Bucket: store.Bucket}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{Addr: ":" + port, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("feedback service started", "port", port)
	if err = server.ListenAndServe(); err == http.ErrServerClosed {
		return nil
	}
	return err
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("feedback service failed", "error", err)
		os.Exit(1)
	}
}
