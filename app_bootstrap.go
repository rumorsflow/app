package app

import "go.uber.org/fx"

func BootstrapOptions(options ...fx.Option) func(*BootstrapEvent) error {
	return func(event *BootstrapEvent) error {
		event.Options = append(event.Options, options...)
		return event.Next()
	}
}

func BootstrapConfig[C any]() func(*BootstrapEvent) error {
	return func(event *BootstrapEvent) error {
		var cfg C
		if err := event.App.LoadConfig(&cfg); err != nil {
			return err
		}

		event.Options = append(event.Options, fx.Supply(cfg))

		return event.Next()
	}
}
