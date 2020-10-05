package move

import (
	"encoding/base64"
	"net/http"
	"net/url"

	"github.com/cozy/cozy-stack/model/job"
	"github.com/cozy/cozy-stack/model/permission"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/jsonapi"
	"github.com/cozy/cozy-stack/pkg/limits"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/worker/move"
	"github.com/labstack/echo/v4"
)

func createExport(c echo.Context) error {
	inst := middlewares.GetInstance(c)
	err := limits.CheckRateLimit(inst, limits.ExportType)
	if limits.IsLimitReachedOrExceeded(err) {
		return echo.NewHTTPError(http.StatusNotFound, "Not found")
	}

	if err := middlewares.AllowWholeType(c, permission.POST, consts.Exports); err != nil {
		return err
	}

	var exportOptions move.ExportOptions
	if _, err := jsonapi.Bind(c.Request().Body, &exportOptions); err != nil {
		return err
	}

	// The contextual domain is used to send a link on the correct domain when
	// the user is accessing their cozy from a backup URL.
	exportOptions.ContextualDomain = inst.ContextualDomain()

	msg, err := job.NewMessage(exportOptions)
	if err != nil {
		return err
	}

	_, err = job.System().PushJob(inst, &job.JobRequest{
		WorkerType: "export",
		Message:    msg,
	})
	if err != nil {
		return err
	}

	return c.NoContent(http.StatusCreated)
}

func exportHandler(c echo.Context) error {
	inst := middlewares.GetInstance(c)

	if err := middlewares.AllowWholeType(c, permission.GET, consts.Exports); err != nil {
		return err
	}

	exportMAC, err := base64.URLEncoding.DecodeString(c.Param("export-mac"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	exportDoc, err := move.GetExport(inst, exportMAC)
	if err != nil {
		return err
	}

	return jsonapi.Data(c, http.StatusOK, exportDoc, nil)
}

func exportDataHandler(c echo.Context) error {
	inst := middlewares.GetInstance(c)
	if !middlewares.IsLoggedIn(c) {
		u := inst.PageURL("/auth/login", url.Values{
			"redirect": {inst.FromURL(c.Request().URL)},
		})
		return c.Redirect(http.StatusSeeOther, u)
	}

	from := inst.SubDomain(consts.SettingsSlug).String()
	middlewares.AppendCSPRule(c, "frame-ancestors", from)

	exportMAC, err := base64.URLEncoding.DecodeString(c.Param("export-mac"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	return move.ExportCopyData(c.Response(), inst, move.SystemArchiver(), exportMAC,
		c.QueryParam("cursor"))
}

// Routes defines the routing layout for the /move module.
func Routes(g *echo.Group) {
	g.POST("/exports", createExport)
	g.GET("/exports/:export-mac", exportHandler)
	g.GET("/exports/data/:export-mac", exportDataHandler)
}
