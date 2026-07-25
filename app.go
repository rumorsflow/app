package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gowool/hook"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

var _ App = (*BaseApp)(nil)

type (
	defaulter interface {
		SetDefaults()
	}
	validatable interface {
		Validate() error
	}
	validatableCtx interface {
		Validate(context.Context) error
	}
	validatableWithContext interface {
		ValidateWithContext(context.Context) error
	}
)

type BootEvent struct {
	hook.Event
	App     App
	Ctx     context.Context
	Logger  fxevent.Logger
	Options []fx.Option
}

type StartEvent struct {
	hook.Event
	App App
	Ctx context.Context
}

type StopEvent struct {
	hook.Event
	App       App
	Ctx       context.Context
	IsRestart bool
}

type App interface {
	Name() string
	Version() string
	LoadConfig(ctx context.Context, outs ...any) error
	OnBoot() *hook.Hook[*BootEvent]
	Boot(ctx context.Context) error
	StartTimeout() time.Duration
	OnStart() *hook.Hook[*StartEvent]
	Start(ctx context.Context) error
	StopTimeout() time.Duration
	OnStop() *hook.Hook[*StopEvent]
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Run(ctx context.Context) error
}

type Config struct {
	StartTimeout    time.Duration
	StopTimeout     time.Duration
	ConfigUnmarshal func(ctx context.Context, data []byte, out any) error
	ConfigRaw       []byte
	ConfigFiles     []string
	EnvPrefix       string
	Name            string
	Version         string
}

type BaseApp struct {
	startTimeout    time.Duration
	stopTimeout     time.Duration
	name            string
	version         string
	envPrefix       string
	configFiles     []string
	configRaw       []byte
	configUnmarshal func(ctx context.Context, data []byte, out any) error
	fxApp           atomic.Pointer[fx.App]
	fxLogger        fxevent.Logger
	onBootstrap     *hook.Hook[*BootEvent]
	onStart         *hook.Hook[*StartEvent]
	onStop          *hook.Hook[*StopEvent]
	stopping        atomic.Bool
	stopErr         error
	done            chan struct{}
}

func Options(options ...fx.Option) func(*BootEvent) error {
	return func(event *BootEvent) error {
		event.Options = append(event.Options, options...)
		return event.Next()
	}
}

func LoadConfig[C any]() func(*BootEvent) error {
	return func(event *BootEvent) error {
		var cfg C
		if err := event.App.LoadConfig(event.Ctx, &cfg); err != nil {
			return err
		}

		event.Options = append(event.Options, fx.Supply(cfg))

		return event.Next()
	}
}

func NewBaseApp(cfg Config) *BaseApp {
	return &BaseApp{
		startTimeout:    cfg.StartTimeout,
		stopTimeout:     cfg.StopTimeout,
		name:            cfg.Name,
		version:         cfg.Version,
		envPrefix:       cfg.EnvPrefix,
		configFiles:     cfg.ConfigFiles,
		configRaw:       cfg.ConfigRaw,
		configUnmarshal: cfg.ConfigUnmarshal,
		onBootstrap:     &hook.Hook[*BootEvent]{},
		onStart:         &hook.Hook[*StartEvent]{},
		onStop:          &hook.Hook[*StopEvent]{},
		done:            make(chan struct{}),
	}
}

func (app *BaseApp) Name() string {
	return app.name
}

func (app *BaseApp) Version() string {
	return app.version
}

func (app *BaseApp) StartTimeout() time.Duration {
	return app.startTimeout
}

func (app *BaseApp) StopTimeout() time.Duration {
	return app.stopTimeout
}

func (app *BaseApp) OnBoot() *hook.Hook[*BootEvent] {
	return app.onBootstrap
}

func (app *BaseApp) OnStart() *hook.Hook[*StartEvent] {
	return app.onStart
}

func (app *BaseApp) OnStop() *hook.Hook[*StopEvent] {
	return app.onStop
}

func (app *BaseApp) LoadConfig(ctx context.Context, outs ...any) error {
	files := make([][]byte, len(app.configFiles))
	for i, file := range app.configFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read config file %s: %w", file, err)
		}
		files[i] = data
	}

	for _, out := range outs {
		if len(app.configRaw) > 0 {
			if err := app.configUnmarshal(ctx, app.configRaw, out); err != nil {
				return err
			}
		}

		for _, file := range files {
			if err := app.configUnmarshal(ctx, file, out); err != nil {
				return err
			}
		}

		if err := env.ParseWithOptions(out, env.Options{Prefix: app.envPrefix}); err != nil {
			return err
		}

		if c, ok := out.(defaulter); ok {
			c.SetDefaults()
		}

		var err error
		switch v := out.(type) {
		case validatableWithContext:
			err = v.ValidateWithContext(ctx)
		case validatableCtx:
			err = v.Validate(ctx)
		case validatable:
			err = v.Validate()
		}
		if err != nil {
			return fmt.Errorf("failed to validate config: %w", err)
		}
	}
	return nil
}

func (app *BaseApp) Boot(ctx context.Context) error {
	event := &BootEvent{App: app, Ctx: ctx, Logger: fxevent.NopLogger}

	return app.OnBoot().Trigger(event, app.createFxApp)
}

func (app *BaseApp) Start(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, app.startTimeout)
	defer cancel()

	event := &StartEvent{App: app, Ctx: ctx}

	return app.OnStart().Trigger(event, app.start)
}

func (app *BaseApp) Stop(ctx context.Context) error {
	if !app.stopping.CompareAndSwap(false, true) {
		<-app.done
		return app.stopErr
	}

	ctx, cancel := context.WithTimeout(ctx, app.stopTimeout)
	defer cancel()

	event := &StopEvent{App: app, Ctx: ctx}

	return app.finish(app.OnStop().Trigger(event, app.stop))
}

// Restart stops the application and replaces the current process with a new
// instance of the same binary via exec, keeping the same PID. On success it
// never returns; stop errors are ignored so a failed graceful shutdown does
// not prevent the exec.
//
// When calling this from code managed by the application itself (an HTTP
// handler, a worker), detach it — otherwise graceful shutdown waits for the
// caller while the caller waits for shutdown, until the stop timeout expires:
//
//	go func() { _ = app.Restart(context.WithoutCancel(ctx)) }()
func (app *BaseApp) Restart(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return errors.New("app: restart is not supported on windows")
	}

	if !app.stopping.CompareAndSwap(false, true) {
		<-app.done
		return app.stopErr
	}

	// Restart is a point of no return: keep the caller's values but drop its
	// cancellation so a dying request cannot cut the shutdown short.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), app.stopTimeout)
	defer cancel()

	event := &StopEvent{App: app, Ctx: ctx, IsRestart: true}

	return app.finish(app.OnStop().Trigger(event, app.stop, app.restart))
}

// finish records the shutdown outcome and releases everyone blocked on it:
// concurrent Stop/Restart callers and Run's select.
func (app *BaseApp) finish(err error) error {
	app.stopErr = err
	close(app.done)
	return err
}

func (app *BaseApp) Run(ctx context.Context) error {
	if err := app.Boot(ctx); err != nil {
		return err
	}

	if err := app.Start(ctx); err != nil {
		return err
	}

	fxApp := app.fxApp.Load()
	if fxApp == nil {
		return errors.New("app: not booted")
	}

	if runtime.GOOS == "windows" {
		select {
		case sig := <-fxApp.Wait():
			app.logSignal(sig.Signal)

			return app.Stop(ctx)
		case <-app.done:
			return app.stopErr
		}
	}

	restartSignal := make(chan os.Signal, 1)
	signal.Notify(restartSignal, syscall.SIGUSR1)

	defer signal.Stop(restartSignal)

	select {
	case sig := <-fxApp.Wait():
		app.logSignal(sig.Signal)

		return app.Stop(ctx)
	case sig := <-restartSignal:
		app.logSignal(sig)

		return app.Restart(ctx)
	case <-app.done:
		// Stop or Restart was invoked directly by application code; the
		// process was not replaced, so surface the outcome and exit.
		return app.stopErr
	}
}

func (app *BaseApp) logSignal(sig os.Signal) {
	app.fxLogger.LogEvent(&fxevent.Stopping{Signal: sig})
}

func (app *BaseApp) start(event *StartEvent) error {
	fxApp := app.fxApp.Load()
	if fxApp == nil {
		return errors.New("app: not booted")
	}

	if err := fxApp.Start(event.Ctx); err != nil {
		return fmt.Errorf("app: unable to start: %w", err)
	}

	return event.Next()
}

func (app *BaseApp) stop(event *StopEvent) error {
	fxApp := app.fxApp.Load()
	if fxApp == nil {
		return errors.New("app: not booted")
	}

	if err := fxApp.Stop(event.Ctx); err != nil {
		if !event.IsRestart {
			return err
		}
	}

	return event.Next()
}

func (app *BaseApp) restart(event *StopEvent) error {
	if !event.IsRestart {
		return event.Next()
	}

	// /proc/self/exe stays valid even if the binary on disk was replaced or
	// deleted, unlike the path reported by os.Executable.
	execPath := "/proc/self/exe"
	if _, err := os.Stat(execPath); err != nil {
		if execPath, err = os.Executable(); err != nil {
			return err
		}
	}

	return syscall.Exec(execPath, os.Args, os.Environ())
}

func (app *BaseApp) createFxApp(event *BootEvent) error {
	if event.Logger == nil {
		return errors.New("app: bootstrap fx event logger is nil")
	}

	app.fxLogger = event.Logger

	app.fxApp.Store(fx.New(
		fx.StartTimeout(app.startTimeout),
		fx.StopTimeout(app.stopTimeout),
		fx.WithLogger(func() fxevent.Logger { return app.fxLogger }),
		fx.Supply(fx.Annotate(app, fx.As(new(App)))),
		fx.Options(event.Options...),
	))

	return event.Next()
}
