# App

A Go base app structure with lifecycle management, configuration loading, and event hooks built on top of [Uber FX](https://github.com/uber-go/fx).

It gives you a small, opinionated skeleton for long-running services:

- **Lifecycle**: `Boot → Start → wait → Stop` (or `Restart`), driven by `Run`.
- **Hooks**: `OnBoot`, `OnStart` and `OnStop` middleware chains built on [gowool/hook](https://github.com/gowool/hook), so you can run code before and after each phase.
- **Configuration**: layered loading from raw bytes, files, environment variables and `.env`, followed by defaults and validation.
- **Signals**: `SIGINT`/`SIGTERM` stop the app gracefully, `SIGUSR1` restarts it in place (same PID) via `exec`.
- **Dependency injection**: everything you register ends up in an `fx.App`; the `app.App` instance itself is available to your constructors.

## Installation

```bash
go get github.com/rumorsflow/app
```

Requires Go 1.27+.

## Quick start

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/rumorsflow/app"
)

// Config is loaded from config.json and then overridden by MYAPP_* env vars.
type Config struct {
	Addr string `json:"addr" env:"ADDR"`
}

func (c *Config) SetDefaults() {
	if c.Addr == "" {
		c.Addr = ":8080"
	}
}

func (c *Config) Validate() error {
	if c.Addr == "" {
		return errors.New("addr is required")
	}
	return nil
}

func newServer(lc fx.Lifecycle, cfg Config, a app.App) *http.Server {
	srv := &http.Server{Addr: cfg.Addr}
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s %s\n", a.Name(), a.Version())
	})

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return err
			}
			go srv.Serve(ln)
			return nil
		},
		OnStop: srv.Shutdown,
	})
	return srv
}

func main() {
	a := app.NewBaseApp(app.Config{
		Name:         "myapp",
		Version:      "1.0.0",
		StartTimeout: 15 * time.Second,
		StopTimeout:  15 * time.Second,
		ConfigFiles:  []string{"config.json"},
		EnvPrefix:    "MYAPP_",
		ConfigUnmarshal: func(_ context.Context, data []byte, out any) error {
			return json.Unmarshal(data, out)
		},
	})

	// Use a real fx logger instead of the default fxevent.NopLogger.
	a.OnBoot().BindFunc(func(e *app.BootEvent) error {
		e.Logger = &fxevent.ConsoleLogger{W: os.Stderr}
		return e.Next()
	})

	// Load Config and supply it to the fx container.
	a.OnBoot().BindFunc(app.LoadConfig[Config]())

	// Register providers and invokes.
	a.OnBoot().BindFunc(app.Options(
		fx.Provide(newServer),
		fx.Invoke(func(*http.Server) {}),
	))

	if err := a.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

Run it, then:

```bash
curl localhost:8080          # -> myapp 1.0.0
kill -USR1 $(pidof myapp)    # graceful restart, same PID
kill -TERM $(pidof myapp)    # graceful stop
```

## Configuration

`NewBaseApp` takes an `app.Config`:

| Field             | Type                                                     | Description                                                                                                                 |
|-------------------|----------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------|
| `Name`            | `string`                                                 | Application name, exposed via `App.Name()`.                                                                                 |
| `Version`         | `string`                                                 | Application version, exposed via `App.Version()`.                                                                           |
| `StartTimeout`    | `time.Duration`                                          | Deadline for `Start` (hooks + fx `OnStart`). **Must be > 0**, otherwise the start context is already expired.               |
| `StopTimeout`     | `time.Duration`                                          | Deadline for `Stop`/`Restart` (hooks + fx `OnStop`). **Must be > 0**.                                                       |
| `ConfigUnmarshal` | `func(ctx context.Context, data []byte, out any) error`  | Decoder used for `ConfigRaw` and `ConfigFiles` (JSON, YAML, TOML, ...). If `nil`, raw bytes and files are skipped entirely. |
| `ConfigRaw`       | `[]byte`                                                 | Inline config document, decoded first.                                                                                      |
| `ConfigFiles`     | `[]string`                                               | Config file paths, decoded in order after `ConfigRaw`. A missing file is an error.                                          |
| `EnvPrefix`       | `string`                                                 | Prefix for environment variables (e.g. `MYAPP_`). Overrides `EnvOptions.Prefix` when non-empty.                             |
| `EnvOptions`      | `*env.Options`                                           | Extra options for [caarlos0/env](https://github.com/caarlos0/env) (required-if-no-default, tag name, custom parsers, ...).  |

## Loading configuration

`App.LoadConfig(ctx, &cfg1, &cfg2, ...)` fills each target in this order, so later sources override earlier ones:

1. `ConfigRaw` (if `ConfigUnmarshal` is set and the raw document is non-empty)
2. each entry of `ConfigFiles`, in order (if `ConfigUnmarshal` is set)
3. environment variables via `env.ParseWithOptions` using the `env:"..."` struct tags
4. `SetDefaults()` if the target implements it
5. validation, if the target implements one of the interfaces below

Because defaults are applied **after** all sources, `SetDefaults` should only fill zero values, as in the quick start above.

### Optional interfaces on config structs

```go
type defaulter interface{ SetDefaults() }

// Exactly one of these is called, in this order of preference:
type validatableWithContext interface{ ValidateWithContext(context.Context) error }
type validatableCtx         interface{ Validate(context.Context) error }
type validatable            interface{ Validate() error }
```

A validation error is returned wrapped as `failed to validate config: <err>`.

### `.env` files

On package init, `godotenv.Load()` is called, so a `.env` file in the working directory is loaded into the process environment. If `DOTENV_PATH` is set, that file is loaded as well. Existing environment variables are never overwritten by either file.

### The `LoadConfig` boot helper

`app.LoadConfig[C]()` returns an `OnBoot` handler that loads a `C` through `App.LoadConfig` and supplies it to the fx container via `fx.Supply`. Your constructors can then simply depend on `C`:

```go
a.OnBoot().BindFunc(app.LoadConfig[Config]())
a.OnBoot().BindFunc(app.Options(fx.Provide(func(cfg Config) *Thing { ... })))
```

You can call it several times with different types if your configuration is split across structs.

## Lifecycle

```
Run(ctx)
 ├─ Boot(ctx)     OnBoot chain   → builds the fx.App
 ├─ Start(ctx)    OnStart chain  → fxApp.Start   (bounded by StartTimeout)
 ├─ wait for: SIGINT/SIGTERM | SIGUSR1 | direct Stop()/Restart()
 └─ Stop(ctx)     OnStop chain   → fxApp.Stop    (bounded by StopTimeout)
    Restart(ctx)  OnStop chain   → fxApp.Stop → syscall.Exec(self)
```

- **`Boot`** triggers `OnBoot` and, at the end of the chain, creates the `fx.App` from the collected `BootEvent.Options`. The app registers itself in the container as `app.App`, so any constructor may take an `app.App` parameter. `BootEvent.Logger` starts as `fxevent.NopLogger`; replace it in a handler to get fx logs. Setting it to `nil` makes `Boot` fail.
- **`Start`** triggers `OnStart` and starts the fx app inside the chain.
- **`Stop`** triggers `OnStop` and stops the fx app. It is idempotent and safe to call concurrently: the first call performs the shutdown, every later call blocks until it finishes and returns the same result. A stopped app cannot be started again.
- **`Restart`** behaves like `Stop` with `StopEvent.IsRestart == true`, then replaces the current process with a fresh instance of the same binary (`/proc/self/exe`, falling back to `os.Executable()`) using the same arguments and environment. On success it never returns. fx stop errors are ignored so a failed graceful shutdown does not block the exec. Not supported on Windows.
- **`Run`** chains `Boot` and `Start`, then blocks until a signal arrives or `Stop`/`Restart` is called from application code, and returns the shutdown result.

### Signals

| Signal            | Behaviour                                   |
|-------------------|---------------------------------------------|
| `SIGINT`, `SIGTERM` | Graceful `Stop`, `Run` returns its error. |
| `SIGUSR1`         | Graceful `Restart` (Linux/macOS only).      |

To trigger a restart from inside the application, either send the signal yourself or call the package-level helper, which sends `SIGUSR1` to the own process:

```go
if err := app.Restart(); err != nil { ... }
```

If you prefer calling `BaseApp.Restart(ctx)` directly from code that is itself managed by the app (an HTTP handler, a worker), detach it so the graceful shutdown does not wait for the caller:

```go
go func() { _ = a.Restart(context.WithoutCancel(ctx)) }()
```

`Restart` drops the caller's cancellation on purpose: a dying request must not cut the shutdown short.

## Hooks

`OnBoot`, `OnStart` and `OnStop` return `*hook.Hook[*Event]` values. Handlers form a middleware chain: each handler receives the event and must call `event.Next()` to continue. Code before `Next()` runs before the following handlers (and before the built-in step at the end of the chain); code after `Next()` runs once they have completed.

```go
a.OnStart().BindFunc(func(e *app.StartEvent) error {
	log.Println("starting...")     // before fx OnStart hooks
	if err := e.Next(); err != nil {
		return err
	}
	log.Println("started")         // after fx OnStart hooks
	return nil
})
```

Returning an error without calling `Next()` short-circuits the chain; the error propagates to `Boot`/`Start`/`Stop`/`Restart`.

Handlers run in registration order. Use `Bind` with a `hook.Handler` to set an explicit `ID` (to replace or later `Unbind` a handler) or a `Priority` (lower runs first).

### Events

| Event        | Fields                                       | Notes                                                                                                          |
|--------------|----------------------------------------------|----------------------------------------------------------------------------------------------------------------|
| `BootEvent`  | `App`, `Ctx`, `Logger`, `Options []fx.Option` | Append to `Options` to register fx providers/invokes; set `Logger` to an `fxevent.Logger`.                    |
| `StartEvent` | `App`, `Ctx`                                 | `Ctx` carries the `StartTimeout` deadline.                                                                     |
| `StopEvent`  | `App`, `Ctx`, `IsRestart`                    | `Ctx` carries the `StopTimeout` deadline. `IsRestart` is `true` when the shutdown was triggered by `Restart`.  |

### Helpers

- `app.Options(opts ...fx.Option)` returns an `OnBoot` handler that appends the given fx options.
- `app.LoadConfig[C]()` returns an `OnBoot` handler that loads and supplies a config struct (see above).

## The `App` interface

```go
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
```

`*BaseApp` implements it and is supplied to the fx container as `App`, so you can depend on it from any constructor:

```go
fx.Provide(func(a app.App) *Health {
	return &Health{name: a.Name(), version: a.Version()}
})
```

## Testing

```bash
go test ./...
```

Restart tests short-circuit the `OnStop` chain before the built-in exec step; otherwise the test binary would be replaced.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
