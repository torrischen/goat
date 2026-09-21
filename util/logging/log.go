package logging

import "go.uber.org/zap"

type logger struct {
	l *zap.SugaredLogger
}

var loggerInstance *logger

func init() {
	l, _ := zap.NewProduction()
	loggerInstance = &logger{
		l: l.Sugar(),
	}
}

func Debug(args ...any) {
	loggerInstance.l.Debug(args...)
}

func Debugf(template string, args ...any) {
	loggerInstance.l.Debugf(template, args...)
}

func Info(args ...any) {
	loggerInstance.l.Info(args...)
}

func Infof(template string, args ...any) {
	loggerInstance.l.Infof(template, args...)
}

func Warn(args ...any) {
	loggerInstance.l.Warn(args...)
}

func Warnf(template string, args ...any) {
	loggerInstance.l.Warnf(template, args...)
}

func Error(args ...any) {
	loggerInstance.l.Error(args...)
}

func Errorf(template string, args ...any) {
	loggerInstance.l.Errorf(template, args...)
}

func Fatal(args ...any) {
	loggerInstance.l.Fatal(args...)
}

func Fatalf(template string, args ...any) {
	loggerInstance.l.Fatalf(template, args...)
}
