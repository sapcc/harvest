package main

import (
	"errors"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/netapp/harvest/v2/pkg/auth"
	"github.com/netapp/harvest/v2/pkg/conf"
	"github.com/netapp/harvest/v2/pkg/tree/node"
	"strings"
	"testing"
)

func TestPingParsing(t *testing.T) {
	poller := Poller{}

	type test struct {
		name string
		out  string
		ping float32
		isOK bool
	}

	tests := []test{
		{
			name: "NotBusy",
			ping: 0.032,
			isOK: true,
			out: `PING 127.0.0.1 (127.0.0.1) 56(84) bytes of data.

	--- 127.0.0.1 ping statistics ---
	1 packets transmitted, 1 received, 0% packet loss, time 0ms
	rtt min/avg/max/mdev = 0.032/0.032/0.032/0.000 ms`,
		},
		{
			name: "BusyBox",
			ping: 0.088,
			isOK: true,
			out: `PING 127.0.0.1 (127.0.0.1): 56 data bytes

--- 127.0.0.1 ping statistics ---
1 packets transmitted, 1 packets received, 0% packet loss
round-trip min/avg/max = 0.088/0.088/0.088 ms`,
		},
		{
			name: "BadInput",
			ping: 0,
			isOK: false,
			out:  `foo`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ping, b := poller.parsePing(tt.out)
			if ping != tt.ping {
				t.Errorf("parsePing ping got = %v, want %v", ping, tt.ping)
			}
			if b != tt.isOK {
				t.Errorf("parsePing isOK got = %v, want %v", b, tt.isOK)
			}
		})
	}
}

func TestUnion2(t *testing.T) {
	configPath := "../../cmd/tools/doctor/testdata/testConfig.yml"
	n := node.NewS("foople")
	conf.TestLoadHarvestConfig(configPath)
	p, err := conf.PollerNamed("infinity2")
	if err != nil {
		panic(err)
	}
	Union2(n, p)
	labels := n.GetChildS("labels")
	if labels == nil {
		t.Fatal("got nil, want labels")
	}
	type label struct {
		key string
		val string
	}
	wants := []label{
		{key: "org", val: "abc"},
		{key: "site", val: "RTP"},
		{key: "floor", val: "3"},
	}
	for i, c := range labels.Children {
		want := wants[i]
		if want.key != c.GetNameS() {
			t.Errorf("got key=%s, want=%s", c.GetNameS(), want.key)
		}
		if want.val != c.GetContentS() {
			t.Errorf("got key=%s, want=%s", c.GetContentS(), want.val)
		}
	}
}

func TestPublishUrl(t *testing.T) {
	poller := Poller{}

	type test struct {
		name   string
		isTLS  bool
		listen string
		want   string
	}

	tests := []test{
		{name: "localhost", isTLS: false, listen: "localhost:8118", want: "http://localhost:8118/api/v1/sd"},
		{name: "all interfaces", isTLS: false, listen: ":8118", want: "http://127.0.0.1:8118/api/v1/sd"},
		{name: "ip", isTLS: false, listen: "10.0.1.1:8118", want: "http://10.0.1.1:8118/api/v1/sd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf.Config.Admin.Httpsd = conf.Httpsd{}
			if tt.isTLS {
				conf.Config.Admin.Httpsd.TLS = conf.TLS{
					CertFile: "a",
					KeyFile:  "a",
				}
			}
			conf.Config.Admin.Httpsd.Listen = tt.listen
			got := poller.makePublishURL()
			if got != tt.want {
				t.Errorf("makePublishURL got = [%v] want [%v]", got, tt.want)
			}
		})
	}
}

func TestCollectorUpgrade(t *testing.T) {
	poller := Poller{params: &conf.Poller{}}

	type test struct {
		name          string
		askFor        string
		wantCollector string
		remote        conf.Remote
	}

	ontap911 := conf.Remote{Version: "9.11.1", ZAPIsExist: true}
	ontap917 := conf.Remote{Version: "9.17.1", ZAPIsExist: false}
	asaR2 := conf.Remote{Version: "9.16.1", ZAPIsExist: false, IsDisaggregated: true, IsSanOptimized: true}
	keyPerf := conf.Remote{Version: "9.17.1", ZAPIsExist: false, IsDisaggregated: true}
	keyPerfWithZapi := conf.Remote{Version: "9.17.1", ZAPIsExist: true, IsDisaggregated: true}

	tests := []test{
		{name: "9.11 w/ ZAPI", remote: ontap911, askFor: "Zapi", wantCollector: "Zapi"},
		{name: "9.11 w/ ZAPI", remote: ontap911, askFor: "ZapiPerf", wantCollector: "ZapiPerf"},
		{name: "9.11 w/ ZAPI", remote: ontap911, askFor: "Rest", wantCollector: "Rest"},
		{name: "9.11 w/ ZAPI", remote: ontap911, askFor: "KeyPerf", wantCollector: "KeyPerf"},

		{name: "9.17 no ZAPI", remote: ontap917, askFor: "Zapi", wantCollector: "Rest"},
		{name: "9.17 no ZAPI", remote: ontap917, askFor: "ZapiPerf", wantCollector: "RestPerf"},
		{name: "9.17 no ZAPI", remote: ontap917, askFor: "KeyPerf", wantCollector: "KeyPerf"},

		{name: "KeyPerf", remote: keyPerf, askFor: "Zapi", wantCollector: "Rest"},
		{name: "KeyPerf", remote: keyPerf, askFor: "Rest", wantCollector: "Rest"},
		{name: "KeyPerf", remote: keyPerf, askFor: "ZapiPerf", wantCollector: "KeyPerf"},
		{name: "KeyPerf", remote: keyPerf, askFor: "RestPerf", wantCollector: "KeyPerf"},

		{name: "KeyPerf w/ ZAPI", remote: keyPerfWithZapi, askFor: "Zapi", wantCollector: "Zapi"},
		{name: "KeyPerf w/ ZAPI", remote: keyPerfWithZapi, askFor: "ZapiPerf", wantCollector: "KeyPerf"},
		{name: "KeyPerf w/ ZAPI", remote: keyPerfWithZapi, askFor: "RestPerf", wantCollector: "KeyPerf"},

		{name: "ASA R2", remote: asaR2, askFor: "Zapi", wantCollector: "Rest"},
		{name: "ASA R2", remote: asaR2, askFor: "RestPerf", wantCollector: "KeyPerf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := conf.Collector{
				Name: tt.askFor,
			}

			newCollector := poller.upgradeCollector(collector, tt.remote)
			if newCollector.Name != tt.wantCollector {
				t.Errorf("got = [%s] want [%s]", newCollector.Name, tt.wantCollector)
			}
		})
	}
}

func Test_nonOverlappingCollectors(t *testing.T) {
	tests := []struct {
		name string
		args []objectCollector
		want []objectCollector
	}{
		{name: "empty", args: make([]objectCollector, 0), want: make([]objectCollector, 0)},
		{name: "one", args: ocs("Rest"), want: ocs("Rest")},
		{name: "no overlap", args: ocs("Rest", "ZapiPerf"), want: ocs("Rest", "ZapiPerf")},
		{name: "w overlap1", args: ocs("Rest", "Zapi"), want: ocs("Rest")},
		{name: "w overlap2", args: ocs("Zapi", "Rest"), want: ocs("Zapi")},
		{name: "w overlap3",
			args: ocs("Zapi", "Rest", "Rest", "Rest", "Rest", "Rest", "Zapi", "Zapi", "Zapi", "Zapi", "Zapi"),
			want: ocs("Zapi")},
		{name: "non ontap", args: ocs("Rest", "SG"), want: ocs("Rest", "SG")},
		{name: "no overlap", args: ocs("Rest", "KeyPerf"), want: ocs("Rest", "KeyPerf")},
		{name: "overlap", args: ocs("RestPerf", "KeyPerf"), want: ocs("RestPerf")},
		{name: "overlap", args: ocs("KeyPerf", "KeyPerf"), want: ocs("KeyPerf")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nonOverlappingCollectors(tt.args)
			diff := cmp.Diff(got, tt.want, cmp.AllowUnexported(objectCollector{}))
			if diff != "" {
				t.Errorf("Mismatch (-got +want):\n%s", diff)
			}
		})
	}
}

func ocs(names ...string) []objectCollector {
	collectors := make([]objectCollector, 0, len(names))
	for _, n := range names {
		collectors = append(collectors, objectCollector{class: n})
	}
	return collectors
}

func Test_uniquifyObjectCollectors(t *testing.T) {
	tests := []struct {
		name string
		args map[string][]objectCollector
		want []objectCollector
	}{
		{name: "empty", args: make(map[string][]objectCollector), want: []objectCollector{}},
		{name: "volume-rest", args: objectCollectorMap("Volume: Rest, Zapi"), want: []objectCollector{{class: "Rest", object: "Volume"}}},
		{name: "qtree-rest", args: objectCollectorMap("Qtree: Rest, Zapi"), want: []objectCollector{{class: "Rest", object: "Qtree"}}},
		{name: "qtree-zapi", args: objectCollectorMap("Qtree: Zapi, Rest"), want: []objectCollector{{class: "Zapi", object: "Qtree"}}},
		{name: "qtree-rest-quota", args: objectCollectorMap("Qtree: Rest, Zapi", "Quota: Rest"),
			want: []objectCollector{{class: "Rest", object: "Qtree"}, {class: "Rest", object: "Quota"}}},
		{name: "qtree-zapi-disable-quota", args: objectCollectorMap("Qtree: Zapi, Rest", "Quota: Rest"),
			want: []objectCollector{{class: "Zapi", object: "Qtree"}}},
		{name: "volume-restperf", args: objectCollectorMap("Volume: RestPerf, KeyPerf"),
			want: []objectCollector{{class: "RestPerf", object: "Volume"}}},
		{name: "volume-keyperf", args: objectCollectorMap("Volume: KeyPerf, RestPerf"),
			want: []objectCollector{{class: "KeyPerf", object: "Volume"}}},
		{name: "multi-keyperf", args: objectCollectorMap("Volume: RestPerf", "Aggregate: KeyPerf"),
			want: []objectCollector{{class: "RestPerf", object: "Volume"}, {class: "KeyPerf", object: "Aggregate"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uniquifyObjectCollectors(tt.args)

			diff := cmp.Diff(got, tt.want, cmp.AllowUnexported(objectCollector{}), cmpopts.SortSlices(func(a, b objectCollector) bool {
				return a.class+a.object < b.class+b.object
			}))

			if diff != "" {
				t.Errorf("Mismatch (-got +want):\n%s", diff)
			}
		})
	}
}

func objectCollectorMap(constructors ...string) map[string][]objectCollector {
	objectsToCollectors := make(map[string][]objectCollector)

	for _, template := range constructors {
		before, after, _ := strings.Cut(template, ":")
		object := before
		classes := strings.Split(after, ",")
		for _, class := range classes {
			class := strings.TrimSpace(class)
			objectsToCollectors[object] = append(objectsToCollectors[object], objectCollector{class: class, object: object})
		}
	}

	return objectsToCollectors
}

func TestNegotiateONTAPAPI(t *testing.T) {

	tests := []struct {
		name           string
		collectors     []conf.Collector
		mockReturn     conf.Remote
		mockError      error
		expectedRemote conf.Remote
	}{
		{
			name: "No ONTAP Collector",
			collectors: []conf.Collector{
				{Name: "StorageGrid"},
			},
			mockReturn:     conf.Remote{},
			mockError:      nil,
			expectedRemote: conf.Remote{},
		},
		{
			name: "ONTAP Collector with Success",
			collectors: []conf.Collector{
				{Name: "Zapi"},
			},
			mockReturn:     conf.Remote{Version: "9.11.1"},
			mockError:      nil,
			expectedRemote: conf.Remote{Version: "9.11.1"},
		},
		{
			name: "ONTAP Collector with Error",
			collectors: []conf.Collector{
				{Name: "Zapi"},
			},
			mockReturn:     conf.Remote{Version: "9.11.1"},
			mockError:      errors.New("failed to gather cluster info"),
			expectedRemote: conf.Remote{Version: "9.11.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockGatherClusterInfo := func(_ string, _ *auth.Credentials) (conf.Remote, error) {
				return tt.mockReturn, tt.mockError
			}
			poller := Poller{}

			poller.negotiateONTAPAPI(tt.collectors, mockGatherClusterInfo)

			if diff := cmp.Diff(poller.remote, tt.expectedRemote); diff != "" {
				t.Errorf("negotiateONTAPAPI() mismatch (-got +want):\n%s", diff)
			}
		})
	}
}
