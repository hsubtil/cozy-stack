package stack

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cozy/checkup"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/config_dyn"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/logger"
	"github.com/cozy/cozy-stack/pkg/sessions"
	"github.com/cozy/cozy-stack/pkg/statik/fs"
	"github.com/cozy/cozy-stack/pkg/utils"

	"github.com/google/gops/agent"
	"github.com/sirupsen/logrus"
)

var log = logger.WithNamespace("stack")

type gopAgent struct{}

func (g gopAgent) Shutdown(ctx context.Context) error {
	fmt.Print("  shutting down gops...")
	agent.Close()
	fmt.Println("ok.")
	return nil
}

// Start is used to initialize all the
func Start() (processes utils.Shutdowner, err error) {
	if config.IsDevRelease() {
		fmt.Print(`                           !! DEVELOPMENT RELEASE !!
You are running a development release which may deactivate some very important
security features. Please do not use this binary as your production server.

`)
	}

	err = agent.Listen(agent.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error on gops agent: %s\n", err)
	}

	if err = config.MakeVault(config.GetConfig()); err != nil {
		return
	}

	// Check that we can properly reach CouchDB.
	u := config.CouchURL()
	u.User = config.GetConfig().CouchDB.Auth
	attempts := 8
	attemptsSpacing := 1 * time.Second
	for i := 0; i < attempts; i++ {
		var db checkup.Result
		db, err = checkup.HTTPChecker{
			URL:         u.String(),
			MustContain: `"version":"2`,
		}.Check()
		if err != nil {
			err = fmt.Errorf("Could not reach Couchdb 2.0 database: %s", err.Error())
		} else if db.Status() == checkup.Down {
			err = fmt.Errorf("Could not reach Couchdb 2.0 database")
		} else if db.Status() != checkup.Healthy {
			log.Warnf("CouchDB does not seem to be in a healthy state, " +
				"the cozy-stack will be starting anyway")
			break
		}
		if err == nil {
			break
		}
		if i < attempts-1 {
			logrus.Warnf("%s, retrying in %v", err, attemptsSpacing)
			time.Sleep(attemptsSpacing)
		}
	}
	if err != nil {
		return
	}
	if err = consts.InitGlobalDB(); err != nil {
		return
	}

	// Init the main global connection to the swift server
	fsURL := config.FsURL()
	if fsURL.Scheme == config.SchemeSwift {
		if err = config.InitSwiftConnection(fsURL); err != nil {
			return
		}
	}

	workersList, err := jobs.GetWorkersList()
	if err != nil {
		return
	}

	var broker jobs.Broker
	var schder jobs.Scheduler
	jobsConfig := config.GetConfig().Jobs
	if cli := jobsConfig.Client(); cli != nil {
		broker = jobs.NewRedisBroker(cli)
		schder = jobs.NewRedisScheduler(cli)
	} else {
		broker = jobs.NewMemBroker()
		schder = jobs.NewMemScheduler()
	}

	if err = jobs.SystemStart(broker, schder, workersList); err != nil {
		return
	}

	assetsList, err := config_dyn.GetAssetsList()
	if err != nil {
		return
	}
	cacheStorage := config.GetConfig().CacheStorage
	if err = fs.RegisterCustomExternals(cacheStorage, assetsList, 6 /*= retry count */); err != nil {
		return
	}
	assetsPollingDisabled := config.GetConfig().AssetsPollingDisabled
	if !assetsPollingDisabled {
		pollingInterval := config.GetConfig().AssetsPollingInterval
		go config_dyn.PollAssetsList(cacheStorage, pollingInterval)
	}

	sessionSweeper := sessions.SweepLoginRegistrations()

	// Global shutdowner that composes all the running processes of the stack
	processes = utils.NewGroupShutdown(
		jobs.System(),
		sessionSweeper,
		gopAgent{},
	)
	return
}
