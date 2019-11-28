package feature

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/url"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
)

type Flags struct {
	DocID   string
	DocRev  string
	M       map[string]interface{}
	Sources []*Flags
}

func (f *Flags) ID() string        { return f.DocID }
func (f *Flags) Rev() string       { return f.DocRev }
func (f *Flags) DocType() string   { return consts.Settings }
func (f *Flags) SetID(id string)   { f.DocID = id }
func (f *Flags) SetRev(rev string) { f.DocRev = rev }
func (f *Flags) Clone() couchdb.Doc {
	clone := Flags{DocID: f.DocID, DocRev: f.DocRev}
	clone.M = make(map[string]interface{})
	for k, v := range f.M {
		clone.M[k] = v
	}
	return &clone
}
func (f *Flags) MarshalJSON() ([]byte, error) {
	return json.Marshal(f.M)
}

func (f *Flags) UnmarshalJSON(bytes []byte) error {
	err := json.Unmarshal(bytes, &f.M)
	if err != nil {
		return err
	}
	if id, ok := f.M["_id"].(string); ok {
		f.SetID(id)
		delete(f.M, "_id")
	}
	if rev, ok := f.M["_rev"].(string); ok {
		f.SetRev(rev)
		delete(f.M, "_rev")
	}
	return nil
}

func GetFlags(inst *instance.Instance) (*Flags, error) {
	sources := make([]*Flags, 0)
	m := make(map[string]interface{})
	flags := &Flags{
		DocID:   consts.FlagsSettingsID,
		M:       m,
		Sources: sources,
	}
	flags.addInstanceFlags(inst)
	if err := flags.addManager(inst); err != nil {
		return nil, err
	}
	if err := flags.addConfig(inst); err != nil {
		return nil, err
	}
	if err := flags.addContext(inst); err != nil {
		return nil, err
	}
	if err := flags.addDefaults(inst); err != nil {
		return nil, err
	}
	return flags, nil
}

func (f *Flags) addInstanceFlags(inst *instance.Instance) {
	if len(inst.FeatureFlags) == 0 {
		return
	}
	m := make(map[string]interface{})
	for k, v := range inst.FeatureFlags {
		m[k] = v
	}
	flags := &Flags{
		DocID: consts.InstanceFlagsSettingsID,
		M:     m,
	}
	f.Sources = append(f.Sources, flags)
	for k, v := range flags.M {
		if _, ok := f.M[k]; !ok {
			f.M[k] = v
		}
	}
}

func (f *Flags) addManager(inst *instance.Instance) error {
	if len(inst.FeatureSets) == 0 {
		return nil
	}
	m, err := getFlagsFromManager(inst)
	if err != nil || len(m) == 0 {
		return err
	}
	flags := &Flags{
		DocID: consts.ManagerFlagsSettingsID,
		M:     m,
	}
	f.Sources = append(f.Sources, flags)
	for k, v := range flags.M {
		if _, ok := f.M[k]; !ok {
			f.M[k] = v
		}
	}
	return nil
}

var (
	cacheDuration      = 12 * time.Hour
	errInvalidResponse = errors.New("Invalid response from the manager")
)

func getFlagsFromManager(inst *instance.Instance) (map[string]interface{}, error) {
	cache := config.GetConfig().CacheStorage
	cacheKey := fmt.Sprintf("flags:%s:%v", inst.ContextName, inst.FeatureSets)
	var flags map[string]interface{}
	if buf, ok := cache.Get(cacheKey); ok {
		if err := json.Unmarshal(buf, &flags); err == nil {
			return flags, nil
		}
	}

	client := instance.APIManagerClient(inst)
	if client == nil {
		return flags, nil
	}
	query := url.Values{
		"sets":    {strings.Join(inst.FeatureSets, ",")},
		"context": {inst.ContextName},
	}.Encode()
	var data map[string]interface{}
	if err := client.Get(fmt.Sprintf("/api/v1/flags?%s", query), &data); err != nil {
		return nil, err
	}
	var ok bool
	if flags, ok = data["flags"].(map[string]interface{}); !ok {
		return nil, errInvalidResponse
	}

	if buf, err := json.Marshal(flags); err == nil {
		cache.Set(cacheKey, buf, cacheDuration)
	}
	return flags, nil
}

func (f *Flags) addConfig(inst *instance.Instance) error {
	ctx, err := inst.SettingsContext()
	if err == instance.ErrContextNotFound {
		return nil
	} else if err != nil {
		return err
	}
	m, ok := ctx["features"].(map[interface{}]interface{})
	if !ok {
		return nil
	}
	normalized := make(map[string]interface{})
	for k, v := range m {
		normalized[fmt.Sprintf("%v", k)] = v
	}
	ctxFlags := &Flags{
		DocID: consts.ConfigFlagsSettingsID,
		M:     normalized,
	}
	f.Sources = append(f.Sources, ctxFlags)
	for k, v := range ctxFlags.M {
		if _, ok := f.M[k]; !ok {
			f.M[k] = v
		}
	}
	return nil
}

func (f *Flags) addContext(inst *instance.Instance) error {
	id := fmt.Sprintf("%s.%s", consts.ContextFlagsSettingsID, inst.ContextName)
	var context Flags
	err := couchdb.GetDoc(couchdb.GlobalDB, consts.Settings, id, &context)
	if couchdb.IsNotFoundError(err) {
		return nil
	} else if err != nil {
		return err
	}
	context.SetID(consts.ContextFlagsSettingsID)
	f.Sources = append(f.Sources, &context)
	for k, v := range context.M {
		if _, ok := f.M[k]; !ok {
			if value := applyRatio(inst, k, v); value != nil {
				f.M[k] = value
			}
		}
	}
	return nil
}

const maxUint32 = 1<<32 - 1

func applyRatio(inst *instance.Instance, key string, data interface{}) interface{} {
	items, ok := data.([]interface{})
	if !ok || len(items) == 0 {
		return nil
	}
	sum := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s:%s", inst.ContextName, key)))
	for i := range items {
		item, ok := items[i].(map[string]interface{})
		if !ok {
			continue
		}
		ratio, ok := item["ratio"].(float64)
		if !ok || ratio == 0.0 {
			continue
		}
		if ratio == 1.0 {
			return item["value"]
		}
		computed := uint32(ratio * maxUint32)
		if computed >= sum {
			return item["value"]
		}
		sum -= computed
	}
	return nil
}

func (f *Flags) addDefaults(inst *instance.Instance) error {
	var defaults Flags
	err := couchdb.GetDoc(couchdb.GlobalDB, consts.Settings, consts.DefaultFlagsSettingsID, &defaults)
	if couchdb.IsNotFoundError(err) {
		return nil
	} else if err != nil {
		return err
	}
	defaults.SetID(consts.DefaultFlagsSettingsID)
	f.Sources = append(f.Sources, &defaults)
	for k, v := range defaults.M {
		if _, ok := f.M[k]; !ok {
			f.M[k] = v
		}
	}
	return nil
}

var _ couchdb.Doc = &Flags{}
