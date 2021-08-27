package api

import (
	"context"
	"errors"
	"fmt"
	"log" // nolint:revive
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/alecthomas/units"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/models"
	"github.com/influxdata/telegraf/plugins/aggregators"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/outputs"
	"github.com/influxdata/telegraf/plugins/parsers"
	"github.com/influxdata/telegraf/plugins/processors"
	"github.com/influxdata/telegraf/plugins/serializers"
)

// api is the general interface to interacting with Telegraf's current config
type api struct {
	agent  config.AgentController
	config *config.Config

	// api shutdown context
	ctx       context.Context
	outputCtx context.Context

	addHooks    []PluginCallbackEvent
	removeHooks []PluginCallbackEvent
}

// nolint:revive
func newAPI(ctx context.Context, outputCtx context.Context, cfg *config.Config, agent config.AgentController) *api {
	c := &api{
		config:    cfg,
		agent:     agent,
		ctx:       ctx,
		outputCtx: outputCtx,
	}
	return c
}

// PluginConfigTypeInfo is a plugin name and details about the config fields.
type PluginConfigTypeInfo struct {
	Name   string                 `json:"name"`
	Config map[string]FieldConfig `json:"config"`
}

type PluginConfig struct {
	ID string `json:"id"` // unique identifer
	PluginConfigCreate
}

type PluginConfigCreate struct {
	Name   string                 `json:"name"`   // name of the plugin
	Config map[string]interface{} `json:"config"` // map field name to field value
}

// FieldConfig describes a single field
type FieldConfig struct {
	Type      FieldType              `json:"type,omitempty"`       // see FieldType
	Default   interface{}            `json:"default,omitempty"`    // whatever the default value is
	Format    string                 `json:"format,omitempty"`     // type-specific format info. eg a url is a string, but has url-formatting rules.
	Required  bool                   `json:"required,omitempty"`   // this is sort of validation, which I'm not sure belongs here.
	SubType   FieldType              `json:"sub_type,omitempty"`   // The subtype. map[string]int subtype is int. []string subtype is string.
	SubFields map[string]FieldConfig `json:"sub_fields,omitempty"` // only for struct/object/FieldConfig types
}

// FieldType enumerable type. Describes config field type information to external applications
type FieldType string

// FieldTypes
const (
	FieldTypeUnknown     FieldType = ""
	FieldTypeString      FieldType = "string"
	FieldTypeInteger     FieldType = "integer"
	FieldTypeDuration    FieldType = "duration" // a special case of integer
	FieldTypeSize        FieldType = "size"     // a special case of integer
	FieldTypeFloat       FieldType = "float"
	FieldTypeBool        FieldType = "bool"
	FieldTypeInterface   FieldType = "any"
	FieldTypeSlice       FieldType = "array"  // array
	FieldTypeFieldConfig FieldType = "object" // a FieldConfig?
	FieldTypeMap         FieldType = "map"    // always map[string]FieldConfig ?
)

// Plugin is an instance of a plugin running with a specific configuration
type Plugin struct {
	ID   models.PluginID
	Name string
	// State()
	Config map[string]interface{}
}

func (a *api) ListPluginTypes() []PluginConfigTypeInfo {
	result := []PluginConfigTypeInfo{}
	inputNames := []string{}
	for name := range inputs.Inputs {
		inputNames = append(inputNames, name)
	}
	sort.Strings(inputNames)

	for _, name := range inputNames {
		creator := inputs.Inputs[name]
		cfg := PluginConfigTypeInfo{
			Name:   "inputs." + name,
			Config: map[string]FieldConfig{},
		}

		p := creator()
		getFieldConfig(p, cfg.Config)

		result = append(result, cfg)
	}

	processorNames := []string{}
	for name := range processors.Processors {
		processorNames = append(processorNames, name)
	}
	sort.Strings(processorNames)

	for _, name := range processorNames {
		creator := processors.Processors[name]
		cfg := PluginConfigTypeInfo{
			Name:   "processors." + name,
			Config: map[string]FieldConfig{},
		}

		p := creator()
		getFieldConfig(p, cfg.Config)

		result = append(result, cfg)
	}

	aggregatorNames := []string{}
	for name := range aggregators.Aggregators {
		aggregatorNames = append(aggregatorNames, name)
	}
	sort.Strings(aggregatorNames)

	for _, name := range aggregatorNames {
		creator := aggregators.Aggregators[name]
		cfg := PluginConfigTypeInfo{
			Name:   "aggregators." + name,
			Config: map[string]FieldConfig{},
		}

		p := creator()
		getFieldConfig(p, cfg.Config)

		result = append(result, cfg)
	}

	outputNames := []string{}
	for name := range outputs.Outputs {
		outputNames = append(outputNames, name)
	}
	sort.Strings(outputNames)

	for _, name := range outputNames {
		creator := outputs.Outputs[name]
		cfg := PluginConfigTypeInfo{
			Name:   "outputs." + name,
			Config: map[string]FieldConfig{},
		}

		p := creator()
		getFieldConfig(p, cfg.Config)

		result = append(result, cfg)
	}

	return result
}

func (a *api) ListRunningPlugins() (runningPlugins []Plugin) {
	if a == nil {
		panic("api is nil")
	}
	for _, v := range a.agent.RunningInputs() {
		p := Plugin{
			ID:     idToString(v.ID),
			Name:   v.LogName(),
			Config: map[string]interface{}{},
		}
		getFieldConfigValuesFromStruct(v.Config, p.Config)
		getFieldConfigValuesFromStruct(v.Input, p.Config)
		runningPlugins = append(runningPlugins, p)
	}
	for _, v := range a.agent.RunningProcessors() {
		p := Plugin{
			ID:     idToString(v.GetID()),
			Name:   v.LogName(),
			Config: map[string]interface{}{},
		}
		val := reflect.ValueOf(v)
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}
		pluginCfg := val.FieldByName("Config").Interface()
		getFieldConfigValuesFromStruct(pluginCfg, p.Config)
		if proc := val.FieldByName("Processor"); proc.IsValid() && !proc.IsNil() {
			getFieldConfigValuesFromStruct(proc.Interface(), p.Config)
		}
		if agg := val.FieldByName("Aggregator"); agg.IsValid() && !agg.IsNil() {
			getFieldConfigValuesFromStruct(agg.Interface(), p.Config)
		}
		runningPlugins = append(runningPlugins, p)
	}
	for _, v := range a.agent.RunningOutputs() {
		p := Plugin{
			ID:     idToString(v.ID),
			Name:   v.LogName(),
			Config: map[string]interface{}{},
		}
		getFieldConfigValuesFromStruct(v.Config, p.Config)
		getFieldConfigValuesFromStruct(v.Output, p.Config)
		runningPlugins = append(runningPlugins, p)
	}

	if runningPlugins == nil {
		return []Plugin{}
	}
	return runningPlugins
}

func (a *api) UpdatePlugin(id models.PluginID, cfg PluginConfigCreate) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// TODO: shut down plugin and start a new plugin with the same id.
	if err := a.DeletePlugin(id); err != nil {
		return err
	}
	// wait for plugin to stop before recreating it with the same ID, otherwise we'll have issues.
	for a.GetPluginStatus(id) != models.PluginStateDead {
		select {
		case <-ctx.Done():
			// plugin didn't stop in time.. do we recreate it anyway?
			return errors.New("timed out shutting down plugin for update")
		case <-time.After(100 * time.Millisecond):
			// try again
		}
	}
	_, err := a.CreatePlugin(cfg, id)
	return err
}

// CreatePlugin creates a new plugin from a specified config. forcedID should be left blank when used by users via the API.
func (a *api) CreatePlugin(cfg PluginConfigCreate, forcedID models.PluginID) (models.PluginID, error) {
	log.Printf("I! [configapi] creating plugin %q", cfg.Name)

	parts := strings.Split(cfg.Name, ".")
	pluginType, name := parts[0], parts[1]
	switch pluginType {
	case "inputs":
		// add an input
		input, ok := inputs.Inputs[name]
		if !ok {
			return "", fmt.Errorf("%w: finding plugin with name %s", ErrNotFound, name)
		}
		// create a copy
		i := input()
		// set the config
		if err := setFieldConfig(cfg.Config, i); err != nil {
			return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
		}

		// get parser!
		if t, ok := i.(parsers.ParserInput); ok {
			pc := &parsers.Config{
				MetricName: name,
				JSONStrict: true,
				DataFormat: "influx",
			}
			if err := setFieldConfig(cfg.Config, pc); err != nil {
				return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
			}
			parser, err := parsers.NewParser(pc)
			if err != nil {
				return "", fmt.Errorf("%w: setting parser %s", ErrBadRequest, err)
			}
			t.SetParser(parser)
		}

		if t, ok := i.(parsers.ParserFuncInput); ok {
			pc := &parsers.Config{
				MetricName: name,
				JSONStrict: true,
				DataFormat: "influx",
			}
			if err := setFieldConfig(cfg.Config, pc); err != nil {
				return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
			}

			t.SetParserFunc(func() (parsers.Parser, error) {
				return parsers.NewParser(pc)
			})
		}

		// start it and put it into the agent manager?
		pluginConfig := &models.InputConfig{Name: name}
		if err := setFieldConfig(cfg.Config, pluginConfig); err != nil {
			return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
		}

		if err := setFieldConfig(cfg.Config, &pluginConfig.Filter); err != nil {
			return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
		}

		ri := models.NewRunningInput(i, pluginConfig)
		if len(forcedID) > 0 {
			ri.ID = forcedID.Uint64()
		}
		ri.SetDefaultTags(a.config.Tags)

		if err := ri.Init(); err != nil {
			return "", fmt.Errorf("%w: could not initialize plugin %s", ErrBadRequest, err)
		}

		a.agent.AddInput(ri)
		a.addPluginHook(PluginConfig{ID: string(idToString(ri.ID)), PluginConfigCreate: PluginConfigCreate{
			Name:   "inputs." + name, // TODO: use PluginName() or something
			Config: cfg.Config,
		}})

		go a.agent.RunInput(ri, time.Now())

		return idToString(ri.ID), nil
	case "outputs":
		// add an output
		output, ok := outputs.Outputs[name]
		if !ok {
			return "", fmt.Errorf("%w: Error finding plugin with name %s", ErrNotFound, name)
		}
		// create a copy
		o := output()
		// set the config
		if err := setFieldConfig(cfg.Config, o); err != nil {
			return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
		}
		// start it and put it into the agent manager?
		pluginConfig := &models.OutputConfig{Name: name}
		if err := setFieldConfig(cfg.Config, pluginConfig); err != nil {
			return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
		}

		if err := setFieldConfig(cfg.Config, &pluginConfig.Filter); err != nil {
			return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
		}

		if t, ok := o.(serializers.SerializerOutput); ok {
			sc := &serializers.Config{
				TimestampUnits: 1 * time.Second,
				DataFormat:     "influx",
			}
			if err := setFieldConfig(cfg.Config, sc); err != nil {
				return "", fmt.Errorf("%w: setting field %s", ErrBadRequest, err)
			}
			serializer, err := serializers.NewSerializer(sc)
			if err != nil {
				return "", fmt.Errorf("%w: setting serializer %s", ErrBadRequest, err)
			}
			t.SetSerializer(serializer)
		}

		ro := models.NewRunningOutput(o, pluginConfig, a.config.Agent.MetricBatchSize, a.config.Agent.MetricBufferLimit)

		if err := ro.Init(); err != nil {
			return "", fmt.Errorf("%w: could not initialize plugin %s", ErrBadRequest, err)
		}

		a.agent.AddOutput(ro)

		a.addPluginHook(PluginConfig{ID: string(idToString(ro.ID)), PluginConfigCreate: PluginConfigCreate{
			Name:   "outputs." + name, // TODO: use PluginName() or something
			Config: cfg.Config,
		}})

		go a.agent.RunOutput(a.outputCtx, ro)

		return idToString(ro.ID), nil
	case "aggregators":
		aggregator, ok := aggregators.Aggregators[name]
		if !ok {
			return "", fmt.Errorf("%w: Error finding aggregator plugin with name %s", ErrNotFound, name)
		}
		agg := aggregator()

		// set the config
		if err := setFieldConfig(cfg.Config, agg); err != nil {
			return "", fmt.Errorf("%w: setting field", ErrBadRequest)
		}
		aggCfg := &models.AggregatorConfig{
			Name:   name,
			Delay:  time.Millisecond * 100,
			Period: time.Second * 30,
			Grace:  time.Second * 0,
		}
		if err := setFieldConfig(cfg.Config, aggCfg); err != nil {
			return "", fmt.Errorf("%w: setting field", ErrBadRequest)
		}

		if err := setFieldConfig(cfg.Config, &aggCfg.Filter); err != nil {
			return "", fmt.Errorf("%w: setting field", ErrBadRequest)
		}

		ra := models.NewRunningAggregator(agg, aggCfg)
		if err := ra.Init(); err != nil {
			return "", fmt.Errorf("%w: could not initialize plugin %s", ErrBadRequest, err)
		}
		a.agent.AddProcessor(ra)

		a.addPluginHook(PluginConfig{ID: string(idToString(ra.ID)), PluginConfigCreate: PluginConfigCreate{
			Name:   "aggregators." + name, // TODO: use PluginName() or something
			Config: cfg.Config,
		}})

		go a.agent.RunProcessor(ra)

		return idToString(ra.ID), nil

	case "processors":
		processor, ok := processors.Processors[name]
		if !ok {
			return "", fmt.Errorf("%w: Error finding processor plugin with name %s", ErrNotFound, name)
		}
		// create a copy
		p := processor()
		rootp := p.(telegraf.PluginDescriber)
		if unwrapme, ok := rootp.(unwrappable); ok {
			rootp = unwrapme.Unwrap()
		}
		// set the config
		if err := setFieldConfig(cfg.Config, rootp); err != nil {
			return "", fmt.Errorf("%w: setting field", ErrBadRequest)
		}
		// start it and put it into the agent manager?
		pluginConfig := &models.ProcessorConfig{Name: name}
		if err := setFieldConfig(cfg.Config, pluginConfig); err != nil {
			return "", fmt.Errorf("%w: setting field", ErrBadRequest)
		}
		if err := setFieldConfig(cfg.Config, &pluginConfig.Filter); err != nil {
			return "", fmt.Errorf("%w: setting field", ErrBadRequest)
		}

		rp := models.NewRunningProcessor(p, pluginConfig)

		if err := rp.Init(); err != nil {
			return "", fmt.Errorf("%w: could not initialize plugin %s", ErrBadRequest, err)
		}

		a.agent.AddProcessor(rp)

		a.addPluginHook(PluginConfig{ID: string(idToString(rp.ID)), PluginConfigCreate: PluginConfigCreate{
			Name:   "processors." + name, // TODO: use PluginName() or something
			Config: cfg.Config,
		}})

		go a.agent.RunProcessor(rp)

		return idToString(rp.ID), nil
	default:
		return "", fmt.Errorf("%w: Unknown plugin type", ErrNotFound)
	}
}

func (a *api) GetPluginStatus(id models.PluginID) models.PluginState {
	for _, v := range a.agent.RunningInputs() {
		if v.ID == id.Uint64() {
			return v.GetState()
		}
	}
	for _, v := range a.agent.RunningProcessors() {
		if v.GetID() == id.Uint64() {
			return v.GetState()
		}
	}
	for _, v := range a.agent.RunningOutputs() {
		if v.ID == id.Uint64() {
			return v.GetState()
		}
	}
	return models.PluginStateDead
}

func (a *api) DeletePlugin(id models.PluginID) error {
	a.removePluginHook(PluginConfig{ID: string(id)})

	for _, v := range a.agent.RunningInputs() {
		if v.ID == id.Uint64() {
			log.Printf("I! [configapi] stopping plugin %q", v.LogName())
			a.agent.StopInput(v)
			return nil
		}
	}
	for _, v := range a.agent.RunningProcessors() {
		if v.GetID() == id.Uint64() {
			log.Printf("I! [configapi] stopping plugin %q", v.LogName())
			a.agent.StopProcessor(v)
			return nil
		}
	}
	for _, v := range a.agent.RunningOutputs() {
		if v.ID == id.Uint64() {
			log.Printf("I! [configapi] stopping plugin %q", v.LogName())
			a.agent.StopOutput(v)
			return nil
		}
	}
	return ErrNotFound
}

type PluginCallbackEvent func(p PluginConfig)

// addPluginHook triggers the hook to fire this event
func (a *api) addPluginHook(p PluginConfig) {
	for _, h := range a.addHooks {
		h(p)
	}
}

// removePluginHook triggers the hook to fire this event
func (a *api) removePluginHook(p PluginConfig) {
	for _, h := range a.removeHooks {
		h(p)
	}
}

// OnPluginAdded adds a hook to get notified of this event
func (a *api) OnPluginAdded(f PluginCallbackEvent) {
	a.addHooks = append(a.addHooks, f)
}

// OnPluginRemoved adds a hook to get notified of this event
func (a *api) OnPluginRemoved(f PluginCallbackEvent) {
	a.removeHooks = append(a.removeHooks, f)
}

// setFieldConfig takes a map of field names to field values and sets them on the plugin
func setFieldConfig(cfg map[string]interface{}, p interface{}) error {
	destStruct := reflect.ValueOf(p)
	if destStruct.Kind() == reflect.Ptr {
		destStruct = destStruct.Elem()
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := cfg[k]
		destField, destFieldType := getFieldByName(destStruct, k) // get by tag
		if !destField.IsValid() {
			continue
		}
		if !destField.CanSet() {
			destField.Addr()
			return fmt.Errorf("cannot set %s (%s)", k, destFieldType.Name())
		}
		val := reflect.ValueOf(v)
		if err := setObject(val, destField, destFieldType); err != nil {
			return fmt.Errorf("Could not set field %q: %w", k, err)
		}
	}
	return nil
}

// getFieldByName gets a reference to a struct field from it's name, considering the tag names
func getFieldByName(destStruct reflect.Value, fieldName string) (reflect.Value, reflect.Type) {
	if destStruct.Kind() == reflect.Ptr {
		if destStruct.IsNil() {
			return reflect.ValueOf(nil), reflect.TypeOf(nil)
		}
		destStruct = destStruct.Elem()
	}
	// may be an interface to a struct
	if destStruct.Kind() == reflect.Interface {
		destStruct = destStruct.Elem()
	}
	destStructType := reflect.TypeOf(destStruct.Interface())
	for i := 0; i < destStruct.NumField(); i++ {
		field := destStruct.Field(i)
		fieldType := destStructType.Field(i)
		if fieldType.Type.Kind() == reflect.Struct && fieldType.Anonymous {
			v, t := getFieldByName(field, fieldName)
			if t != reflect.TypeOf(nil) {
				return v, t
			}
		}
		if fieldType.Tag.Get("toml") == fieldName {
			return field, fieldType.Type
		}
		if name, ok := toSnakeCase(fieldType.Name, fieldType); ok {
			if name == fieldName && isExported(fieldType) {
				return field, fieldType.Type
			}
		}
	}
	return reflect.ValueOf(nil), reflect.TypeOf(nil)
}

// getFieldConfig builds FieldConfig objects based on the structure of a plugin's struct
// it expects a plugin, p, (of any type) and a map to populate.
// it calls itself recursively so p must be an interface{}
func getFieldConfig(p interface{}, cfg map[string]FieldConfig) {
	structVal := reflect.ValueOf(p)
	structType := structVal.Type()
	for structType.Kind() == reflect.Ptr {
		structVal = structVal.Elem()
		structType = structType.Elem()
	}

	// safety check.
	if structType.Kind() != reflect.Struct {
		// woah, what?
		panic(fmt.Sprintf("getFieldConfig expected a struct type, but got %v %v", p, structType.String()))
	}
	// structType.NumField()

	for i := 0; i < structType.NumField(); i++ {
		var f reflect.Value
		if !structVal.IsZero() {
			f = structVal.Field(i)
		}
		_ = f
		ft := structType.Field(i)

		ftType := ft.Type
		if ftType.Kind() == reflect.Ptr {
			ftType = ftType.Elem()
			// f = f.Elem()
		}

		// check if it's not exported, and skip if so.
		if len(ft.Name) > 0 && strings.ToLower(string(ft.Name[0])) == string(ft.Name[0]) {
			continue
		}
		tomlTag := ft.Tag.Get("toml")
		if tomlTag == "-" {
			continue
		}
		switch ftType.Kind() {
		case reflect.Func, reflect.Interface:
			continue
		}

		// if this field is a struct, get the structure of it.
		if ftType.Kind() == reflect.Struct && !isInternalStructFieldType(ft.Type) {
			if ft.Anonymous { // embedded
				t := getSubTypeType(ft.Type)
				i := reflect.New(t)
				getFieldConfig(i.Interface(), cfg)
			} else {
				subCfg := map[string]FieldConfig{}
				t := getSubTypeType(ft.Type)
				i := reflect.New(t)
				getFieldConfig(i.Interface(), subCfg)
				cfg[ft.Name] = FieldConfig{
					Type:      FieldTypeFieldConfig,
					SubFields: subCfg,
					SubType:   getFieldType(t),
				}
			}
			continue
		}

		// all other field types...
		fc := FieldConfig{
			Type:     getFieldTypeFromStructField(ft),
			Format:   ft.Tag.Get("format"),
			Required: ft.Tag.Get("required") == "true",
		}

		// set the default value for the field
		if f.IsValid() && !f.IsZero() {
			fc.Default = f.Interface()
			// special handling for internal struct types so the struct doesn't serialize to an object.
			if d, ok := fc.Default.(config.Duration); ok {
				fc.Default = d
			}
			if s, ok := fc.Default.(config.Size); ok {
				fc.Default = s
			}
		}

		// if we found a slice of objects, get the structure of that object
		if hasSubType(ft.Type) {
			t := getSubTypeType(ft.Type)
			n := t.Name()
			_ = n
			fc.SubType = getFieldType(t)

			if t.Kind() == reflect.Struct {
				i := reflect.New(t)
				subCfg := map[string]FieldConfig{}
				getFieldConfig(i.Interface(), subCfg)
				fc.SubFields = subCfg
			}
		}
		// if we found a map of objects, get the structure of that object

		cfg[ft.Name] = fc
	}
}

// getFieldConfigValuesFromStruct takes a struct and populates a map.
func getFieldConfigValuesFromStruct(p interface{}, cfg map[string]interface{}) {
	structVal := reflect.ValueOf(p)
	structType := structVal.Type()
	if structVal.IsZero() {
		return
	}

	for structType.Kind() == reflect.Ptr {
		structVal = structVal.Elem()
		structType = structType.Elem()
	}

	// safety check.
	if structType.Kind() != reflect.Struct {
		// woah, what?
		panic(fmt.Sprintf("getFieldConfigValues expected a struct type, but got %v %v", p, structType.String()))
	}

	for i := 0; i < structType.NumField(); i++ {
		f := structVal.Field(i)
		ft := structType.Field(i)

		if !isExported(ft) {
			continue
		}
		if ft.Name == "Log" {
			continue
		}
		ftType := ft.Type
		if ftType.Kind() == reflect.Ptr {
			f = f.Elem()
		}
		// if struct call self recursively
		// if it's composed call self recursively
		if name, ok := toSnakeCase(ft.Name, ft); ok {
			setMapKey(cfg, name, f)
		}
	}
}

func getFieldConfigValuesFromValue(val reflect.Value) interface{} {
	typ := val.Type()
	// typ may be a pointer to a type
	if typ.Kind() == reflect.Ptr {
		val = val.Elem()
		typ = val.Type()
	}

	switch typ.Kind() {
	case reflect.Slice:
		return getFieldConfigValuesFromSlice(val)
	case reflect.Struct:
		m := map[string]interface{}{}
		getFieldConfigValuesFromStruct(val.Interface(), m)
		return m
	case reflect.Map:
		return getFieldConfigValuesFromMap(val)
	case reflect.Bool:
		return val.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return val.Int()
	case reflect.Int64:
		// special case for config.Duration, time.Duration etc.
		switch typ.String() {
		case "time.Duration", "config.Duration":
			return time.Duration(val.Int()).String()
		case "config.Size":
			sz := config.Size(val.Int())
			return string((&sz).MarshalText())
		}
		return val.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return val.Uint()
	case reflect.Float32, reflect.Float64:
		return val.Float()
	case reflect.Interface:
		return val.Interface() // do we bother to decode this?
	case reflect.Ptr:
		return getFieldConfigValuesFromValue(val.Elem())
	case reflect.String:
		return val.String()
	default:
		return val.Interface() // log that we missed the type?
	}
}

func getFieldConfigValuesFromMap(f reflect.Value) map[string]interface{} {
	obj := map[string]interface{}{}
	iter := f.MapRange()
	for iter.Next() {
		setMapKey(obj, iter.Key().String(), iter.Value())
	}
	return obj
}

func getFieldConfigValuesFromSlice(val reflect.Value) []interface{} {
	s := []interface{}{}
	for i := 0; i < val.Len(); i++ {
		s = append(s, getFieldConfigValuesFromValue(val.Index(i)))
	}
	return s
}

func setMapKey(obj map[string]interface{}, key string, v reflect.Value) {
	v = reflect.ValueOf(getFieldConfigValuesFromValue(v))
	switch v.Kind() {
	case reflect.Bool:
		obj[key] = v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		obj[key] = v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		obj[key] = v.Uint()
	case reflect.Float32, reflect.Float64:
		obj[key] = v.Float()
	case reflect.Interface:
		obj[key] = v.Interface()
	case reflect.Map:
		obj[key] = v.Interface().(map[string]interface{})
	case reflect.Ptr:
		setMapKey(obj, key, v.Elem())
	case reflect.Slice:
		obj[key] = v.Interface()
	case reflect.String:
		obj[key] = v.String()
	case reflect.Struct:
		obj[key] = v.Interface()
	default:
		// obj[key] = v.Interface()
		panic("unhandled type " + v.Type().String() + " for field " + key)
	}
}

func isExported(ft reflect.StructField) bool {
	return unicode.IsUpper(rune(ft.Name[0]))
}

var matchFirstCapital = regexp.MustCompile("(.)([A-Z][a-z]+)")
var matchAllCapitals = regexp.MustCompile("([a-z0-9])([A-Z])")

func toSnakeCase(str string, sf reflect.StructField) (result string, ok bool) {
	if toml, ok := sf.Tag.Lookup("toml"); ok {
		if toml == "-" {
			return "", false
		}
		return toml, true
	}
	snakeStr := matchFirstCapital.ReplaceAllString(str, "${1}_${2}")
	snakeStr = matchAllCapitals.ReplaceAllString(snakeStr, "${1}_${2}")
	return strings.ToLower(snakeStr), true
}

func setObject(from, to reflect.Value, destType reflect.Type) error {
	if from.Kind() == reflect.Interface {
		from = reflect.ValueOf(from.Interface())
	}
	// switch on source type
	switch from.Kind() {
	case reflect.Bool:
		if to.Kind() == reflect.Ptr {
			ptr := reflect.New(destType.Elem())
			to.Set(ptr)
			to = ptr.Elem()
		}
		if to.Kind() == reflect.Interface {
			to.Set(from)
		} else {
			to.SetBool(from.Bool())
		}
	case reflect.String:
		if to.Kind() == reflect.Ptr {
			ptr := reflect.New(destType.Elem())
			destType = destType.Elem()
			to.Set(ptr)
			to = ptr.Elem()
		}
		// consider duration and size types
		switch destType.String() {
		case "time.Duration", "config.Duration":
			d, err := time.ParseDuration(from.Interface().(string))
			if err != nil {
				return fmt.Errorf("Couldn't parse duration %q: %w", from.Interface().(string), err)
			}
			to.SetInt(int64(d))
		case "config.Size":
			size, err := units.ParseStrictBytes(from.Interface().(string))
			if err != nil {
				return fmt.Errorf("Couldn't parse size %q: %w", from.Interface().(string), err)
			}
			to.SetInt(size)
		// TODO: handle slice types?
		default:
			// to.SetString(from.Interface().(string))
			to.Set(from)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if to.Kind() == reflect.Ptr {
			ptr := reflect.New(destType.Elem())
			destType = destType.Elem()
			to.Set(ptr)
			to = ptr.Elem()
		}

		if destType.String() == "internal.Number" {
			n := float64(from.Int())
			to.Set(reflect.ValueOf(n))
			return nil
		}

		switch destType.Kind() {
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			to.SetUint(uint64(from.Int()))
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			to.SetInt(from.Int())
		case reflect.Float32, reflect.Float64:
			to.SetFloat(from.Float())
		case reflect.Interface:
			to.Set(from)
		default:
			return fmt.Errorf("cannot coerce int type into %s", destType.Kind().String())
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if to.Kind() == reflect.Ptr {
			ptr := reflect.New(destType.Elem())
			// destType = destType.Elem()
			to.Set(ptr)
			to = ptr.Elem()
		}

		if destType.String() == "internal.Number" {
			n := float64(from.Uint())
			to.Set(reflect.ValueOf(n))
			return nil
		}

		if to.Kind() == reflect.Interface {
			to.Set(from)
		} else {
			to.SetUint(from.Uint())
		}
	case reflect.Float32, reflect.Float64:
		if to.Kind() == reflect.Ptr {
			ptr := reflect.New(destType.Elem())
			// destType = destType.Elem()
			to.Set(ptr)
			to = ptr.Elem()
		}
		if destType.String() == "internal.Number" {
			n := from.Float()
			to.Set(reflect.ValueOf(n))
			return nil
		}

		if to.Kind() == reflect.Interface {
			to.Set(from)
		} else {
			to.SetFloat(from.Float())
		}
	case reflect.Slice:
		if destType.Kind() == reflect.Ptr {
			destType = destType.Elem()
			to = to.Elem()
		}
		if destType.Kind() != reflect.Slice {
			return fmt.Errorf("error setting slice field into %s", destType.Kind().String())
		}
		d := reflect.MakeSlice(destType, from.Len(), from.Len())
		for i := 0; i < from.Len(); i++ {
			if err := setObject(from.Index(i), d.Index(i), destType.Elem()); err != nil {
				return fmt.Errorf("couldn't set slice element: %w", err)
			}
		}
		to.Set(d)
	case reflect.Map:
		if destType.Kind() == reflect.Ptr {
			destType = destType.Elem()
			ptr := reflect.New(destType)
			to.Set(ptr)
			to = to.Elem()
		}
		switch destType.Kind() {
		case reflect.Struct:
			structPtr := reflect.New(destType)
			err := setFieldConfig(from.Interface().(map[string]interface{}), structPtr.Interface())
			if err != nil {
				return err
			}
			to.Set(structPtr.Elem())
		case reflect.Map:
			//TODO: handle map[string]type
			if destType.Key().Kind() != reflect.String {
				panic("expecting string types for maps")
			}
			to.Set(reflect.MakeMap(destType))

			switch destType.Elem().Kind() {
			case reflect.Interface,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
				reflect.Float32, reflect.Float64,
				reflect.Bool:
				for _, k := range from.MapKeys() {
					t := from.MapIndex(k)
					if t.Kind() == reflect.Interface {
						t = reflect.ValueOf(t.Interface())
					}
					to.SetMapIndex(k, t)
				}
			case reflect.String:
				for _, k := range from.MapKeys() {
					t := from.MapIndex(k)
					if t.Kind() == reflect.Interface {
						t = reflect.ValueOf(t.Interface())
					}
					to.SetMapIndex(k, t)
				}
				// for _, k := range from.MapKeys() {
				// 	v := from.MapIndex(k)
				// 	s := v.Interface().(string)
				// 	to.SetMapIndex(k, reflect.ValueOf(s))
				// }
			case reflect.Slice:
				for _, k := range from.MapKeys() {
					// slice := reflect.MakeSlice(destType.Elem(), 0, 0)
					sliceptr := reflect.New(destType.Elem())
					// sliceptr.Elem().Set(slice)
					err := setObject(from.MapIndex(k), sliceptr, sliceptr.Type())
					if err != nil {
						return fmt.Errorf("could not set slice: %w", err)
					}
					to.SetMapIndex(k, sliceptr.Elem())
				}

			case reflect.Struct:
				for _, k := range from.MapKeys() {
					structPtr := reflect.New(destType.Elem())
					err := setFieldConfig(
						from.MapIndex(k).Interface().(map[string]interface{}),
						structPtr.Interface(),
					)
					// err := setObject(from.MapIndex(k), structPtr, structPtr.Type())
					if err != nil {
						return fmt.Errorf("could not set struct: %w", err)
					}
					to.SetMapIndex(k, structPtr.Elem())
				}

			default:
				return fmt.Errorf("can't write settings into map of type map[string]%s", destType.Elem().Kind().String())
			}
		default:
			return fmt.Errorf("Cannot load map into %q", destType.Kind().String())
			// panic("foo")
		}
		// to.Set(val)
	default:
		return fmt.Errorf("cannot convert unknown type %s to %s", from.Kind().String(), destType.String())
	}
	return nil
}

// hasSubType returns true when the field has a subtype (map,slice,struct)
func hasSubType(t reflect.Type) bool {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Map:
		return true
	case reflect.Struct:
		switch t.String() {
		case "internal.Duration", "config.Duration", "internal.Size", "config.Size":
			return false
		}
		return true
	default:
		return false
	}
}

// getSubTypeType gets the underlying subtype's reflect.Type
// examples:
//   []string => string
//   map[string]int => int
//   User => User
func getSubTypeType(typ reflect.Type) reflect.Type {
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Slice:
		t := typ.Elem()
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		return t
	case reflect.Map:
		t := typ.Elem()
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		return t
	case reflect.Struct:
		return typ
	}
	panic(typ.String() + " is not a type that has subtype information (map, slice, struct)")
}

// getFieldType translates reflect.Types to our API field types.
func getFieldType(t reflect.Type) FieldType {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return FieldTypeString
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64:
		return FieldTypeInteger
	case reflect.Float32, reflect.Float64:
		return FieldTypeFloat
	case reflect.Bool:
		return FieldTypeBool
	case reflect.Slice:
		return FieldTypeSlice
	case reflect.Map:
		return FieldTypeMap
	case reflect.Struct:
		switch t.String() {
		case "internal.Duration", "config.Duration":
			return FieldTypeDuration
		case "internal.Size", "config.Size":
			return FieldTypeSize
		}
		return FieldTypeFieldConfig
	}
	return FieldTypeUnknown
}

func getFieldTypeFromStructField(structField reflect.StructField) FieldType {
	fieldName := structField.Name
	ft := structField.Type
	result := getFieldType(ft)
	if result == FieldTypeUnknown {
		panic(fmt.Sprintf("unknown type, name: %q, string: %q", fieldName, ft.String()))
	}
	return result
}

func isInternalStructFieldType(t reflect.Type) bool {
	switch t.String() {
	case "internal.Duration", "config.Duration":
		return true
	case "internal.Size", "config.Size":
		return true
	default:
		return false
	}
}

func idToString(id uint64) models.PluginID {
	return models.PluginID(fmt.Sprintf("%016x", id))
}

// make sure these models implement RunningPlugin
var _ models.RunningPlugin = &models.RunningProcessor{}
var _ models.RunningPlugin = &models.RunningAggregator{}
var _ models.RunningPlugin = &models.RunningInput{}
var _ models.RunningPlugin = &models.RunningOutput{}

type unwrappable interface {
	Unwrap() telegraf.Processor
}
