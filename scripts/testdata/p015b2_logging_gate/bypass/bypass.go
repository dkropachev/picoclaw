//go:build p015_inventory_never

package bypass

import (
	foreign "example.invalid/foreign"
	f "fmt"
	i "io"
	stdlog "log"
	s "log/slog"
	o "os"
	dbg "runtime/debug"

	lr "github.com/go-logr/logr"
	z "github.com/rs/zerolog"
	pico "github.com/sipeed/picoclaw/pkg/logger"
)

var initializer = func() int {
	pico.Info("initializer")
	return 1
}()

type injectedSink interface {
	Info(...any)
	Error(...any)
	ErrorResult(...any)
	Trace(...any)
	Notice(...any)
	Crit(...any)
	Critical(...any)
	DPanic(...any)
	Alert(...any)
	Emergency(...any)
}

type picoFacadeAlias = pico.Logger

var (
	zeroPico         pico.Logger
	storedPico       = pico.NewLogger("stored")
	picoConstructor  = pico.NewLogger
	zeroStandard     stdlog.Logger
	zeroStructured   s.Logger
	storedStandard   = stdlog.Default()
	storedStructured = s.Default()
)

var injectedGlobalSink injectedSink
var injectedGlobalEmitter = injectedGlobalSink.Info
var foreignGlobalEmitter = foreign.Info

func facade(value string) {
	pico.Warn(value)
}

func bypasses(value string) {
	emit := pico.Error
	_ = emit
	safeEmit := pico.InfoSafeCF
	_ = safeEmit
	panicArtifact := pico.InitPanic
	_ = panicArtifact
	recoverEmit := pico.RecoverPanicNoExit
	_ = recoverEmit
	printer := f.Println
	_ = printer
	_, _ = f.Fprintf(o.Stdout, "%s", value)
	_, _ = i.WriteString(o.Stderr, value)
	stdlog.Print(value)
	s.Info(value)
	dbg.PrintStack()
	_ = z.New(o.Stdout)
	_ = lr.Discard
	foreign.Info(value)
	foreign.Trace(value)
	foreign.Notice(value)
	foreign.Crit(value)
	foreign.Critical(value)
	foreign.DPanic(value)
	foreign.Alert(value)
	foreign.Emergency(value)
	print(value)
}

func shadowed() {
	pico := struct{ Info func(string) }{Info: func(string) {}}
	pico.Info("shadowed")
}

func injectedFacade(sink injectedSink) {
	sink.Info("injected")
	sink.Error("injected")
	sink.Trace("injected")
	sink.Notice("injected")
	sink.Crit("injected")
	sink.Critical("injected")
	sink.DPanic("injected")
	sink.Alert("injected")
	sink.Emergency("injected")
	emit := sink.Info
	alias := emit
	alias("method-value-alias")
	errorEmit := sink.Error
	errorEmit("method-value")
	errorResult := sink.ErrorResult
	errorResult("unresolved-result")
}

func acceptEmitter(any) {}

func passEmitterValues(sink injectedSink) {
	acceptEmitter(sink.Warn)
	acceptEmitter(foreign.Warn)
}

func returnInjectedEmitter(sink injectedSink) any {
	return sink.Notice
}

func returnForeignEmitter() any {
	return foreign.Notice
}

func foreignMethodValues(value string) {
	emit := foreign.Info
	emit(value)
	errorEmit := foreign.Error
	errorEmit(value)
}

func picoFacadeBypasses(injected *pico.Logger) {
	pico.NewLogger("chained").Info("chained")
	storedPico.Warn("stored")
	zeroPico.Log(1, 1, "zero")
	injected.Debug("injected-type")
	_ = picoConstructor
}

func standardFacadeBypasses() {
	storedStandard.Print("stored-standard")
	stdlog.Default().Print("chained-standard")
	storedStructured.Info("stored-structured")
	s.Default().Warn("chained-structured")
	_ = zeroStandard
	_ = zeroStructured
}
