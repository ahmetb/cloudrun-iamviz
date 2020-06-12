/*
Copyright © 2020 Ahmet Alp Balkan

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	runv1 "google.golang.org/api/run/v1"
)

const runInvokerRole = "roles/run.invoker"

var rootCmd = &cobra.Command{
	Use:   "cloudrun-iamviz",
	Short: "Visualize service permissions for Cloud Run apps.",
	RunE:  do,
}

func init() {
	log.SetOutput(os.Stderr)
	// add flags here
	//rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

func main() {
	if _, err := exec.LookPath("dot"); err != nil {
		log.Fatal("`dot` not installed on this machine")
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func do(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	project, err := inferGCPProject()
	if err != nil {
		return errors.Wrap(err, "failed to find project")
	}
	log.Printf("project=%s", project)

	client, err := runv1.NewService(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to initialize client")
	}

	regions, err := getCloudRunRegions(ctx, client, project)
	if err != nil {
		return errors.Wrap(err, "failed to query cloud run regions")
	}

	for _, r := range regions {
		log.Printf("--> %s", r)
	}

	regionToServicesMap := make(map[*runv1.Location][]*runv1.Service)
	servicePermissions := make(map[ServiceRecord][]string)

	log.Printf("PROJECTS:")
	svcs, err := getCloudRunServices(ctx, project, regions)
	if err != nil {
		return errors.Wrap(err, "failed to query services in all regions")
	}
	for _, s := range svcs {
		regionToServicesMap[s.Region] = append(regionToServicesMap[s.Region], s.Service)

		log.Printf("\t name=%s region=%s acct=%s", s.Metadata.Name, s.Metadata.Namespace, s.Spec.Template.Spec.ServiceAccountName)

		callers, err := queryPermissionsForSvc(client, s)
		if err != nil {
			return errors.Wrapf(err, "failed to query permissions for service %q in %q", s.Metadata.Name, s.Region.LocationId)
		}
		servicePermissions[s] = callers
		for _, c := range callers {
			log.Printf("\t\tauthorized caller: %s", c)
		}
	}


	var b bytes.Buffer
	mw := io.MultiWriter(&b, os.Stdout)
	render(mw, regionToServicesMap, servicePermissions)

	// convert to svg
	// TODO the following /dev devices won't work on windows
	cmd := exec.Command("dot", "-Tsvg", "-o", "/dev/stdout", "/dev/stdin")
	var stderr bytes.Buffer
	cmd.Stdin = &b
	cmd.Stderr = &stderr

	svgout, err := cmd.Output()
	if err != nil {
		return errors.Wrapf(err, "failed to convert svg, dot error output:\n%s", string(stderr.Bytes()))
	}
	log.Printf("converted to svg successfully")

	fp := filepath.Join(os.TempDir(),"iamviz.svg") // TODO add random name
	if err := ioutil.WriteFile(fp, svgout, 0644); err != nil {
		return errors.Wrap(err, "failed to write to temp file")
	}

	log.Printf("written file to: %s", fp)
	log.Printf("launching in browser...")
	url := "file://"+filepath.ToSlash(fp)
	return openInBrowser(url)
}

func render(out io.Writer,
	regionsToServices map[*runv1.Location][]*runv1.Service,
	permissionsMap map[ServiceRecord][]string) {


	svcAccountsToTargets := make(map[string][]ServiceRecord)
	for svc, callers := range permissionsMap {
		for _, c := range callers {
			svcAccountsToTargets[c] = append(svcAccountsToTargets[c], svc)
		}
	}

	p := func(f string, vals ...interface{}) { fmt.Fprintf(out, f+"\n", vals...) }
	regionName := func(s string) string { return "cluster_" + strings.ReplaceAll(s, "-", "_") }
	svcNode := func(s *runv1.Service, r string) string {
		return fmt.Sprintf(`%s_%s`, r, s.Metadata.Name)
	}

	p(`digraph G {`)
	//p(`  subgraph accounts {`)
	//p(`    rankdir="TB";`)
	//for acct := range svcAccounts {
	//	p(`"%s"[label = "%s",shape=box];`, acct, acct)
	//}
	//p(`  }`)

	for region, svcs := range regionsToServices {
			p("  subgraph %s {", regionName(region.LocationId))
			p("  style=dashed;")
			p("  node [style=filled,shape=box];")
			p(`  label = "%s (%s)";`, region.LocationId, region.DisplayName)
			for _, s := range svcs {
				svcURL := fmt.Sprintf("https://console.cloud.google.com/run/detail/%s/%s/revisions?project=%s",
					region.LocationId, s.Metadata.Name, s.Metadata.Namespace)

				nodeName := svcNode(s, region.LocationId)
				color := colorFor(s.Metadata.Name)
				p(`    "%s"[href="%s",color=%s,label = <%s<br/><font point-size='9'>%s</font>> ];`,
					nodeName, svcURL, color, s.Metadata.Name, s.Spec.Template.Spec.ServiceAccountName) // service node
			}
			p("  }")
	}

	for region, svcs := range regionsToServices {
		for _, s := range svcs {

			sa := s.Spec.Template.Spec.ServiceAccountName
			targets := svcAccountsToTargets[sa]

			for _, target := range targets {
				permissionsURL := fmt.Sprintf("https://console.cloud.google.com/run/detail/%s/%s/permissions?project=%s",
					target.Region.LocationId, target.Metadata.Name, target.Metadata.Namespace)

				p(`"%s" -> "%s" [href="%s"];`,
					svcNode(s, region.LocationId),
					svcNode(target.Service, target.Region.LocationId), permissionsURL)
			}
		}
	}
	//for svc, callers := range permissionsMap {
	//	nodeName := svcNode(svc.Service, svc.Region.LocationId)
	//
	//	for _, acct := range callers {
	//		p(`"%s" -> "%s" [label ="can invoke",color=red];`, acct, nodeName)
	//	}
	//}
	p("}")
}

type ServiceRecord struct {
	*runv1.Service
	Region *runv1.Location
}

func inferGCPProject() (string, error) {
	if v := os.Getenv("GOOGLE_CLOUD_PROJECT"); v != "" {
		return v, nil
	}

	b, err := exec.Command("gcloud", "config", "get-value", "core/project", "-q").Output()
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(b)), nil
}

func getCloudRunRegions(ctx context.Context, client *runv1.APIService, project string) ([]*runv1.Location, error) {
	var out []*runv1.Location
	err := client.Projects.Locations.List("projects/"+project).
		Pages(ctx, func(resp *runv1.ListLocationsResponse) error {
			for _, v := range resp.Locations {
				out = append(out, v)
			}
			return nil
		})
	return out, err
}

func getCloudRunServices(ctx context.Context, project string, regions []*runv1.Location) ([]ServiceRecord, error) {
	cctx, cancel := context.WithCancel(ctx)

	var (
		mu  sync.Mutex
		out []ServiceRecord

		retErr error

		wg sync.WaitGroup
	)

	for _, region := range regions {
		wg.Add(1)

		go func(r *runv1.Location) {
			defer wg.Done()

			svcs, err := getCloudRunServicesInRegion(cctx, project, r)
			if err != nil {
				retErr = err
				cancel()
				return
			}

			mu.Lock()
			for _, s := range svcs {
				out = append(out, ServiceRecord{Service: s, Region: r})
			}
			mu.Unlock()
		}(region)
	}
	wg.Wait()
	return out, retErr
}

func getCloudRunServicesInRegion(ctx context.Context, project string, region *runv1.Location) ([]*runv1.Service, error) {
	client, err := regionalAPIClient(ctx, region)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize client")
	}

	var out []*runv1.Service
	resp, err := runv1.NewProjectsLocationsServicesService(client).List("namespaces/" + project).Do()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to query services in %q", region)
	}
	log.Printf("found %d svcs in %s", len(resp.Items), region.LocationId)
	for _, v := range resp.Items {
		out = append(out, v)
	}
	return out, nil
}

func regionalAPIClient(ctx context.Context, region *runv1.Location) (*runv1.APIService, error) {
	client, err := runv1.NewService(ctx)
	if err != nil {
		return nil, err
	}
	client.BasePath = strings.Replace(client.BasePath, "//run", "//"+region.LocationId+"-run", 1)
	client.BasePath += "apis/serving.knative.dev/" // TODO find way to avoid adding this.
	return client, nil
}

func queryPermissionsForSvc(client *runv1.APIService, svc ServiceRecord) ([]string, error) {
	res := fmt.Sprintf(`projects/%s/locations/%s/services/%s`,
		svc.Metadata.Namespace,
		svc.Region.LocationId,
		svc.Metadata.Name)

	resp, err := client.Projects.Locations.Services.GetIamPolicy(res).Do()
	if err != nil {
		return nil, err
	}

	var members []string
	for _, binding := range resp.Bindings {
		if binding.Role == runInvokerRole {

			for _, m := range binding.Members {
				p := strings.SplitN(m, ":", 2)
				if len(p) > 1 {
					members = append(members, p[1])
				}
			}
		}
	}
	return members, nil
}

func openInBrowser(url string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}


func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

func colorFor(s string) string {
	colors := []string{ // http://www.graphviz.org/doc/info/colors.html
		`coral1`,
		`cadetblue`,
		`gold2`,
		`aquamarine2`,
		`lightpink`,
		`lightsalmon`,
		`springgreen`,
		`wheat1`,
		`lavender`,
		`chartreuse`,
	}
	return colors[int(hash(s))%len(colors)]
}
