package signal

import (
	"os"
	gosignal "os/signal"
	"runtime"
	"sync/atomic"
	"syscall"

	"github.com/2qif49lt/logrus"
)

// Trap sets up a simplified signal "trap", appropriate for common
// behavior expected from a vanilla unix command-line tool in general
// (and the Docker engine in particular).
//
// * If SIGINT or SIGTERM are received, `cleanup` is called, then the process is terminated.
// * If SIGINT or SIGTERM are received 3 times before cleanup is complete, then cleanup is
//   skipped and the process is terminated immediately (allows force quit of stuck daemon)
// * A SIGQUIT always causes an exit without cleanup, with a goroutine dump preceding exit.
// * Ignore SIGPIPE events. These are generated by systemd when journald is restarted while
//   the docker daemon is not restarted and also running under systemd.
//   Fixes https://github.com/docker/docker/issues/19728
//
func Trap(cleanup func()) {
	c := make(chan os.Signal, 1)
	// we will handle INT, TERM, QUIT, SIGPIPE here
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGPIPE}
	gosignal.Notify(c, signals...)
	go func() {
		interruptCount := uint32(0)
		for sig := range c {
			if sig == syscall.SIGPIPE {
				continue
			}

			go func(sig os.Signal) {
				logrus.Infof("Processing signal '%v'", sig)
				switch sig {
				case os.Interrupt, syscall.SIGTERM:
					if atomic.LoadUint32(&interruptCount) < 3 {
						// Initiate the cleanup only once
						if atomic.AddUint32(&interruptCount, 1) == 1 {
							// Call the provided cleanup handler
							cleanup()
							os.Exit(0)
						} else {
							return
						}
					} else {
						// 3 SIGTERM/INT signals received; force exit without cleanup
						logrus.Info("Forcing docker daemon shutdown without cleanup; 3 interrupts received")
					}
				case syscall.SIGQUIT:
					logrus.Info("Forcing docker daemon shutdown without cleanup on SIGQUIT")
				}
				//for the SIGINT/TERM, and SIGQUIT non-clean shutdown case, exit with 128 + signal #
				os.Exit(128 + int(sig.(syscall.Signal)))
			}(sig)
		}
	}()
}

// DumpStacks dumps the runtime stack.
func DumpStacks() {
	var (
		buf       []byte
		stackSize int
	)
	bufferLen := 16384
	for stackSize == len(buf) {
		buf = make([]byte, bufferLen)
		stackSize = runtime.Stack(buf, true)
		bufferLen *= 2
	}
	buf = buf[:stackSize]
	// Note that if the daemon is started with a less-verbose log-level than "info" (the default), the goroutine
	// traces won't show up in the log.
	logrus.Warnf("=== BEGIN goroutine stack dump ===\n%s\n=== END goroutine stack dump ===", buf)
}
