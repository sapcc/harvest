package harvest

Exporters: [Name=_]: #Prom | #Influx

#ExporterDefs: string | #Prom | #Influx

label: [string]: string

#Auth: {
	username: string
	password: string
}

#HTTPSD: {
	listen:        string
	auth_basic?:   #Auth
	tls?:          #TLS
	heart_beat?:   string
	expire_after?: string
}

#TLS: {
	cert_file: string
	key_file:  string
}

#Admin: {
	addr?:   string
	httpsd?: #HTTPSD
}

#Prom: {
	add_meta_tags?: bool
	addr?:          string // deprecated
	allow_addrs_regex?: [...string]
	exporter:         "Prometheus"
	local_http_addr?: "0.0.0.0" | "localhost" | "127.0.0.1"
	port?:            int
	port_range?:      string
	sort_labels?:     bool
	tls?:             #TLS
}

#Influx: {
	addr?: string // one of addr|url
	allow_addrs_regex: [...string]
	bucket?:  string
	exporter: "InfluxDB"
	org?:     string
	token?:   string
	url?:     string
}

#CertificateScript: {
	path:     string
	timeout?: string
}

#CredentialsScript: {
	path:      string
	schedule?: string
	timeout?:  string
}

#Recorder: {
	path: string
	mode: "record" | "replay"
	keep_last?: int
}

#CollectorDef: {
	[Name=_]: [...string]
}

Pollers: [Name=_]: #Poller

#Poller: {
	addr?:               string
	auth_style?:         "basic_auth" | "certificate_auth"
	ca_cert?:            string
	certificate_script?: #CertificateScript
	client_timeout?:     string
	collectors?:         [...#CollectorDef] | [...string]
	conf_path?:          string
	credentials_file?:   string
	credentials_script?: #CredentialsScript
	datacenter?:         string
	disabled?:           bool
	exporters:           [...#ExporterDefs]
	is_kfs?:             bool
	labels?:             [...label]
	log:                 [...string]
	log_max_bytes?:      int
	log_max_files?:      int
	password?:           string
	poller_log_schedule?: string
	prefer_zapi?:        bool
	prom_port?:          int
	recorder?:           #Recorder
	ssl_cert?:           string
	ssl_key?:            string
	tls_min_version?:    string
	use_insecure_tls?:   bool
	username?:           string
}
