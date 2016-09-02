package middleware

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/2qif49lt/agent/api/server/httputils"
	"github.com/2qif49lt/agent/errors"
	"github.com/2qif49lt/agent/pkg/eventdb"
	"github.com/2qif49lt/agent/pkg/random"
	"github.com/2qif49lt/logrus"
)

// EventDBMiddleware record  the request mission.
// THIS MIDDLEWARE SHOULD APPEND AT LAST
func EventDBMiddleware(handler func(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error) func(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
		logrus.Debugln("EventDBMiddleware enter")
		defer logrus.Debugln("EventDBMiddleware leave")

		mid, err := random.GetGuid()
		if err != nil {
			logrus.Warnln("GetGuid fail", err)
			mid = fmt.Sprintf("ffffffff%s%010d", time.Now().Format("20060102150405"), rand.Intn(1e10))
		}
		ctx = context.WithValue(ctx, "mid", mid)
		command := httputils.CommandFromRequest(r)
		paras := r.RequestURI + " " + fmt.Sprintf("%v", vars)

		begtime := time.Now()

		err = handler(ctx, w, r, vars)

		cost := time.Since(begtime) / time.Millisecond
		version := httputils.VersionFromContext(ctx)

		if eventerr := eventdb.InsertMission(mid, version, command, paras, errors.Str(err), int(cost)); eventerr != nil {
			logrus.WithFields(logrus.Fields{
				"mid":     mid,
				"version": version,
				"command": command,
				"paras":   paras,
				"result":  errors.Str(err),
				"cost":    int(cost),
			}).Warnln(err.Error())
		}
		return err
	}
}
