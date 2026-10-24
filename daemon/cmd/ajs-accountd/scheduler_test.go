package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"ajsaccountd/internal/store"
)

func TestStartSchedulerDisabledByDefault(t *testing.T) {
	t.Setenv("AJS_SCHEDULES", "")
	service, stop, err := startScheduler(context.Background(), store.NewEmpty(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(t.TempDir(), "state.json"), nil, 1, nil)
	if err != nil {
		t.Fatalf("startScheduler: %v", err)
	}
	if service != nil || stop != nil {
		t.Fatalf("disabled scheduler returned service=%t stop=%t", service != nil, stop != nil)
	}
}

func TestStartSchedulerEnabledStartsAndStops(t *testing.T) {
	t.Setenv("AJS_SCHEDULES", "1")
	t.Setenv("AJS_RENEWAL_REMINDERS", "0")
	t.Setenv("AJS_SCHEDULER_TICK", "1s")
	dir := t.TempDir()
	service, stop, err := startScheduler(context.Background(), store.NewEmpty(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(dir, "state.json"), nil, 1, nil)
	if err != nil {
		t.Fatalf("startScheduler: %v", err)
	}
	if service == nil || stop == nil {
		t.Fatal("enabled scheduler returned a nil service or stop function")
	}
	stop()
}
