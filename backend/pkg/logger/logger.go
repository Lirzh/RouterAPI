package logger

import (
	"os"
	"time"

	"github.com/sirupsen/logrus"
)

func Init(releaseMode bool) {
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: time.DateTime,
	})
	if releaseMode {
		logrus.SetLevel(logrus.InfoLevel)
		logrus.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: time.DateTime,
		})
	} else {
		logrus.SetLevel(logrus.DebugLevel)
	}
	logrus.SetOutput(os.Stdout)
}
