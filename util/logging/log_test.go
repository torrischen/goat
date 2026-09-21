package logging

import (
	"testing"

	"go.uber.org/zap"
)

func TestLoggingFunctions(t *testing.T) {
	previous := loggerInstance
	loggerInstance = &logger{l: zap.NewNop().Sugar()}
	t.Cleanup(func() { loggerInstance = previous })

	Debug("debug")
	Debugf("debug %d", 1)
	Info("info")
	Infof("info %d", 1)
	Warn("warn")
	Warnf("warn %d", 1)
	Error("error")
	Errorf("error %d", 1)
}
