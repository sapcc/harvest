package collector

import (
	"path"
	"regexp"
    "strings"
    "strconv"
    "errors"
	"io/ioutil"
	"goharvest2/poller/struct/yaml"
)

func ImportTemplate(harvest_path, collector_name string) (*yaml.Node, error) {
    fp := path.Join(harvest_path, "config/", strings.ToLower(collector_name), "default.yaml")
	return yaml.Import(fp)
}

func ImportSubTemplate(harvest_path, dirname, filename, collector string, version [3]int) (*yaml.Node, error) {

    var err error
    var selected_version string
    var template *yaml.Node

    path_prefix := path.Join(harvest_path, "config/", strings.ToLower(collector), dirname, "cdot")
    //Log.Debug("Looking for best-fitting template in [%s]", path_prefix)

    available := make(map[string]bool)
    files, _ := ioutil.ReadDir(path_prefix)
    for _, file := range files {
        //Log.Debug("Found version dir: [%s]", file.Name())
        if match, _ := regexp.MatchString(`\d+\.\d+\.\d+`, file.Name()); match == true && file.IsDir() {
            available[file.Name()] = true
        }
    }

    vers := version[0] * 100 + version[1] * 10 + version[2]

    for max:=300; max>0 && vers>0; max-=1 {
        str := strings.Join(strings.Split(strconv.Itoa(vers), ""), ".")
        if _, exists := available[str]; exists == true {
            selected_version = str
            break
        }
        vers -= 1
    }

    if selected_version == "" {
        //Log.Debug("looking for newer version")

        vers = version[0] * 100 + version[1] * 10 + version[2]

        for max:=300; max>0 && vers>0; max-=1 {
            str := strings.Join(strings.Split(strconv.Itoa(vers), ""), ".")
            if _, exists := available[str]; exists == true {
                selected_version = str
                break
            }
            vers += 1
        }
    }

    if selected_version == "" {
        err = errors.New("No best-fitting subtemplate version found")
    } else {
        template_path := path.Join(path_prefix, selected_version, filename)
        //Log.Info("Selected best-fitting subtemplate [%s]", template_path)
        template, err = yaml.Import(template_path)
    }
    return template, err
}
