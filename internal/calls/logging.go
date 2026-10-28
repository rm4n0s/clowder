package calls

import (
	"fmt"
	"log/slog"

	"github.com/pion/logging"
)

// slogFactory gives pion's loggers to slog: what pion reports as an error
// goes to the debug level of the logger of the server (it names addresses and
// ports, which are not for the info level), and the rest is dropped.
type slogFactory struct {
	logger *slog.Logger
}

// NewLogger makes the logger of one pion scope.
func (f slogFactory) NewLogger(scope string) logging.LeveledLogger {
	return slogLeveled{logger: f.logger.With("pion", scope)}
}

// slogLeveled is a pion logger that writes errors and warnings to slog at the
// debug level.
type slogLeveled struct {
	logger *slog.Logger
}

func (l slogLeveled) Trace(string)          {}
func (l slogLeveled) Tracef(string, ...any) {}
func (l slogLeveled) Debug(string)          {}
func (l slogLeveled) Debugf(string, ...any) {}
func (l slogLeveled) Info(string)           {}
func (l slogLeveled) Infof(string, ...any)  {}
func (l slogLeveled) Warn(msg string)       { l.logger.Debug("pion warning", "msg", msg) }
func (l slogLeveled) Warnf(format string, args ...any) {
	l.logger.Debug("pion warning", "msg", fmt.Sprintf(format, args...))
}
func (l slogLeveled) Error(msg string) { l.logger.Debug("pion error", "msg", msg) }
func (l slogLeveled) Errorf(format string, args ...any) {
	l.logger.Debug("pion error", "msg", fmt.Sprintf(format, args...))
}
