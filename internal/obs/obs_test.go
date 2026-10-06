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
	var a, b, c bytes.Buffer
	ha := slog.NewTextHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo})
	hb := slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})
	hc := slog.NewTextHandler(&c, &slog.HandlerOptions{Level: slog.LevelError})

	m := multiHandler{ha, hb, hc}
	// Any-enabled semantics: one child allows Debug even though the others don't.
	if !m.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("enabled for debug expected: one child handler allows debug")
	}
	if !m.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("enabled for info expected")
	}
	if err := m.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelDebug, "hello", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if strings.Contains(a.String(), "hello") {
		t.Fatalf("info-level handler must not receive a debug record, got %q", a.String())
	}
	if !strings.Contains(b.String(), "hello") {
		t.Fatalf("debug-level handler must receive the debug record, got %q", b.String())
	}
	if strings.Contains(c.String(), "hello") {
		t.Fatalf("error-level handler must not receive a debug record, got %q", c.String())
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
