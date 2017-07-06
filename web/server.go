// Package web Cozy Stack API.
//
// Cozy is a personal platform as a service with a focus on data.
package web

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"path"
	"time"

	"github.com/cozy/cozy-stack/pkg/apps"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/pkg/utils"
	webapps "github.com/cozy/cozy-stack/web/apps"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/echo"
	"github.com/cozy/echo/middleware"
	"github.com/google/gops/agent"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/rakyll/statik/fs"
	"github.com/spf13/afero"
)

type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

var supportedLocales = []string{"en", "fr"}

// LoadSupportedLocales reads the po files packed in go or from the assets directory
// and loads them for translations
func LoadSupportedLocales() error {
	// By default, use the po files packed in the binary
	// but use assets from the disk is assets option is filled in config
	assetsPath := config.GetConfig().Assets
	if assetsPath != "" {
		for _, locale := range supportedLocales {
			pofile := path.Join(assetsPath, "locales", locale+".po")
			po, err := ioutil.ReadFile(pofile)
			if err != nil {
				return fmt.Errorf("Can't load the po file for %s", locale)
			}
			instance.LoadLocale(locale, string(po))
		}
		return nil
	}

	statikFS, err := fs.New()
	if err != nil {
		return err
	}
	for _, locale := range supportedLocales {
		f, err := statikFS.Open("/locales/" + locale + ".po")
		if err != nil {
			return fmt.Errorf("Can't load the po file for %s", locale)
		}
		po, err := ioutil.ReadAll(f)
		if err != nil {
			return err
		}
		instance.LoadLocale(locale, string(po))
	}
	return nil
}

// ListenAndServe creates and setups all the necessary http endpoints and start
// them.
func ListenAndServe() (Shutdowner, error) {
	return listenAndServe(webapps.Serve)
}

// ListenAndServeWithAppDir creates and setup all the necessary http endpoints
// and serve the specified application on a app subdomain.
//
// In order to serve the application, the specified directory should provide
// a manifest.webapp file that will be used to parameterize the application
// permissions.
func ListenAndServeWithAppDir(appsdir map[string]string) (Shutdowner, error) {
	for slug, dir := range appsdir {
		dir = utils.AbsPath(dir)
		appsdir[slug] = dir
		exists, err := utils.DirExists(dir)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("Directory %s does not exist", dir)
		}
		if err = checkExists(path.Join(dir, apps.WebappManifestName)); err != nil {
			return err
		}
		if err = checkExists(path.Join(dir, "index.html")); err != nil {
			return err
		}
	}
	return listenAndServe(func(c echo.Context) error {
		slug := c.Get("slug").(string)
		dir, ok := appsdir[slug]
		if !ok {
			return webapps.Serve(c)
		}
		method := c.Request().Method
		if method != "GET" && method != "HEAD" {
			return echo.NewHTTPError(http.StatusMethodNotAllowed, "Method not allowed")
		}
		fs := afero.NewBasePathFs(afero.NewOsFs(), dir)
		manFile, err := fs.Open(apps.WebappManifestName)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("Could not find the %s file in your application directory %s",
					apps.WebappManifestName, dir)
			}
			return err
		}
		app := &apps.WebappManifest{}
		if err = app.ReadManifest(manFile, slug, "file://localhost"+dir); err != nil {
			return fmt.Errorf("Could not parse the %s file: %s",
				apps.WebappManifestName, err.Error())
		}
		i := middlewares.GetInstance(c)
		f := apps.NewAferoFileServer(fs, func(_, _, file string) string {
			return path.Join("/", file)
		})
		// Save permissions in couchdb before loading an index page
		if _, file := app.FindRoute(path.Clean(c.Request().URL.Path)); file == "" {
			if app.Permissions() != nil {
				if err := permissions.ForceWebapp(i, app.Slug(), app.Permissions()); err != nil {
					return err
				}
			}
		}
		return webapps.ServeAppFile(c, i, f, app)
	})
}

func checkExists(filepath string) error {
	exists, err := utils.FileExists(filepath)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Directory %s should contain a %s file",
			path.Dir(filepath), path.Base(filepath))
	}
	return nil
}

func listenAndServe(appsHandler echo.HandlerFunc) (Shutdowner, error) {
	major, err := CreateSubdomainProxy(echo.New(), appsHandler)
	if err != nil {
		return err
	}
	if err = LoadSupportedLocales(); err != nil {
		return err
	}

	if config.IsDevRelease() {
		major.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: "time=${time_rfc3339}\tstatus=${status}\tmethod=${method}\thost=${host}\turi=${uri}\tbytes_out=${bytes_out}\n",
		}))
	}

	errs := make(chan error)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)

	if err = agent.Listen(nil); err != nil {
		return err
	}

	admin := echo.New()
	if err = SetupAdminRoutes(admin); err != nil {
		return err
	}

	go func() { errs <- admin.Start(config.AdminServerAddr()) }()
	go func() { errs <- major.Start(config.ServerAddr()) }()

	select {
	case err := <-errs:
		return err
	case sig := <-sigs:
		ctx := context.WithTimeout(context.Background(), 60*time.Second)
	}
}

type groupShutdown struct {
	s []Shutdowner
}

func newGroupShutdown(s ...Shutdowner) groupShutdown {
	return groupShutdown{s}
}

func (g *groupShutdown) Shutdown(ctx context.Context) error {
	errs := make(chan error)
	count := len(g.s)
	for _, s := range g.s {
		go func() { errs <- s.Shutdown(ctx) }()
	}
	var errm error
	for err := range errs {
		if err != nil {
			errm = multierror.Append(errm, err)
		}
		count--
		if count == 0 {
			break
		}
	}
	return errm
}
