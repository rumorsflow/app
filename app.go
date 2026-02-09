package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
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
	LoadConfig(outs ...any) error
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
	ConfigUnmarshal func(data []byte, out any) error
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
	configUnmarshal func(data []byte, out any) error
	fxApp           *fx.App
	fxLogger        fxevent.Logger
	onBootstrap     *hook.Hook[*BootEvent]
	onStart         *hook.Hook[*StartEvent]
	onStop          *hook.Hook[*StopEvent]
}

func BootOptions(options ...fx.Option) func(*BootEvent) error {
	return func(event *BootEvent) error {
		event.Options = append(event.Options, options...)
		return event.Next()
	}
}

func BootConfig[C any]() func(*BootEvent) error {
	return func(event *BootEvent) error {
		var cfg C
		if err := event.App.LoadConfig(&cfg); err != nil {
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

func (app *BaseApp) LoadConfig(outs ...any) error {
	files := make([][]byte, len(app.configFiles))
	for i, file := range app.configFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		files[i] = data
	}

	for _, out := range outs {
		if len(app.configRaw) > 0 {
			if err := app.configUnmarshal(app.configRaw, out); err != nil {
				return err
			}
		}

		for _, file := range files {
			if err := app.configUnmarshal(file, out); err != nil {
				return err
			}
		}

		if err := env.ParseWithOptions(out, env.Options{Prefix: app.envPrefix}); err != nil {
			return err
		}

		if c, ok := out.(defaulter); ok {
			c.SetDefaults()
		}

		if v, ok := out.(validatable); ok {
			if err := v.Validate(); err != nil {
				return err
			}
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
	ctx, cancel := context.WithTimeout(ctx, app.stopTimeout)
	defer cancel()

	event := &StopEvent{App: app, Ctx: ctx}

	return app.OnStop().Trigger(event, app.stop)
}

func (app *BaseApp) Restart(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return errors.New("app: restart is not supported on windows")
	}

	ctx, cancel := context.WithTimeout(ctx, app.stopTimeout)
	defer cancel()

	event := &StopEvent{App: app, Ctx: ctx, IsRestart: true}

	return app.OnStop().Trigger(event, app.stop, app.restart)
}

func (app *BaseApp) Run(ctx context.Context) error {
	if err := app.Boot(ctx); err != nil {
		return err
	}

	if err := app.Start(ctx); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		sig := <-app.fxApp.Wait()
		app.logSignal(sig.Signal)

		return app.Stop(ctx)
	}

	restartSignal := make(chan os.Signal, 1)
	signal.Notify(restartSignal, syscall.SIGUSR1)

	defer signal.Stop(restartSignal)

	select {
	case sig := <-app.fxApp.Wait():
		app.logSignal(sig.Signal)

		return app.Stop(ctx)
	case sig := <-restartSignal:
		app.logSignal(sig)

		return app.Restart(ctx)
	}
}

func (app *BaseApp) logSignal(sig os.Signal) {
	app.fxLogger.LogEvent(&fxevent.Stopping{Signal: sig})
}

func (app *BaseApp) start(event *StartEvent) error {
	if err := app.fxApp.Start(event.Ctx); err != nil {
		return fmt.Errorf("app: unable to start: %w", err)
	}

	return event.Next()
}

func (app *BaseApp) stop(event *StopEvent) error {
	if err := app.fxApp.Stop(event.Ctx); err != nil {
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

	app.reset()

	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(execPath, os.Args, os.Environ())
}

func (app *BaseApp) reset() {
	app.fxApp = nil
	app.fxLogger = nil
}

func (app *BaseApp) createFxApp(event *BootEvent) error {
	if event.Logger == nil {
		return errors.New("app: bootstrap fx event logger is nil")
	}

	app.fxLogger = event.Logger

	app.fxApp = fx.New(
		fx.StartTimeout(app.startTimeout),
		fx.StopTimeout(app.stopTimeout),
		fx.WithLogger(func() fxevent.Logger { return app.fxLogger }),
		fx.Supply(fx.Annotate(app, fx.As(new(App)))),
		fx.Options(event.Options...),
	)

	return event.Next()
}
