package restperf

import (
	"encoding/json"
	"fmt"
	"github.com/tidwall/gjson"
	rest2 "goharvest2/cmd/collectors/rest"
	"goharvest2/cmd/poller/collector"
	"goharvest2/cmd/poller/plugin"
	"goharvest2/cmd/tools/rest"
	"goharvest2/pkg/color"
	"goharvest2/pkg/errors"
	"goharvest2/pkg/matrix"
	"goharvest2/pkg/set"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	latencyIoReqd = 10
	BILLION       = 1_000_000_000
)

var qosQuery = "api/cluster/counter/tables/qos"
var qosVolumeQuery = "api/cluster/counter/tables/qos_volume"
var qosDetailQuery = "api/cluster/counter/tables/qos_detail"
var qosDetailVolumeQuery = "api/cluster/counter/tables/qos_detail_volume"
var qosWorkloadQuery = "api/storage/qos/workloads"

var qosQueries = map[string]string{
	qosQuery:       qosQuery,
	qosVolumeQuery: qosVolumeQuery,
}
var qosDetailQueries = map[string]string{
	qosDetailQuery:       qosDetailQuery,
	qosDetailVolumeQuery: qosDetailVolumeQuery,
}

type RestPerf struct {
	*rest2.Rest // provides: AbstractCollector, Client, Object, Query, TemplateFn, TemplateType
	perfProp    *perfProp
}

type counter struct {
	name        string
	description string
	counterType string
	unit        string
	denominator string
}

type perfProp struct {
	isCacheEmpty  bool
	counterInfo   map[string]*counter
	latencyIoReqd int
	qosLabels     map[string]string
}

type metricResponse struct {
	label   string
	value   string
	isArray bool
}

func init() {
	plugin.RegisterModule(RestPerf{})
}

func (RestPerf) HarvestModule() plugin.ModuleInfo {
	return plugin.ModuleInfo{
		ID:  "harvest.collector.restperf",
		New: func() plugin.Module { return new(RestPerf) },
	}
}

func (r *RestPerf) Init(a *collector.AbstractCollector) error {

	var err error

	r.Rest = &rest2.Rest{AbstractCollector: a}

	r.perfProp = &perfProp{}

	r.InitProp()

	r.perfProp.counterInfo = make(map[string]*counter)

	if err = r.InitClient(); err != nil {
		return err
	}

	if r.Prop.TemplatePath, err = r.LoadTemplate(); err != nil {
		return err
	}

	if err = collector.Init(r); err != nil {
		return err
	}

	if err = r.InitCache(); err != nil {
		return err
	}

	if err = r.InitMatrix(); err != nil {
		return err
	}

	if err = r.InitQOSLabels(); err != nil {
		return err
	}

	r.Logger.Info().Str("count", strconv.Itoa(len(r.Matrix.GetMetrics()))).Msg("initialized cache with metrics")

	return nil
}

func (r *RestPerf) InitQOSLabels() error {
	if isWorkloadObject(r.Prop.Query) || isWorkloadDetailObject(r.Prop.Query) {
		if qosLabels := r.Params.GetChildS("qos_labels"); qosLabels == nil {
			return errors.New(errors.MISSING_PARAM, "qos_labels")
		} else {
			r.perfProp.qosLabels = make(map[string]string)
			for _, label := range qosLabels.GetAllChildContentS() {

				display := strings.ReplaceAll(label, "-", "_")
				before, after, found := strings.Cut(label, "=>")
				if found {
					label = strings.TrimSpace(before)
					display = strings.TrimSpace(after)
				}
				r.perfProp.qosLabels[label] = display
			}
		}
	}
	return nil
}

func (r *RestPerf) InitMatrix() error {
	//init perf properties
	r.perfProp.latencyIoReqd = r.loadParamInt("latency_io_reqd", latencyIoReqd)
	r.perfProp.isCacheEmpty = true
	// overwrite from abstract collector
	r.Matrix.Object = r.Prop.Object
	// Add system (cluster) name
	r.Matrix.SetGlobalLabel("cluster", r.Client.Cluster().Name)
	if r.Params.HasChildS("labels") {
		for _, l := range r.Params.GetChildS("labels").GetChildren() {
			r.Matrix.SetGlobalLabel(l.GetNameS(), l.GetContentS())
		}
	}
	return nil
}

// load a string parameter or use defaultValue
func (r *RestPerf) loadParamStr(name, defaultValue string) string {

	var x string

	if x = r.Params.GetChildContentS(name); x != "" {
		r.Logger.Debug().Str("name", name).Msg("using values")
		return x
	}
	r.Logger.Debug().Str("name", name).Str("defaultValue", defaultValue).Msg("using values")
	return defaultValue
}

// load an int parameter or use defaultValue
func (r *RestPerf) loadParamInt(name string, defaultValue int) int {

	var (
		x string
		n int
		e error
	)

	if x = r.Params.GetChildContentS(name); x != "" {
		if n, e = strconv.Atoi(x); e == nil {
			r.Logger.Debug().Msgf("using %s = [%d]", name, n)
			return n
		}
		r.Logger.Warn().Msgf("invalid parameter %s = [%s] (expected integer)", name, x)
	}

	r.Logger.Debug().Str("name", name).Str("defaultValue", strconv.Itoa(defaultValue)).Msg("using values")
	return defaultValue
}

func (r *RestPerf) PollCounter() (*matrix.Matrix, error) {
	var (
		content []byte
		err     error
		records []interface{}
	)

	href := rest.BuildHref(r.Prop.Query, "", nil, "", "", "", r.Prop.ReturnTimeOut, r.Prop.Query)
	r.Logger.Debug().Str("href", href).Msg("")
	if href == "" {
		return nil, errors.New(errors.ERR_CONFIG, "empty url")
	}

	err = rest.FetchData(r.Client, href, &records)
	if err != nil {
		r.Logger.Error().Stack().Err(err).Str("href", href).Msg("Failed to fetch data")
		return nil, err
	}

	all := rest.Pagination{
		Records: records,
	}

	content, err = json.Marshal(all)
	if err != nil {
		r.Logger.Error().Err(err).Str("ApiPath", r.Prop.Query).Msg("Unable to marshal rest pagination")
	}

	if !gjson.ValidBytes(content) {
		return nil, fmt.Errorf("json is not valid for: %s", r.Prop.Query)
	}

	results := gjson.GetManyBytes(content, "records.0.counter_schemas", "records.0.name", "records.0.description")

	// populate denominator metric to prop metrics
	results[0].ForEach(func(key, c gjson.Result) bool {
		if !c.IsObject() {
			r.Logger.Warn().Str("type", c.Type.String()).Msg("Counter is not object, skipping")
			return true
		}

		name := c.Get("name").String()
		if _, has := r.Prop.Metrics[name]; has {
			d := c.Get("denominator.name").String()
			if d != "" {
				if _, has := r.Prop.Metrics[d]; !has {
					// export false
					m := &rest2.Metric{Label: "", Name: d, MetricType: "", Exportable: false}
					r.Prop.Metrics[d] = m
				}
			}
		}
		return true
	})

	results[0].ForEach(func(key, c gjson.Result) bool {

		if !c.IsObject() {
			r.Logger.Warn().Str("type", c.Type.String()).Msg("Counter is not object, skipping")
			return true
		}

		name := c.Get("name").String()
		if _, has := r.Prop.Metrics[name]; has {
			if _, ok := r.perfProp.counterInfo[name]; !ok {
				r.perfProp.counterInfo[name] = &counter{
					name:        c.Get("name").String(),
					description: c.Get("description").String(),
					counterType: c.Get("type").String(),
					unit:        c.Get("unit").String(),
					denominator: c.Get("denominator.name").String(),
				}
				if p := r.GetOverride(name); p != "" {
					r.perfProp.counterInfo[name].counterType = p
				}
			}
		} else {
			r.Logger.Trace().
				Str("key", name).
				Msg("Skip counter not requested")
		}
		return true
	})

	// Create an artificial metric to hold timestamp of each instance data.
	// The reason we don't keep a single timestamp for the whole data
	// is because we might get instances in different batches
	if r.Matrix.GetMetric("timestamp") == nil {
		m, err := r.Matrix.NewMetricFloat64("timestamp")
		if err != nil {
			r.Logger.Error().Stack().Err(err).Msg("add timestamp metric")
		}
		m.SetProperty("raw")
		m.SetExportable(false)
	}

	_, err = r.processWorkLoadCounter()
	if err != nil {
		return r.Matrix, err
	}

	return r.Matrix, nil
}

func parseProperties(instanceData gjson.Result, property string) gjson.Result {
	if property == "id" {
		value := gjson.Get(instanceData.String(), "id")
		return value
	}
	t := gjson.Get(instanceData.String(), "properties.#.name")

	for _, name := range t.Array() {
		if name.String() == property {
			value := gjson.Get(instanceData.String(), "properties.#(name="+property+").value")
			return value
		}
	}
	return gjson.Result{}
}

var metricTypeRegex = regexp.MustCompile(`\[(.*?)\]`)

func arrayMetricToString(value string) string {
	r := strings.NewReplacer("\n", "", " ", "", "\"", "")
	s := r.Replace(value)

	match := metricTypeRegex.FindAllStringSubmatch(s, -1)
	if match != nil {
		name := match[0][1]
		return name
	}
	return value
}

func parseMetricResponse(instanceData gjson.Result, metric string) *metricResponse {
	t := gjson.Get(instanceData.String(), "counters.#.name")

	for _, name := range t.Array() {
		if name.String() == metric {
			value := gjson.Get(instanceData.String(), "counters.#(name="+metric+").value")
			if value.String() != "" {
				return &metricResponse{value: value.String(), label: "", isArray: false}
			}
			values := gjson.Get(instanceData.String(), "counters.#(name="+metric+").values")
			if values.String() != "" {
				label := gjson.Get(instanceData.String(), "counters.#(name="+metric+").labels")
				return &metricResponse{value: arrayMetricToString(values.String()), label: arrayMetricToString(label.String()), isArray: true}
			}
		}
	}
	return &metricResponse{}
}

// GetOverride override counter property
func (r *RestPerf) GetOverride(counter string) string {
	if o := r.Params.GetChildS("override"); o != nil {
		return o.GetChildContentS(counter)
	}
	return ""
}

func (r *RestPerf) processWorkLoadCounter() (*matrix.Matrix, error) {
	var err error
	// for these two objects, we need to create latency/ops counters for each of the workload layers
	// their original counters will be discarded
	if isWorkloadDetailObject(r.Prop.Query) {

		for name, metric := range r.Prop.Metrics {
			metr, ok := r.Matrix.GetMetrics()[name]
			if !ok {
				if metr, err = r.Matrix.NewMetricFloat64(name); err != nil {
					r.Logger.Error().Err(err).
						Str("name", name).
						Msg("NewMetricFloat64")
				}
			}
			metr.SetName(metric.Label)
			metr.SetExportable(metric.Exportable)
		}

		var service, wait, visits, ops matrix.Metric

		if service = r.Matrix.GetMetric("service_time"); service == nil {
			r.Logger.Error().Stack().Msg("metric [service_time] required to calculate workload missing")
		}

		if wait = r.Matrix.GetMetric("wait_time"); wait == nil {
			r.Logger.Error().Stack().Msg("metric [wait-time] required to calculate workload missing")
		}

		if visits = r.Matrix.GetMetric("visits"); visits == nil {
			r.Logger.Error().Stack().Msg("metric [visits] required to calculate workload missing")
		}

		if service == nil || wait == nil || visits == nil {
			return nil, errors.New(errors.MISSING_PARAM, "workload metrics")
		}

		if ops = r.Matrix.GetMetric("ops"); ops == nil {
			if ops, err = r.Matrix.NewMetricFloat64("ops"); err != nil {
				return nil, err
			}
			r.perfProp.counterInfo["ops"] = &counter{
				name:        "ops",
				description: "",
				counterType: r.perfProp.counterInfo[visits.GetName()].counterType,
				unit:        r.perfProp.counterInfo[visits.GetName()].unit,
				denominator: "",
			}
		}

		service.SetExportable(false)
		wait.SetExportable(false)
		visits.SetExportable(false)

		if resourceMap := r.Params.GetChildS("resource_map"); resourceMap == nil {
			return nil, errors.New(errors.MISSING_PARAM, "resource_map")
		} else {
			for _, x := range resourceMap.GetChildren() {
				name := x.GetNameS()
				resource := x.GetContentS()

				if m := r.Matrix.GetMetric(name); m != nil {
					continue
				}
				if m, err := r.Matrix.NewMetricFloat64(name); err != nil {
					return nil, err
				} else {
					r.perfProp.counterInfo[name] = &counter{
						name:        "resource_latency",
						description: "",
						counterType: r.perfProp.counterInfo[service.GetName()].counterType,
						unit:        r.perfProp.counterInfo[service.GetName()].unit,
						denominator: "ops",
					}
					m.SetName("resource_latency")
					m.SetLabel("resource", resource)

					r.Logger.Debug().Str("name", name).Str("resource", resource).Msg("added workload latency metric")
				}
			}
		}
	}
	return r.Matrix, nil
}

func (r *RestPerf) PollData() (*matrix.Matrix, error) {

	var (
		content         []byte
		count           uint64
		apiD, parseD    time.Duration
		startTime       time.Time
		err             error
		records         []interface{}
		instanceKeys    []string
		resourceLatency matrix.Metric // for workload* objects
	)

	r.Logger.Debug().Msg("updating data cache")

	// clone matrix without numeric data
	newData := r.Matrix.Clone(false, true, true)
	newData.Reset()
	timestamp := newData.GetMetric("timestamp")
	if timestamp == nil {
		return nil, errors.New(errors.ERR_CONFIG, "missing timestamp metric")
	}

	startTime = time.Now()

	dataQuery := path.Join(r.Prop.Query, "rows")

	href := rest.BuildHref(dataQuery, strings.Join(r.Prop.Fields, ","), nil, "", "", "", r.Prop.ReturnTimeOut, dataQuery)

	r.Logger.Debug().Str("href", href).Msg("")
	if href == "" {
		return nil, errors.New(errors.ERR_CONFIG, "empty url")
	}

	ts := float64(time.Now().UnixNano()) / BILLION
	err = rest.FetchData(r.Client, href, &records)
	if err != nil {
		r.Logger.Error().Stack().Err(err).Str("href", href).Msg("Failed to fetch data")
		return nil, err
	}

	all := rest.Pagination{
		Records:    records,
		NumRecords: len(records),
	}
	apiD = time.Since(startTime)

	content, err = json.Marshal(all)
	if err != nil {
		r.Logger.Error().Err(err).Str("ApiPath", r.Prop.Query).Msg("Unable to marshal rest pagination")
	}

	startTime = time.Now()
	if !gjson.ValidBytes(content) {
		return nil, fmt.Errorf("json is not valid for: %s", r.Prop.Query)
	}
	parseD = time.Since(startTime)

	results := gjson.GetManyBytes(content, "num_records", "records")
	numRecords := results[0]
	if numRecords.Int() == 0 {
		return nil, errors.New(errors.ERR_NO_INSTANCE, "no "+r.Object+" instances on cluster")
	}

	r.Logger.Debug().Str("object", r.Object).Str("records", numRecords.String()).Msg("Extracted records")

	if isWorkloadDetailObject(r.Prop.Query) {
		if resourceMap := r.Params.GetChildS("resource_map"); resourceMap == nil {
			return nil, errors.New(errors.MISSING_PARAM, "resource_map")
		} else {
			instanceKeys = make([]string, 0)
			for _, layer := range resourceMap.GetAllChildNamesS() {
				for key := range r.Matrix.GetInstances() {
					instanceKeys = append(instanceKeys, key+"."+layer)
				}
			}
		}
	}

	results[1].ForEach(func(key, instanceData gjson.Result) bool {
		var (
			instanceKey string
			instance    *matrix.Instance
		)

		if !instanceData.IsObject() {
			r.Logger.Warn().Str("type", instanceData.Type.String()).Msg("Instance data is not object, skipping")
			return true
		}

		// extract instance key(s)
		for _, k := range r.Prop.InstanceKeys {
			value := parseProperties(instanceData, k)
			if value.Exists() {
				instanceKey += value.String()
			} else {
				r.Logger.Warn().Str("key", k).Msg("skip instance, missing key")
				break
			}
		}

		// special case for these two objects
		// we need to process each latency layer for each instance/counter
		if isWorkloadDetailObject(r.Prop.Query) {

			layer := "" // latency layer (resource) for workloads

			before, after, found := strings.Cut(instanceKey, ".")
			if found {
				instanceKey = before
				layer = after
			} else {
				r.Logger.Warn().
					Str("key", instanceKey).
					Msg("Instance key has unexpected format")
				return true
			}

			if resourceLatency = newData.GetMetric(layer); resourceLatency == nil {
				r.Logger.Trace().
					Str("layer", layer).
					Msg("Resource-latency metric missing in cache")
				return true
			}
		}

		if r.Params.GetChildContentS("only_cluster_instance") != "true" {
			if instanceKey == "" {
				return true
			}
		}

		if isWorkloadObject(r.Prop.Query) || isWorkloadDetailObject(r.Prop.Query) {
			instance = newData.GetInstance(strings.Split(instanceKey, ":")[1])
		} else {
			instance = newData.GetInstance(instanceKey)
		}

		if instance == nil {
			if isWorkloadObject(r.Prop.Query) || isWorkloadDetailObject(r.Prop.Query) {
				r.Logger.Debug().
					Str("key", instanceKey).
					Msg("Skip instance key, not found in cache")
			} else {
				r.Logger.Warn().
					Str("key", instanceKey).
					Msg("Skip instance key, not found in cache")
			}
			return true
		}

		//// add batch timestamp as custom counter
		for label, display := range r.Prop.InstanceLabels {
			value := parseProperties(instanceData, label)
			if value.Exists() {
				if value.IsArray() {
					var labelArray []string
					for _, r := range value.Array() {
						labelString := r.String()
						labelArray = append(labelArray, labelString)
					}
					instance.SetLabel(display, strings.Join(labelArray, ","))
				} else {
					instance.SetLabel(display, value.String())
				}
				count++
			} else {
				// spams a lot currently due to missing label mappings. Moved to debug for now till rest gaps are filled
				r.Logger.Debug().Str("Instance key", instanceKey).Str("label", label).Msg("Missing label value")
			}
		}

		for name, metric := range r.Prop.Metrics {
			f := parseMetricResponse(instanceData, name)
			if f.value != "" {
				// special case for workload_detail
				if isWorkloadDetailObject(r.Prop.Query) {
					if name == "wait_time" || name == "service_time" {
						if err := resourceLatency.AddValueString(instance, f.value); err != nil {
							r.Logger.Error().
								Stack().
								Err(err).
								Str("name", name).
								Str("value", f.value).
								Msg("Add resource-latency failed")
						} else {
							r.Logger.Trace().
								Str("name", name).
								Str("value", f.value).
								Msg("Add resource-latency")
							count++
						}
						continue
					}
					// "visits" are ignored. This counter is only used to set properties of ops counter
					if name == "visits" {
						continue
					}
				} else {
					if f.isArray {
						labels := strings.Split(f.label, ",")
						values := strings.Split(f.value, ",")

						if len(labels) != len(values) {
							// warn & skip
							r.Logger.Warn().
								Stack().
								Str("labels", f.label).
								Str("value", f.value).
								Msg("labels don't match parsed values")
							continue
						}

						for i, label := range labels {
							k := name + "#" + label
							metr, ok := newData.GetMetrics()[k]
							if !ok {
								if metr, err = newData.NewMetricFloat64(k); err != nil {
									r.Logger.Error().Err(err).
										Str("name", k).
										Msg("NewMetricFloat64")
									continue
								}
								metr.SetName(metric.Label)
								metr.SetLabel("metric", label)
								// differentiate between array and normal counter
								metr.SetArray(true)
								metr.SetExportable(metric.Exportable)
							}
							if err = metr.SetValueString(instance, values[i]); err != nil {
								r.Logger.Error().
									Stack().
									Err(err).
									Str("name", name).
									Str("label", label).
									Str("value", values[i]).
									Msg("Set value failed")
								continue
							} else {
								r.Logger.Trace().
									Str("name", name).
									Str("label", label).
									Str("value", values[i]).
									Msg("Set name.label = value")
								count++
							}
						}
					} else {
						metr, ok := newData.GetMetrics()[name]
						if !ok {
							if metr, err = newData.NewMetricFloat64(name); err != nil {
								r.Logger.Error().Err(err).
									Str("name", name).
									Msg("NewMetricFloat64")
							}
						}
						metr.SetName(metric.Label)
						metr.SetExportable(metric.Exportable)
						if c, err := strconv.ParseFloat(f.value, 64); err == nil {
							if err = metr.SetValueFloat64(instance, c); err != nil {
								r.Logger.Error().Err(err).Str("key", metric.Name).Str("metric", metric.Label).
									Msg("Unable to set float key on metric")
							}
						} else {
							r.Logger.Error().Err(err).Str("key", metric.Name).Str("metric", metric.Label).
								Msg("Unable to parse float value")
						}
						count++
					}
				}
			} else {
				r.Logger.Warn().Str("counter", name).Msg("Counter is nil. Unable to process. Check template")
			}
		}
		if err = newData.GetMetric("timestamp").SetValueFloat64(instance, ts); err != nil {
			r.Logger.Error().Err(err).Msg("Failed to set timestamp")
		}
		return true
	})

	r.Logger.Debug().
		Uint64("dataPoints", count).
		Str("apiTime", apiD.String()).
		Str("parseTime", parseD.String()).
		Msg("Collected")

	if isWorkloadDetailObject(r.Prop.Query) {
		if err := r.getParentOpsCounters(newData); err != nil {
			// no point to continue as we can't calculate the other counters
			return nil, err
		}
	}

	_ = r.Metadata.LazySetValueInt64("api_time", "data", apiD.Microseconds())
	_ = r.Metadata.LazySetValueInt64("parse_time", "data", parseD.Microseconds())
	_ = r.Metadata.LazySetValueUint64("count", "data", count)
	r.AddCollectCount(count)

	// skip calculating from delta if no data from previous poll
	if r.perfProp.isCacheEmpty {
		r.Logger.Debug().Msg("skip postprocessing until next poll (previous cache empty)")
		r.Matrix = newData
		r.perfProp.isCacheEmpty = false
		return nil, nil
	}

	calcStart := time.Now()

	r.Logger.Debug().Msg("starting delta calculations from previous cache")

	// cache raw data for next poll
	cachedData := newData.Clone(true, true, true)

	orderedNonDenominatorMetrics := make([]matrix.Metric, 0, len(newData.GetMetrics()))
	orderedNonDenominatorKeys := make([]string, 0, len(orderedNonDenominatorMetrics))

	orderedDenominatorMetrics := make([]matrix.Metric, 0, len(newData.GetMetrics()))
	orderedDenominatorKeys := make([]string, 0, len(orderedDenominatorMetrics))

	for key, metric := range newData.GetMetrics() {
		if metric.GetName() != "timestamp" {
			counter := r.counterLookup(metric, key)
			if counter != nil {
				if !isWorkloadDetailObject(r.Prop.Query) {
					// set metric unit
					metric.SetLabel("unit", counter.unit)
				}
				if counter.denominator == "" {
					// does not require base counter
					orderedNonDenominatorMetrics = append(orderedNonDenominatorMetrics, metric)
					orderedNonDenominatorKeys = append(orderedNonDenominatorKeys, key)
				} else {
					// does require base counter
					orderedDenominatorMetrics = append(orderedDenominatorMetrics, metric)
					orderedDenominatorKeys = append(orderedDenominatorKeys, key)
				}
			} else {
				r.Logger.Warn().Str("counter", metric.GetName()).Msg("Counter is nil. Unable to process. Check template")
			}
		}
	}

	// order metrics, such that those requiring base counters are processed last
	orderedMetrics := append(orderedNonDenominatorMetrics, orderedDenominatorMetrics...)
	orderedKeys := append(orderedNonDenominatorKeys, orderedDenominatorKeys...)

	// calculate timestamp delta first since many counters require it for postprocessing.
	// Timestamp has "raw" property, so it isn't post-processed automatically
	if err = timestamp.Delta(r.Matrix.GetMetric("timestamp")); err != nil {
		r.Logger.Error().Err(err).Msg("(timestamp) calculate delta:")
	}

	var base matrix.Metric

	for i, metric := range orderedMetrics {
		key := orderedKeys[i]
		counter := r.counterLookup(metric, key)
		if counter == nil {
			r.Logger.Error().Stack().Err(err).Str("counter", metric.GetName()).Msg("Missing counter:")
			continue
		}
		property := counter.counterType

		// RAW - submit without post-processing
		if property == "raw" {
			continue
		}

		// all other properties - first calculate delta
		if err = metric.Delta(r.Matrix.GetMetric(key)); err != nil {
			r.Logger.Error().Stack().Err(err).Str("key", key).Msg("Calculate delta")
			continue
		}

		// DELTA - subtract previous value from current
		if property == "delta" {
			// already done
			continue
		}

		// RATE - delta, normalized by elapsed time
		if property == "rate" {
			// defer calculation, so we can first calculate averages/percents
			// Note: calculating rate before averages are averages/percentages are calculated
			// used to be a bug in Harvest 2.0 (Alpha, RC1, RC2) resulting in very high latency values
			continue
		}

		// For the next two properties we need base counters
		// We assume that delta of base counters is already calculated
		// (name of base counter is stored as Comment)
		if base = newData.GetMetric(counter.denominator); base == nil {
			r.Logger.Warn().
				Str("key", key).
				Str("property", property).
				Str("denominator", counter.denominator).
				Msg("Base counter missing")
			continue
		}

		// remaining properties: average and percent
		//
		// AVERAGE - delta, divided by base-counter delta
		//
		// PERCENT - average * 100
		// special case for latency counter: apply minimum number of iops as threshold
		if property == "average" || property == "percent" {

			if strings.HasSuffix(metric.GetName(), "latency") {
				err = metric.DivideWithThreshold(base, r.perfProp.latencyIoReqd)
			} else {
				err = metric.Divide(base)
			}

			if err != nil {
				r.Logger.Error().Stack().Err(err).Str("key", key).Msg("Division by base")
				continue
			}

			if property == "average" {
				continue
			}
		}

		if property == "percent" {
			if err = metric.MultiplyByScalar(100); err != nil {
				r.Logger.Error().Stack().Err(err).Str("key", key).Msg("Multiply by scalar")
			}
			continue
		}
		// If we reach here then one of the earlier clauses should have executed `continue` statement
		r.Logger.Error().Stack().Err(err).
			Str("key", key).
			Str("property", property).
			Msg("Unknown property")
	}

	// calculate rates (which we deferred to calculate averages/percents first)
	for i, metric := range orderedMetrics {
		key := orderedKeys[i]
		counter := r.counterLookup(metric, key)
		if counter != nil {
			property := counter.counterType
			if property == "rate" {
				if err = metric.Divide(timestamp); err != nil {
					r.Logger.Error().Stack().Err(err).
						Int("i", i).
						Str("metric", metric.GetName()).
						Str("key", orderedKeys[i]).
						Msg("Calculate rate")
					continue
				}
			}
		} else {
			r.Logger.Warn().Str("counter", metric.GetName()).Msg("Counter is nil. Unable to process. Check template ")
			continue
		}
	}

	_ = r.Metadata.LazySetValueInt64("calc_time", "data", time.Since(calcStart).Microseconds())
	// store cache for next poll
	r.Matrix = cachedData

	return newData, nil
}

// Poll counter "ops" of the related/parent object, required for objects
// workload_detail and workload_detail_volume. This counter is already
// collected by the other collectors, so this poll is redundant
// (until we implement some sort of inter-collector communication).
func (r *RestPerf) getParentOpsCounters(data *matrix.Matrix) error {

	var (
		ops    matrix.Metric
		object string
		//instanceKeys []string
		dataQuery string
		err       error
		records   []interface{}
		content   []byte
	)

	if r.Prop.Query == qosDetailQuery {
		dataQuery = path.Join(qosQuery, "rows")
		object = "qos"
	} else {
		dataQuery = path.Join(qosVolumeQuery, "rows")
		object = "qos_volume"
	}

	if ops = data.GetMetric("ops"); ops == nil {
		r.Logger.Error().Stack().Err(nil).Msgf("ops counter not found in cache")
		return errors.New(errors.MISSING_PARAM, "counter ops")
	}

	//instanceKeys = data.GetInstanceKeys()

	var filter []string
	filter = append(filter, "counters.name=ops")
	href := rest.BuildHref(dataQuery, "*", filter, "", "", "", r.Prop.ReturnTimeOut, dataQuery)

	r.Logger.Debug().Str("href", href).Msg("")
	if href == "" {
		return errors.New(errors.ERR_CONFIG, "empty url")
	}

	err = rest.FetchData(r.Client, href, &records)
	if err != nil {
		r.Logger.Error().Stack().Err(err).Str("href", href).Msg("Failed to fetch data")
		return err
	}

	all := rest.Pagination{
		Records:    records,
		NumRecords: len(records),
	}

	content, err = json.Marshal(all)
	if err != nil {
		r.Logger.Error().Err(err).Str("ApiPath", dataQuery).Msg("Unable to marshal rest pagination")
	}

	if !gjson.ValidBytes(content) {
		return fmt.Errorf("json is not valid for: %s", dataQuery)
	}

	results := gjson.GetManyBytes(content, "num_records", "records")
	numRecords := results[0]
	if numRecords.Int() == 0 {
		return errors.New(errors.ERR_NO_INSTANCE, "no "+object+" instances on cluster")
	}

	r.Logger.Debug().Str("object", r.Object).Str("records", numRecords.String()).Msg("Extracted records")

	results[1].ForEach(func(key, instanceData gjson.Result) bool {
		var (
			instanceKey string
			instance    *matrix.Instance
		)

		if !instanceData.IsObject() {
			r.Logger.Warn().Str("type", instanceData.Type.String()).Msg("Instance data is not object, skipping")
			return true
		}

		value := parseProperties(instanceData, "name")
		if value.Exists() {
			instanceKey += value.String()
		} else {
			r.Logger.Warn().Str("key", "name").Msg("skip instance, missing key")
			return true
		}
		instance = data.GetInstance(instanceKey)
		if instance == nil {
			r.Logger.Trace().Str("key", key.String()).Msg("skip instance not found in cache")
			return true
		}

		counterName := "ops"
		f := parseMetricResponse(instanceData, counterName)
		if f.value != "" {
			if err = ops.SetValueString(instance, f.value); err != nil {
				r.Logger.Error().Stack().Err(err).Str("metric", counterName).Str("value", value.String()).Msg("set metric")
			} else {
				r.Logger.Trace().Msgf("+ metric (%s) = [%s%s%s]", counterName, color.Cyan, value, color.End)
			}
		}
		return true
	})
	return nil
}

func (r *RestPerf) counterLookup(metric matrix.Metric, metricKey string) *counter {
	var c *counter

	if metric.IsArray() {
		lastInd := strings.LastIndex(metricKey, "#")
		name := metricKey[:lastInd]
		c = r.perfProp.counterInfo[name]
	} else {
		c = r.perfProp.counterInfo[metricKey]
	}
	return c
}

func (r *RestPerf) LoadPlugin(kind string, abc *plugin.AbstractPlugin) plugin.Plugin {
	switch kind {
	default:
		r.Logger.Warn().Str("kind", kind).Msg("no rest performance plugin found ")
	}
	return nil
}

// PollInstance updates instance cache
func (r *RestPerf) PollInstance() (*matrix.Matrix, error) {

	var (
		err                              error
		oldInstances                     *set.Set
		oldSize, newSize, removed, added int
		records                          []interface{}
		content                          []byte
	)

	oldInstances = set.New()
	for key := range r.Matrix.GetInstances() {
		oldInstances.Add(key)
	}
	oldSize = oldInstances.Size()

	dataQuery := path.Join(r.Prop.Query, "rows")
	instanceKeys := r.Prop.InstanceKeys
	fields := "properties"
	fields = "*"
	var filter []string

	if isWorkloadObject(r.Prop.Query) || isWorkloadDetailObject(r.Prop.Query) {
		dataQuery = qosWorkloadQuery
		if isWorkloadObject(r.Prop.Query) {
			instanceKeys = []string{"uuid"}
		}
		if isWorkloadDetailObject(r.Prop.Query) {
			instanceKeys = []string{"name"}
		}
		if r.Prop.Query == qosVolumeQuery || r.Prop.Query == qosDetailVolumeQuery {
			filter = append(filter, "workload-class=autovolume")
		} else {
			filter = append(filter, "workload-class=user_defined")
		}
	}

	href := rest.BuildHref(dataQuery, fields, filter, "", "", "", r.Prop.ReturnTimeOut, dataQuery)

	r.Logger.Debug().Str("href", href).Msg("")
	if href == "" {
		return nil, errors.New(errors.ERR_CONFIG, "empty url")
	}

	err = rest.FetchData(r.Client, href, &records)
	if err != nil {
		r.Logger.Error().Stack().Err(err).Str("href", href).Msg("Failed to fetch data")
		return nil, err
	}

	all := rest.Pagination{
		Records:    records,
		NumRecords: len(records),
	}

	content, err = json.Marshal(all)
	if err != nil {
		r.Logger.Error().Err(err).Str("ApiPath", r.Prop.Query).Msg("Unable to marshal rest pagination")
	}

	if !gjson.ValidBytes(content) {
		return nil, fmt.Errorf("json is not valid for: %s", r.Prop.Query)
	}

	results := gjson.GetManyBytes(content, "num_records", "records")
	numRecords := results[0]
	if numRecords.Int() == 0 {
		return nil, errors.New(errors.ERR_NO_INSTANCE, "no "+r.Object+" instances on cluster")
	}

	r.Logger.Debug().Str("object", r.Object).Str("records", numRecords.String()).Msg("Extracted records")

	results[1].ForEach(func(key, instanceData gjson.Result) bool {
		var (
			instanceKey string
		)

		if !instanceData.IsObject() {
			r.Logger.Warn().Str("type", instanceData.Type.String()).Msg("Instance data is not object, skipping")
			return true
		}

		// extract instance key(s)
		for _, k := range instanceKeys {
			var value gjson.Result
			if isWorkloadObject(r.Prop.Query) || isWorkloadDetailObject(r.Prop.Query) {
				value = instanceData.Get(k)
			} else {
				value = parseProperties(instanceData, k)
			}
			if value.Exists() {
				instanceKey += value.String()
			} else {
				r.Logger.Warn().Str("key", k).Msg("skip instance, missing key")
				break
			}
		}

		if oldInstances.Delete(instanceKey) {
			// instance already in cache
			r.Logger.Debug().Msgf("updated instance [%s%s%s%s]", color.Bold, color.Yellow, key, color.End)
		} else if instance, err := r.Matrix.NewInstance(instanceKey); err != nil {
			r.Logger.Error().Err(err).Str("Instance key", instanceKey).Msg("add instance")
		} else {
			r.Logger.Debug().
				Str("key", instanceKey).
				Msg("Added new instance")
			if isWorkloadObject(r.Prop.Query) || isWorkloadDetailObject(r.Prop.Query) {
				for label, display := range r.perfProp.qosLabels {
					if value := instanceData.Get(label); value.Exists() {
						instance.SetLabel(display, value.String())
					} else {
						r.Logger.Warn().Str("label", label).Str("instanceKey", instanceKey).Msgf("Missing label")

					}
				}
				r.Logger.Debug().Str("query", r.Prop.Query).Str("key", key.String()).Str("qos labels", instance.GetLabels().String()).Msg("")
			}
		}
		return true
	})

	for key := range oldInstances.Iter() {
		r.Matrix.RemoveInstance(key)
		r.Logger.Debug().Msgf("removed instance [%s]", key)
	}

	removed = oldInstances.Size()
	newSize = len(r.Matrix.GetInstances())
	added = newSize - (oldSize - removed)

	r.Logger.Debug().Msgf("added %d new, removed %d (total instances %d)", added, removed, newSize)

	if newSize == 0 {
		return nil, errors.New(errors.ERR_NO_INSTANCE, "")
	}

	return nil, err
}

func isWorkloadObject(query string) bool {
	_, ok := qosQueries[query]
	return ok
}

func isWorkloadDetailObject(query string) bool {
	_, ok := qosDetailQueries[query]
	return ok
}

// Interface guards
var (
	_ collector.Collector = (*RestPerf)(nil)
)
