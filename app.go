// Copyright (c) 2023 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package bedrock provides a minimal foundation for building more complex frameworks on top of.
package bedrock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/z5labs/bedrock/pkg/config"
	"github.com/z5labs/bedrock/pkg/otelconfig"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
)

// Runtime
type Runtime interface {
	Run(context.Context) error
}

type FinalizerFunc func() error

type finalizer struct {
	Finalizers []FinalizerFunc
}

// BuildContext
type BuildContext struct {
	Config    config.Manager
	finalizer *finalizer
}

func (b BuildContext) RegisterFinalizers(f ...FinalizerFunc) {
	b.finalizer.Finalizers = append(b.finalizer.Finalizers, f...)
}

// RuntimeBuilder
type RuntimeBuilder interface {
	Build(BuildContext) (Runtime, error)
}

// RuntimeBuilderFunc
type RuntimeBuilderFunc func(BuildContext) (Runtime, error)

// Build implements the RuntimeBuilder interface.
func (f RuntimeBuilderFunc) Build(ctx BuildContext) (Runtime, error) {
	return f(ctx)
}

// Option
type Option func(*App)

// Name
func Name(name string) Option {
	return func(a *App) {
		a.name = name
	}
}

// WithRuntimeBuilder
func WithRuntimeBuilder(rb RuntimeBuilder) Option {
	return func(a *App) {
		a.rbs = append(a.rbs, rb)
	}
}

// WithRuntimeBuilderFunc
func WithRuntimeBuilderFunc(f func(BuildContext) (Runtime, error)) Option {
	return func(a *App) {
		a.rbs = append(a.rbs, RuntimeBuilderFunc(f))
	}
}

// Config
func Config(r io.Reader) Option {
	return func(a *App) {
		a.cfgSrc = r
	}
}

// InitTracerProvider
func InitTracerProvider(f func(BuildContext) (otelconfig.Initializer, error)) Option {
	return func(a *App) {
		a.otelIniterFunc = f
	}
}

// App
type App struct {
	name           string
	cfgSrc         io.Reader
	otelIniterFunc func(BuildContext) (otelconfig.Initializer, error)
	rbs            []RuntimeBuilder
}

// New
func New(opts ...Option) *App {
	var name string
	if len(os.Args) > 0 {
		name = os.Args[0]
	}
	app := &App{
		name: name,
		otelIniterFunc: func(_ BuildContext) (otelconfig.Initializer, error) {
			return otelconfig.Noop, nil
		},
	}
	for _, opt := range opts {
		opt(app)
	}
	return app
}

// Run
func (app *App) Run(args ...string) error {
	cmd := buildCmd(app)
	cmd.SetArgs(args)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	return cmd.ExecuteContext(ctx)
}

var errNilRuntime = errors.New("nil runtime")

func buildCmd(app *App) *cobra.Command {
	rs := make([]Runtime, len(app.rbs))
	bc := BuildContext{finalizer: &finalizer{Finalizers: []FinalizerFunc{finalizeOtel}}}
	return &cobra.Command{
		PreRunE: func(cmd *cobra.Command, args []string) (err error) {
			defer errRecover(&err)
			if app.cfgSrc != nil {
				b, err := readAllAndTryClose(app.cfgSrc)
				if err != nil {
					return err
				}

				m, err := config.Read(bytes.NewReader(b), config.Language(config.YAML))
				if err != nil {
					return err
				}

				bc.Config = m
			}

			otelIniter, err := app.otelIniterFunc(bc)
			if err != nil {
				return err
			}
			tp, err := otelIniter.Init()
			if err != nil {
				return err
			}
			otel.SetTracerProvider(tp)

			for i, rb := range app.rbs {
				r, err := rb.Build(bc)
				if err != nil {
					return err
				}
				if r == nil {
					return errNilRuntime
				}
				rs[i] = r
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			defer errRecover(&err)

			if len(rs) == 0 {
				return
			}
			if len(rs) == 1 {
				return rs[0].Run(cmd.Context())
			}

			g, gctx := errgroup.WithContext(cmd.Context())
			for _, rt := range rs {
				rt := rt
				g.Go(func() (e error) {
					defer errRecover(&e)
					return rt.Run(gctx)
				})
			}
			return g.Wait()
		},
		PostRunE: func(cmd *cobra.Command, args []string) error {
			// will always have at least one finalizer for otel
			var me multiError
			for _, f := range bc.finalizer.Finalizers {
				err := f()
				if err != nil {
					me.errors = append(me.errors, err)
				}
			}

			if len(me.errors) == 0 {
				return nil
			}
			return me
		},
	}
}

type multiError struct {
	errors []error
}

func (m multiError) Error() string {
	if len(m.errors) == 0 {
		return ""
	}

	e := ""
	for _, err := range m.errors {
		e += err.Error() + ";"
	}

	return strings.TrimSuffix(e, ";")
}

func finalizeOtel() error {
	tp := otel.GetTracerProvider()
	stp, ok := tp.(interface {
		Shutdown(context.Context) error
	})
	if !ok {
		return nil
	}
	return stp.Shutdown(context.Background())
}

func readAllAndTryClose(r io.Reader) ([]byte, error) {
	defer func() {
		rc, ok := r.(io.ReadCloser)
		if !ok {
			return
		}
		rc.Close()
	}()
	return io.ReadAll(r)
}

type panicError struct {
	v any
}

func (e panicError) Error() string {
	return fmt.Sprintf("bedrock: recovered from a panic caused by: %v", e.v)
}

func errRecover(err *error) {
	r := recover()
	if r == nil {
		return
	}
	rerr, ok := r.(error)
	if !ok {
		*err = panicError{v: r}
		return
	}
	*err = rerr
}
