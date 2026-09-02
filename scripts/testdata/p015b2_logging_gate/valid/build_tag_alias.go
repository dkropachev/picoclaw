//go:build p015_inventory_never

package valid

import (
	"crypto/sha256"
	f "fmt"
	logx "github.com/sipeed/picoclaw/pkg/logger"
	toolpkg "github.com/sipeed/picoclaw/pkg/tools"
	i "io"
	"os"
	"strings"
)

var initialized = func() int {
	logx.Info("initializer")
	return 1
}()

func aliasAndLiteral() {
	logx.Warn("first")
	func() {
		logx.Error("literal")
	}()
	logx.Debug("second")
}

func functionalFormatting() {
	var builder strings.Builder
	_, _ = f.Fprintf(&builder, "%s", "value")
	digest := sha256.New()
	_, _ = f.Fprintf(digest, "%d:", 5)
	_, _ = i.WriteString(&builder, "value")
}

func literalWhitespace() {
	logx.Info("a  b")
	logx.Info("a b")
	logx.Info(`first line
	  second line`)
}

type ordinaryError interface {
	Error() string
}

func nonEmittingErrorMethod(err ordinaryError) string {
	return err.Error()
}

func reviewedNonLoggerFunctionValue() {
	result := toolpkg.ErrorResult
	_ = result("functional result, not a log")
}

func directoryEntryMetadata(entry os.DirEntry) {
	_, _ = entry.Info()
}
