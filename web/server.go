// Package web Cozy Stack API.
//
// Cozy is a personal platform as a service with a focus on data.
package web

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"

	"github.com/cozy/cozy-stack/pkg/apps"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/permissions"
	"github.com/cozy/cozy-stack/pkg/utils"
	webapps "github.com/cozy/cozy-stack/web/apps"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/labstack/echo"
	"github.com/labstack/echo/middleware"
	"github.com/rakyll/statik/fs"
	"github.com/spf13/afero"
)

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
func ListenAndServe(noAdmin bool) error {
	return listenAndServe(noAdmin, webapps.Serve)
}

// ListenAndServeWithAppDir creates and setup all the necessary http endpoints
// and serve the specified application on a app subdomain.
//
// In order to serve the application, the specified directory should provide
// a manifest.webapp file that will be used to parameterize the application
// permissions.
func ListenAndServeWithAppDir(appsdir map[string]string) error {
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
		if err = checkExists(path.Join(dir, apps.WebappManifest)); err != nil {
			return err
		}
		if err = checkExists(path.Join(dir, "index.html")); err != nil {
			return err
		}
	}
	return listenAndServe(false, func(c echo.Context) error {
		slug := c.Get("slug").(string)
		dir, ok := appsdir[slug]
		if !ok {
			return webapps.Serve(c)
		}
		method := c.Request().Method
		if method != "GET" && method != "HEAD" {
			return echo.NewHTTPError(http.StatusMethodNotAllowed, "Method %s not allowed", method)
		}
		fs := afero.NewBasePathFs(afero.NewOsFs(), dir)
		manFile, err := fs.Open(apps.WebappManifest)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("Could not find the %s file in your application directory %s",
					apps.WebappManifest, dir)
			}
			return err
		}
		app := &apps.Manifest{}
		if err = json.NewDecoder(manFile).Decode(&app); err != nil {
			return fmt.Errorf("Could not parse the %s file: %s",
				apps.WebappManifest, err.Error())
		}
		app.CreateDefaultRoute()
		app.Slug = slug
		i := middlewares.GetInstance(c)
		f := webapps.NewAferoServer(fs, func(_, folder, file string) string {
			return path.Join(folder, file)
		})
		// Save permissions in couchdb before loading an index page
		if _, file := app.FindRoute(path.Clean(c.Request().URL.Path)); file == "" {
			if app.Permissions != nil {
				if err := permissions.Force(i, app.Slug, *app.Permissions); err != nil {
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

func listenAndServe(noAdmin bool, appsHandler echo.HandlerFunc) error {
	main, err := CreateSubdomainProxy(echo.New(), appsHandler)
	if err != nil {
		return err
	}
	if err = LoadSupportedLocales(); err != nil {
		return err
	}

	if config.IsDevRelease() {
		fmt.Println(`                           !! DEVELOPMENT RELEASE !!
You are running a development release which may deactivate some very important
security features. Please do not use this binary as your production server.
`)
		main.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: "time=${time_rfc3339}\tstatus=${status}\tmethod=${method}\thost=${host}\turi=${uri}\tbytes_out=${bytes_out}\n",
		}))
	}

	errs := make(chan error)

	if !noAdmin {
		admin := echo.New()
		if err = SetupAdminRoutes(admin); err != nil {
			return err
		}
		go func() { errs <- admin.Start(config.AdminServerAddr()) }()
	}

	go func() { errs <- main.Start(config.ServerAddr()) }()
	return <-errs
}
