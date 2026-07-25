package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/rumorsflow/app"
)

var errSentinel = errors.New("sentinel")

func jsonUnmarshal(_ context.Context, data []byte, out any) error {
	return json.Unmarshal(data, out)
}

type netConfig struct {
	Addr string `env:"ADDR" json:"addr"`
	Port int    `env:"PORT" json:"port"`
}

type defaultedConfig struct {
	Addr string `json:"addr"`

	defaultsApplied bool
}

func (c *defaultedConfig) SetDefaults() {
	c.defaultsApplied = true
	if c.Addr == "" {
		c.Addr = "localhost"
	}
}

type validatedConfig struct {
	Fail bool `json:"fail"`

	validateCalls int
}

func (c *validatedConfig) Validate() error {
	c.validateCalls++
	if c.Fail {
		return errSentinel
	}
	return nil
}

type validatedCtxConfig struct {
	validateCalls int
}

func (c *validatedCtxConfig) Validate(context.Context) error {
	c.validateCalls++
	return nil
}

type ctxValidatedConfig struct {
	Fail bool `json:"fail"`

	validateCalls    int
	ctxValidateCalls int
}

func (c *ctxValidatedConfig) Validate() error {
	c.validateCalls++
	return nil
}

func (c *ctxValidatedConfig) ValidateWithContext(context.Context) error {
	c.ctxValidateCalls++
	if c.Fail {
		return errSentinel
	}
	return nil
}

func configApp(cfg app.Config) *app.BaseApp {
	if cfg.ConfigUnmarshal == nil {
		cfg.ConfigUnmarshal = jsonUnmarshal
	}
	return app.NewBaseApp(cfg)
}

func newApp(t *testing.T, opts ...fx.Option) *app.BaseApp {
	t.Helper()

	a := app.NewBaseApp(app.Config{
		StartTimeout: 10 * time.Second,
		StopTimeout:  10 * time.Second,
		Name:         "test-app",
		Version:      "0.0.1",
	})
	if len(opts) > 0 {
		a.OnBoot().BindFunc(app.Options(opts...))
	}
	return a
}

func newStartedApp(t *testing.T, opts ...fx.Option) *app.BaseApp {
	t.Helper()

	a := newApp(t, opts...)
	ctx := context.Background()
	if err := a.Boot(ctx); err != nil {
		t.Fatalf("Boot() = %v", err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	return a
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	return path
}

func startRun(t *testing.T, a *app.BaseApp) (<-chan error, <-chan struct{}) {
	t.Helper()

	started := make(chan struct{})
	a.OnStart().BindFunc(func(e *app.StartEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		close(started)
		return nil
	})

	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(context.Background()) }()
	return runErr, started
}

func waitClosed(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal(msg)
	}
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Run to return")
		return nil
	}
}

func TestNewBaseApp(t *testing.T) {
	a := app.NewBaseApp(app.Config{
		StartTimeout: time.Minute,
		StopTimeout:  time.Second,
		Name:         "svc",
		Version:      "1.2.3",
	})

	if got := a.Name(); got != "svc" {
		t.Errorf("Name() = %q, want %q", got, "svc")
	}
	if got := a.Version(); got != "1.2.3" {
		t.Errorf("Version() = %q, want %q", got, "1.2.3")
	}
	if got := a.StartTimeout(); got != time.Minute {
		t.Errorf("StartTimeout() = %v, want %v", got, time.Minute)
	}
	if got := a.StopTimeout(); got != time.Second {
		t.Errorf("StopTimeout() = %v, want %v", got, time.Second)
	}
}

func TestLoadConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("raw config", func(t *testing.T) {
		a := configApp(app.Config{ConfigRaw: []byte(`{"addr":"raw","port":1}`)})

		var cfg netConfig
		if err := a.LoadConfig(ctx, &cfg); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if cfg.Addr != "raw" || cfg.Port != 1 {
			t.Errorf("cfg = %+v, want Addr=raw Port=1", cfg)
		}
	})

	t.Run("file overrides raw", func(t *testing.T) {
		a := configApp(app.Config{
			ConfigRaw:   []byte(`{"addr":"raw","port":1}`),
			ConfigFiles: []string{writeConfigFile(t, `{"addr":"file"}`)},
		})

		var cfg netConfig
		if err := a.LoadConfig(ctx, &cfg); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if cfg.Addr != "file" || cfg.Port != 1 {
			t.Errorf("cfg = %+v, want Addr=file Port=1", cfg)
		}
	})

	t.Run("env overrides file", func(t *testing.T) {
		t.Setenv("TESTAPP_ADDR", "env")

		a := configApp(app.Config{
			ConfigFiles: []string{writeConfigFile(t, `{"addr":"file","port":1}`)},
			EnvPrefix:   "TESTAPP_",
		})

		var cfg netConfig
		if err := a.LoadConfig(ctx, &cfg); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if cfg.Addr != "env" || cfg.Port != 1 {
			t.Errorf("cfg = %+v, want Addr=env Port=1", cfg)
		}
	})

	t.Run("env parse error", func(t *testing.T) {
		t.Setenv("TESTAPP_PORT", "not-a-number")

		a := configApp(app.Config{EnvPrefix: "TESTAPP_"})

		var cfg netConfig
		if err := a.LoadConfig(ctx, &cfg); err == nil {
			t.Error("LoadConfig() = nil, want env parse error")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		a := configApp(app.Config{ConfigFiles: []string{filepath.Join(t.TempDir(), "missing.json")}})

		var cfg netConfig
		err := a.LoadConfig(ctx, &cfg)
		if err == nil || !strings.Contains(err.Error(), "failed to read config file") {
			t.Errorf("LoadConfig() = %v, want read error", err)
		}
	})

	t.Run("unmarshal error", func(t *testing.T) {
		a := configApp(app.Config{ConfigRaw: []byte(`{invalid`)})

		var cfg netConfig
		if err := a.LoadConfig(ctx, &cfg); err == nil {
			t.Error("LoadConfig() = nil, want unmarshal error")
		}
	})

	t.Run("defaults applied", func(t *testing.T) {
		a := configApp(app.Config{})

		var cfg defaultedConfig
		if err := a.LoadConfig(ctx, &cfg); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if !cfg.defaultsApplied {
			t.Error("SetDefaults was not called")
		}
		if cfg.Addr != "localhost" {
			t.Errorf("cfg.Addr = %q, want %q", cfg.Addr, "localhost")
		}
	})

	t.Run("validate called once", func(t *testing.T) {
		a := configApp(app.Config{})

		var cfg validatedConfig
		if err := a.LoadConfig(ctx, &cfg); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if cfg.validateCalls != 1 {
			t.Errorf("Validate called %d times, want 1", cfg.validateCalls)
		}
	})

	t.Run("validate context called once", func(t *testing.T) {
		a := configApp(app.Config{})

		var cfg validatedCtxConfig
		if err := a.LoadConfig(ctx, &cfg); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if cfg.validateCalls != 1 {
			t.Errorf("Validate called %d times, want 1", cfg.validateCalls)
		}
	})

	t.Run("validate error wrapped", func(t *testing.T) {
		a := configApp(app.Config{ConfigRaw: []byte(`{"fail":true}`)})

		var cfg validatedConfig
		err := a.LoadConfig(ctx, &cfg)
		if !errors.Is(err, errSentinel) {
			t.Errorf("LoadConfig() = %v, want wrapped %v", err, errSentinel)
		}
		if err == nil || !strings.Contains(err.Error(), "failed to validate config") {
			t.Errorf("LoadConfig() = %v, want validation prefix", err)
		}
	})

	t.Run("context validation preferred", func(t *testing.T) {
		a := configApp(app.Config{})

		var cfg ctxValidatedConfig
		if err := a.LoadConfig(ctx, &cfg); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if cfg.ctxValidateCalls != 1 {
			t.Errorf("ValidateWithContext called %d times, want 1", cfg.ctxValidateCalls)
		}
		if cfg.validateCalls != 0 {
			t.Errorf("Validate called %d times, want 0", cfg.validateCalls)
		}
	})

	t.Run("multiple outs", func(t *testing.T) {
		a := configApp(app.Config{ConfigRaw: []byte(`{"addr":"x","port":7}`)})

		var nc netConfig
		var dc defaultedConfig
		if err := a.LoadConfig(ctx, &nc, &dc); err != nil {
			t.Fatalf("LoadConfig() = %v", err)
		}
		if nc.Addr != "x" || nc.Port != 7 {
			t.Errorf("nc = %+v, want Addr=x Port=7", nc)
		}
		if dc.Addr != "x" || !dc.defaultsApplied {
			t.Errorf("dc = %+v, want Addr=x with defaults applied", dc)
		}
	})
}

func TestLoadConfigHookSuppliesConfig(t *testing.T) {
	a := configApp(app.Config{
		StartTimeout: 10 * time.Second,
		StopTimeout:  10 * time.Second,
		ConfigRaw:    []byte(`{"addr":"cfg","port":8080}`),
	})

	var got netConfig
	a.OnBoot().BindFunc(app.LoadConfig[netConfig]())
	a.OnBoot().BindFunc(app.Options(fx.Populate(&got)))

	if err := a.Boot(context.Background()); err != nil {
		t.Fatalf("Boot() = %v", err)
	}
	if got.Addr != "cfg" || got.Port != 8080 {
		t.Errorf("populated config = %+v, want Addr=cfg Port=8080", got)
	}
}

func TestLoadConfigHookError(t *testing.T) {
	a := configApp(app.Config{
		StartTimeout: 10 * time.Second,
		StopTimeout:  10 * time.Second,
		ConfigRaw:    []byte(`{invalid`),
	})
	a.OnBoot().BindFunc(app.LoadConfig[netConfig]())

	if err := a.Boot(context.Background()); err == nil {
		t.Error("Boot() = nil, want config load error")
	}
}

func TestBootSuppliesAppInterface(t *testing.T) {
	a := newApp(t)

	var got app.App
	a.OnBoot().BindFunc(app.Options(fx.Populate(&got)))

	if err := a.Boot(context.Background()); err != nil {
		t.Fatalf("Boot() = %v", err)
	}
	if got != a {
		t.Errorf("populated App = %v, want the BaseApp itself", got)
	}
}

func TestBootNilLogger(t *testing.T) {
	a := newApp(t)
	a.OnBoot().BindFunc(func(e *app.BootEvent) error {
		e.Logger = nil
		return e.Next()
	})

	err := a.Boot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "logger is nil") {
		t.Errorf("Boot() = %v, want nil-logger error", err)
	}
}

func TestBootHookError(t *testing.T) {
	a := newApp(t)
	a.OnBoot().BindFunc(func(*app.BootEvent) error { return errSentinel })

	if err := a.Boot(context.Background()); !errors.Is(err, errSentinel) {
		t.Errorf("Boot() = %v, want %v", err, errSentinel)
	}
}

func TestStartStopLifecycleOrder(t *testing.T) {
	var order []string

	a := newApp(t, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{
			OnStart: func(context.Context) error {
				order = append(order, "fx-start")
				return nil
			},
			OnStop: func(context.Context) error {
				order = append(order, "fx-stop")
				return nil
			},
		})
	}))
	a.OnStart().BindFunc(func(e *app.StartEvent) error {
		order = append(order, "hook-start-before")
		if err := e.Next(); err != nil {
			return err
		}
		order = append(order, "hook-start-after")
		return nil
	})
	a.OnStop().BindFunc(func(e *app.StopEvent) error {
		order = append(order, "hook-stop-before")
		if err := e.Next(); err != nil {
			return err
		}
		order = append(order, "hook-stop-after")
		return nil
	})

	ctx := context.Background()
	if err := a.Boot(ctx); err != nil {
		t.Fatalf("Boot() = %v", err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if err := a.Stop(ctx); err != nil {
		t.Fatalf("Stop() = %v", err)
	}

	want := []string{
		"hook-start-before", "fx-start", "hook-start-after",
		"hook-stop-before", "fx-stop", "hook-stop-after",
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("lifecycle order = %v, want %v", order, want)
	}
}

func TestStartNotBooted(t *testing.T) {
	a := newApp(t)

	err := a.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not booted") {
		t.Errorf("Start() = %v, want not-booted error", err)
	}
}

func TestStartError(t *testing.T) {
	a := newApp(t, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{
			OnStart: func(context.Context) error { return errSentinel },
		})
	}))

	if err := a.Boot(context.Background()); err != nil {
		t.Fatalf("Boot() = %v", err)
	}

	err := a.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "app: unable to start") {
		t.Errorf("Start() = %v, want wrapped start error", err)
	}
	if err == nil || !strings.Contains(err.Error(), errSentinel.Error()) {
		t.Errorf("Start() = %v, want cause %v", err, errSentinel)
	}
}

func TestStopNotBooted(t *testing.T) {
	a := newApp(t)

	err := a.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not booted") {
		t.Errorf("Stop() = %v, want not-booted error", err)
	}

	// The app is terminal after the first Stop; later calls return the
	// recorded outcome instead of running the shutdown again.
	if second := a.Stop(context.Background()); !errors.Is(second, err) {
		t.Errorf("second Stop() = %v, want %v", second, err)
	}
}

func TestStopIdempotent(t *testing.T) {
	var stops atomic.Int32

	a := newStartedApp(t, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{
			OnStop: func(context.Context) error {
				stops.Add(1)
				return nil
			},
		})
	}))

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop() = %v", err)
	}
	if got := stops.Load(); got != 1 {
		t.Errorf("fx OnStop ran %d times, want 1", got)
	}
}

func TestStopConcurrent(t *testing.T) {
	var stops atomic.Int32

	a := newStartedApp(t, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{
			OnStop: func(context.Context) error {
				stops.Add(1)
				return nil
			},
		})
	}))

	errs := make([]error, 5)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			errs[i] = a.Stop(context.Background())
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Stop() #%d = %v, want nil", i, err)
		}
	}
	if got := stops.Load(); got != 1 {
		t.Errorf("fx OnStop ran %d times, want 1", got)
	}
}

func TestStopError(t *testing.T) {
	a := newStartedApp(t, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{
			OnStop: func(context.Context) error { return errSentinel },
		})
	}))

	err := a.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), errSentinel.Error()) {
		t.Errorf("Stop() = %v, want error containing %v", err, errSentinel)
	}

	// The failed outcome is terminal too.
	if second := a.Stop(context.Background()); !errors.Is(second, err) {
		t.Errorf("second Stop() = %v, want %v", second, err)
	}
}

func TestStopKeepsCallerCancellation(t *testing.T) {
	a := newStartedApp(t)

	var ctxErr error
	a.OnStop().BindFunc(func(e *app.StopEvent) error {
		ctxErr = e.Ctx.Err()
		// Short-circuit: stopping fx with a dead context would just error.
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := a.Stop(ctx); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if !errors.Is(ctxErr, context.Canceled) {
		t.Errorf("stop event ctx error = %v, want %v", ctxErr, context.Canceled)
	}
}

// Restart tests must short-circuit the OnStop chain: letting it reach the
// default handlers would exec over the test binary.

func TestRestartIsTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("restart is not supported on windows")
	}

	a := newStartedApp(t)

	var isRestart atomic.Bool
	a.OnStop().BindFunc(func(e *app.StopEvent) error {
		isRestart.Store(e.IsRestart)
		return errSentinel
	})

	if err := a.Restart(context.Background()); !errors.Is(err, errSentinel) {
		t.Fatalf("Restart() = %v, want %v", err, errSentinel)
	}
	if !isRestart.Load() {
		t.Error("stop event IsRestart = false, want true")
	}

	// Every later shutdown call observes the recorded outcome.
	if err := a.Stop(context.Background()); !errors.Is(err, errSentinel) {
		t.Errorf("Stop() after Restart = %v, want %v", err, errSentinel)
	}
	if err := a.Restart(context.Background()); !errors.Is(err, errSentinel) {
		t.Errorf("second Restart() = %v, want %v", err, errSentinel)
	}
}

func TestRestartDropsCallerCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("restart is not supported on windows")
	}

	a := newStartedApp(t)

	var ctxErr error
	a.OnStop().BindFunc(func(e *app.StopEvent) error {
		ctxErr = e.Ctx.Err()
		return errSentinel
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := a.Restart(ctx); !errors.Is(err, errSentinel) {
		t.Fatalf("Restart() = %v, want %v", err, errSentinel)
	}
	if ctxErr != nil {
		t.Errorf("restart event ctx error = %v, want nil", ctxErr)
	}
}

func TestRunBootError(t *testing.T) {
	a := newApp(t)
	a.OnBoot().BindFunc(func(*app.BootEvent) error { return errSentinel })

	if err := a.Run(context.Background()); !errors.Is(err, errSentinel) {
		t.Errorf("Run() = %v, want %v", err, errSentinel)
	}
}

func TestRunUnblocksOnDirectStop(t *testing.T) {
	a := newApp(t)
	runErr, started := startRun(t, a)
	waitClosed(t, started, "app did not start")

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if err := waitErr(t, runErr); err != nil {
		t.Errorf("Run() = %v, want nil", err)
	}
}

func TestRunStopsOnSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-driven shutdown is not exercised on windows")
	}

	// Keep SIGTERM from killing the test binary when a kill below lands
	// while no other handler is registered. Intentionally never stopped:
	// restoring the default disposition with a re-sent signal in flight
	// would terminate the test process.
	safety := make(chan os.Signal, 1)
	signal.Notify(safety, syscall.SIGTERM)

	a := newApp(t)
	runErr, started := startRun(t, a)
	waitClosed(t, started, "app did not start")

	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-runErr:
			if err != nil {
				t.Fatalf("Run() = %v, want nil", err)
			}
			return
		case <-deadline:
			t.Fatal("Run did not return after SIGTERM")
		case <-time.After(50 * time.Millisecond):
			// Re-send until fx's receivers, started inside Run's select,
			// pick it up.
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				t.Fatalf("kill: %v", err)
			}
		}
	}
}

func TestRunRestartsOnSIGUSR1(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("restart is not supported on windows")
	}

	// Same rationale as the SIGTERM safety net above.
	safety := make(chan os.Signal, 1)
	signal.Notify(safety, syscall.SIGUSR1)

	a := newApp(t)
	a.OnStop().BindFunc(func(e *app.StopEvent) error {
		if e.IsRestart {
			// Short-circuit before the exec replaces the test binary; the
			// sentinel proves the restart path was taken.
			return errSentinel
		}
		return e.Next()
	})

	runErr, started := startRun(t, a)
	waitClosed(t, started, "app did not start")

	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-runErr:
			if !errors.Is(err, errSentinel) {
				t.Fatalf("Run() = %v, want %v", err, errSentinel)
			}
			return
		case <-deadline:
			t.Fatal("Run did not return after SIGUSR1")
		case <-time.After(50 * time.Millisecond):
			// The package-level Restart sends SIGUSR1 to the own process.
			if err := app.Restart(); err != nil {
				t.Fatalf("Restart() = %v", err)
			}
		}
	}
}
