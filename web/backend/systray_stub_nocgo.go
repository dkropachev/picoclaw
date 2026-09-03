//go:build (darwin || freebsd || android) && !cgo

package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// runTray falls back to a headless mode on platforms where systray requires cgo.
func runTray() {
	logger.Infof("System tray is unavailable in %s builds without cgo; running without tray", runtime.GOOS)

	if !*noBrowser {
		go func() {
			time.Sleep(browserDelay)
			if err := openBrowser(); err != nil {
				logger.Errorf("Warning: Failed to auto-open browser: %v", err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
	case serveErr := <-launcherServeErrors:
		if serveErr != nil {
			logger.ErrorC("web", serveErr.Error())
		}
	}
}
