package updates

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cozy/cozy-stack/pkg/apps"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/registry"
	"github.com/sirupsen/logrus"
)

const numUpdaters = 4
const numUpdatersSingleInstance = 4

func init() {
	jobs.AddWorker(&jobs.WorkerConfig{
		WorkerType:   "updates",
		Concurrency:  1,
		MaxExecCount: 1,
		Timeout:      1 * time.Hour,
		WorkerFunc:   Worker,
	})
}

type updateError struct {
	domain string
	slug   string
	step   string
	reason error
}

func (u *updateError) toFields() logrus.Fields {
	fields := make(logrus.Fields, 4)
	fields["step"] = u.step
	fields["reason"] = u.reason.Error()
	if u.domain != "" {
		fields["domain"] = u.domain
	}
	if u.slug != "" {
		fields["slug"] = u.slug
	}
	return fields
}

func updateErrorFromInstaller(inst *apps.Installer, step string, reason error) *updateError {
	return &updateError{
		domain: inst.Domain(),
		slug:   inst.Slug(),
		step:   step,
		reason: reason,
	}
}

// Options is the option handler for updates:
//   - Slugs: allow to filter the application's slugs to update, if empty, all
//     applications are updated
//   - Force: forces the update, even if the user has not activated the auto-
//     update
//   - ForceRegistry: translates the git:// sourced application into
//     registry://
type Options struct {
	Slugs              []string `json:"slugs,omitempty"`
	Domain             string   `json:"domain,omitempty"`
	DomainsWithContext string   `json:"domains_with_context,omitempty"`
	AllDomains         bool     `json:"all_domains"`
	Force              bool     `json:"force"`
	ForceRegistry      bool     `json:"force_registry"`
	OnlyRegistry       bool     `json:"only_registry"`
}

// Worker is the worker method to launch the updates.
func Worker(ctx *jobs.WorkerContext) error {
	var opts Options
	if err := ctx.UnmarshalMessage(&opts); err != nil {
		return err
	}
	if opts.AllDomains {
		return UpdateAll(ctx, &opts)
	}
	if opts.Domain != "" {
		inst, err := instance.Get(opts.Domain)
		if err != nil {
			return err
		}
		return UpdateInstance(ctx, inst, &opts)
	}
	return nil
}

// UpdateAll starts the auto-updates process for all instances. The slugs
// parameters can be used optionnaly to filter (whitelist) the applications'
// slug to update.
func UpdateAll(ctx *jobs.WorkerContext, opts *Options) error {
	insc := make(chan *apps.Installer)
	errc := make(chan *updateError)

	var g sync.WaitGroup
	g.Add(numUpdaters)

	for i := 0; i < numUpdaters; i++ {
		go func() {
			for installer := range insc {
				_, err := installer.RunSync()
				if err != nil {
					errc <- updateErrorFromInstaller(installer, "RunSync", err)
				} else {
					errc <- nil
				}
			}
			g.Done()
		}()
	}

	go func() {
		// TODO: filter instances that are AutoUpdate only
		errf := instance.ForeachInstances(func(inst *instance.Instance) error {
			if opts.DomainsWithContext != "" &&
				inst.ContextName != opts.DomainsWithContext {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if opts.Force || !inst.NoAutoUpdate {
				installerPush(inst, insc, errc, opts)
			}
			return nil
		})
		if errf != nil {
			errc <- &updateError{step: "ForeachInstances", reason: errf}
		}
		close(insc)
		g.Wait()
		close(errc)
	}()

	errors := 0
	totals := 0
	for err := range errc {
		if err != nil {
			ctx.Logger().WithFields(err.toFields()).Error()
			errors++
		}
		totals++
	}

	if errors > 0 {
		return fmt.Errorf("At least one error has happened during the updates: "+
			"%d errors for %d updates", errors, totals)
	}
	return nil
}

// UpdateInstance starts the auto-update process on the given instance. The
// slugs parameters can be used to filter (whitelist) the applications' slug
func UpdateInstance(ctx *jobs.WorkerContext, inst *instance.Instance, opts *Options) error {
	insc := make(chan *apps.Installer)
	errc := make(chan *updateError)

	if opts.DomainsWithContext != "" &&
		inst.ContextName != opts.DomainsWithContext {
		return nil
	}

	var g sync.WaitGroup
	g.Add(numUpdatersSingleInstance)

	for i := 0; i < numUpdatersSingleInstance; i++ {
		go func() {
			for installer := range insc {
				_, err := installer.RunSync()
				if err != nil {
					errc <- updateErrorFromInstaller(installer, "RunSync", err)
				} else {
					errc <- nil
				}
			}
			g.Done()
		}()
	}

	go func() {
		installerPush(inst, insc, errc, opts)
		close(insc)
		g.Wait()
		close(errc)
	}()

	errors := 0
	totals := 0
	for err := range errc {
		if err != nil {
			ctx.Logger().WithFields(err.toFields()).Error()
			errors++
		}
		totals++
	}

	if errors > 0 {
		return fmt.Errorf("At least one error has happened during the updates: "+
			"%d errors for %d updates", errors, totals)
	}
	return nil
}

func installerPush(inst *instance.Instance, insc chan *apps.Installer, errc chan *updateError, opts *Options) {
	registries, err := inst.Registries()
	if err != nil {
		errc <- &updateError{step: "Registries", reason: err}
		return
	}

	var g sync.WaitGroup
	g.Add(2)

	go func() {
		defer g.Done()
		webapps, err := apps.ListWebapps(inst)
		if err != nil {
			errc <- &updateError{
				domain: inst.Domain,
				step:   "ListWebapps",
				reason: err,
			}
			return
		}
		for _, app := range webapps {
			if filterSlug(app.Slug(), opts.Slugs) {
				continue
			}
			if opts.OnlyRegistry && strings.HasPrefix(app.Source(), "registry://") {
				continue
			}
			installer, err := createInstaller(inst, registries, app, opts)
			if err != nil {
				errc <- &updateError{
					domain: inst.Domain,
					slug:   app.Slug(),
					step:   "CreateInstaller",
					reason: err,
				}
			} else {
				insc <- installer
			}
		}
	}()

	go func() {
		defer g.Done()
		konnectors, err := apps.ListKonnectors(inst)
		if err != nil {
			errc <- &updateError{
				domain: inst.Domain,
				step:   "ListKonnectors",
				reason: err,
			}
			return
		}
		for _, app := range konnectors {
			if filterSlug(app.Slug(), opts.Slugs) {
				continue
			}
			if opts.OnlyRegistry && strings.HasPrefix(app.Source(), "registry://") {
				continue
			}
			installer, err := createInstaller(inst, registries, app, opts)
			if err != nil {
				errc <- &updateError{
					domain: inst.Domain,
					slug:   app.Slug(),
					step:   "CreateInstaller",
					reason: err,
				}
			} else {
				insc <- installer
			}
		}
	}()

	g.Wait()
}

func filterSlug(slug string, slugs []string) bool {
	if len(slugs) == 0 {
		return false
	}
	for _, s := range slugs {
		if s == slug {
			return false
		}
	}
	return true
}

func createInstaller(inst *instance.Instance, registries []*url.URL, man apps.Manifest, opts *Options) (*apps.Installer, error) {
	var sourceURL string
	if opts.ForceRegistry {
		originalSourceURL, err := url.Parse(man.Source())
		if err == nil && (originalSourceURL.Scheme == "git" ||
			originalSourceURL.Scheme == "git+ssh" ||
			originalSourceURL.Scheme == "ssh+git") {
			var channel string
			if man.AppType() == apps.Webapp && strings.HasPrefix(originalSourceURL.Fragment, "build") {
				channel = "dev"
			} else {
				channel = "stable"
			}
			_, err := registry.GetLatestVersion(man.Slug(), channel, registries)
			if err == nil {
				sourceURL = fmt.Sprintf("registry://%s/%s", man.Slug(), channel)
			}
		}
	}
	return apps.NewInstaller(inst, inst.AppsCopier(man.AppType()),
		&apps.InstallerOptions{
			Operation:  apps.Update,
			Manifest:   man,
			Registries: registries,
			SourceURL:  sourceURL,
		},
	)
}
