package exec

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/cozy/cozy-stack/pkg/apps"
	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/logger"
	"github.com/cozy/cozy-stack/pkg/realtime"
	"github.com/cozy/cozy-stack/pkg/vfs"

	"github.com/spf13/afero"
)

type konnectorMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type konnectorWorker struct {
	slug     string
	msg      map[string]interface{}
	man      *apps.KonnManifest
	messages []konnectorMsg
}

const (
	konnectorMsgTypeDebug    = "debug"
	konnectorMsgTypeWarning  = "warning"
	konnectorMsgTypeError    = "error"
	konnectorMsgTypeCritical = "critical"
)

// const konnectorMsgTypeProgress string = "progress"

func (w *konnectorWorker) PrepareWorkDir(ctx *jobs.WorkerContext, i *instance.Instance) (workDir string, err error) {
	var msg map[string]interface{}
	if err = ctx.UnmarshalMessage(&msg); err != nil {
		return
	}

	slug, _ := msg["konnector"].(string)
	man, err := apps.GetKonnectorBySlug(i, slug)
	if err != nil {
		return
	}

	// TODO: disallow konnectors on state Installed to be run when we define our
	// workflow to accept permissions changes on konnectors.
	if s := man.State(); s != apps.Ready && s != apps.Installed {
		err = errors.New("Konnector is not ready")
		return
	}

	w.slug = slug
	w.msg = msg
	w.man = man

	osFS := afero.NewOsFs()
	workDir, err = afero.TempDir(osFS, "", "konnector-"+slug)
	if err != nil {
		return
	}
	workFS := afero.NewBasePathFs(osFS, workDir)

	fileServer := i.KonnectorsFileServer()
	tarFile, err := fileServer.Open(slug, man.Version(), apps.KonnectorArchiveName)
	if err != nil {
		return
	}

	// Create the folder in which the konnector has the right to write.
	{
		fs := i.VFS()
		folderToSave, _ := msg["folder_to_save"].(string)
		if folderToSave != "" {
			if _, err = fs.DirByID(folderToSave); os.IsNotExist(err) {
				folderToSave = ""
			}
		}
		if folderToSave == "" {
			defaultFolderPath, _ := msg["default_folder_path"].(string)
			if defaultFolderPath == "" {
				name := i.Translate("Tree Administrative")
				defaultFolderPath = fmt.Sprintf("/%s/%s", name, strings.Title(slug))
			}
			var dir *vfs.DirDoc
			dir, err = vfs.MkdirAll(fs, defaultFolderPath, nil)
			if err != nil {
				log := logger.WithDomain(i.Domain)
				log.Warnf("Can't create the default folder %s for konnector %s: %s", defaultFolderPath, slug, err)
				return
			}
			msg["folder_to_save"] = dir.ID()
		}
	}

	tr := tar.NewReader(tarFile)
	for {
		var hdr *tar.Header
		hdr, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return
		}
		dirname := path.Dir(hdr.Name)
		if dirname != "." {
			if err = workFS.MkdirAll(dirname, 0755); err != nil {
				return
			}
		}
		var f afero.File
		f, err = workFS.OpenFile(hdr.Name, os.O_CREATE|os.O_WRONLY, 0640)
		if err != nil {
			return
		}
		_, err = io.Copy(f, tr)
		errc := f.Close()
		if err != nil {
			return
		}
		if errc != nil {
			err = errc
			return
		}
	}

	return workDir, nil
}

func (w *konnectorWorker) PrepareCmdEnv(ctx *jobs.WorkerContext, i *instance.Instance) (cmd string, env []string, jobID string, err error) {
	jobID = fmt.Sprintf("konnector/%s/%s", w.slug, i.Domain)

	// Directly pass the job message as fields parameters
	fieldsJSON, err := json.Marshal(w.msg)
	if err != nil {
		return
	}

	paramsJSON, err := json.Marshal(w.man.Parameters)
	if err != nil {
		return
	}

	token := i.BuildKonnectorToken(w.man)

	cmd = config.GetConfig().Konnectors.Cmd
	env = []string{
		"COZY_URL=" + i.PageURL("/", nil),
		"COZY_CREDENTIALS=" + token,
		"COZY_FIELDS=" + string(fieldsJSON),
		"COZY_PARAMETERS=" + string(paramsJSON),
		"COZY_TYPE=" + w.man.Type,
		"COZY_LOCALE=" + i.Locale,
		"COZY_JOB_ID=" + jobID,
	}
	return
}

func (w *konnectorWorker) ScanOuput(ctx *jobs.WorkerContext, i *instance.Instance, line []byte) error {
	var msg konnectorMsg
	if err := json.Unmarshal(line, &msg); err != nil {
		return fmt.Errorf("Could not parse stdout as JSON: %q", string(line))
	}

	switch msg.Type {
	case konnectorMsgTypeDebug:
		ctx.Logger().Debug(msg.Message)
	case konnectorMsgTypeWarning:
		ctx.Logger().Warn(msg.Message)
	case konnectorMsgTypeError, konnectorMsgTypeCritical:
		ctx.Logger().Error(msg.Message)
	}

	w.messages = append(w.messages, msg)
	realtime.GetHub().Publish(&realtime.Event{
		Verb: realtime.EventCreate,
		Doc: couchdb.JSONDoc{Type: consts.JobEvents, M: map[string]interface{}{
			"type":    msg.Type,
			"message": msg.Message,
		}},
		Domain: i.Domain,
	})
	return nil
}

func (w *konnectorWorker) Error(i *instance.Instance, err error) error {
	// For retro-compatibility, we still use "error" logs as returned error, only
	// in the case that no "critical" message are actually returned. In such
	// case, We use the last "error" log as the returned error.
	var lastErrorMessage error
	for _, msg := range w.messages {
		if msg.Type == konnectorMsgTypeCritical {
			return errors.New(msg.Message)
		}
		if msg.Type == konnectorMsgTypeError {
			lastErrorMessage = errors.New(msg.Message)
		}
	}
	if lastErrorMessage != nil {
		return lastErrorMessage
	}

	return err
}

func (w *konnectorWorker) Commit(ctx *jobs.WorkerContext, errjob error) error {
	return nil
	// triggerID, ok := ctx.TriggerID()
	// if !ok {
	// 	return nil
	// }

	// sched := globals.GetScheduler()
	// t, err := sched.Get(ctx.Domain(), triggerID)
	// if err != nil {
	// 	return err
	// }

	// lastJob, err := scheduler.GetLastJob(t)
	// // if it is the first try we do not take into account an error, we bail.
	// if err == scheduler.ErrNotFoundTrigger {
	// 	return nil
	// }
	// if err != nil {
	// 	return err
	// }

	// // if the job has not errored, or the last one was already errored, we bail.
	// if errjob == nil || lastJob.State == jobs.Errored {
	// 	return nil
	// }

	// i, err := instance.Get(ctx.Domain())
	// if err != nil {
	// 	return err
	// }

	// konnectorURL := i.SubDomain(consts.CollectSlug)
	// konnectorURL.Fragment = "/category/all/" + w.slug
	// mail := mails.Options{
	// 	Mode:         mails.ModeNoReply,
	// 	Subject:      i.Translate("Error Konnector execution", ctx.Domain()),
	// 	TemplateName: "konnector_error_" + i.Locale,
	// 	TemplateValues: map[string]string{
	// 		"KonnectorName": w.slug,
	// 		"KonnectorPage": konnectorURL.String(),
	// 	},
	// }

	// msg, err := jobs.NewMessage(&mail)
	// if err != nil {
	// 	return err
	// }

	// ctx.Logger().Info("Konnector has failed definitively, should send mail.", mail)
	// _, err = globals.GetBroker().PushJob(&jobs.JobRequest{
	// 	Domain:     ctx.Domain(),
	// 	WorkerType: "sendmail",
	// 	Message:    msg,
	// })
	// return err
}
