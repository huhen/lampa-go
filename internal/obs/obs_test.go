package obs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"lampa-go/internal/config"
)

func TestSetupDisabledUsesStdoutAndNoop(t *testing.T) {
	cfg := config.Defaults()
	cfg.OTel.Enable = false

	core, err := Setup(context.Background(), cfg.OTel, cfg.Log)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if core.Logger == nil || core.Tracer == nil || core.Meter == nil {
		t.Fatal("core must expose logger, tracer and meter")
	}
	if err := core.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"bogus": slog.LevelInfo, // fallback
		"":      slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMultiHandlerFanOut(t *testing.T) {
	var a, b bytes.Buffer
	ha := slog.NewTextHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo})
	hb := slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})

	m := multiHandler{ha, hb}
	if !m.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("enabled for info expected")
	}
	if err := m.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(a.String(), "hello") || !strings.Contains(b.String(), "hello") {
		t.Fatalf("both handlers must receive the record, got %q and %q", a.String(), b.String())
	}
	withAttrs := m.WithAttrs([]slog.Attr{slog.String("k", "v")})
	if withAttrs == nil {
		t.Fatal("WithAttrs must return a handler")
	}
	if m.WithGroup("g") == nil {
		t.Fatal("WithGroup must return a handler")
	}
}

func TestDisabledLoggerJsonFormat(t *testing.T) {
	cfg := config.Defaults()
	cfg.Log.Format = "json"
	core, err := Setup(context.Background(), cfg.OTel, cfg.Log)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	core.Logger.Info("startup", "version", "test")
	if err := core.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
