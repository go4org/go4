// +build ignore

/*
Copyright 2017 The Go4 Authors.

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

// The tests_on_cloud program runs the tests of a package in an instance
// deployed on Google Compute Engine. Its purpose is to help testing
// go4.org/wkfs/gcs.
package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"go4.org/cloud/cloudlaunch"
	_ "go4.org/wkfs/gcs"

	"cloud.google.com/go/compute/metadata"
	compute "google.golang.org/api/compute/v1"
	storageapi "google.golang.org/api/storage/v1"
)

var (
	// TODO(mpl): testedPkg should be an arg or a flag, but how to pass it to
	// the service running on the cloud? So for now it's hardcoded to go4.org/wkfs/gcs
	// since it's the one I originally wanted to test for.
	testedPkg = "go4.org/wkfs/gcs"
	goVersion = "1.7.4"
	home string
	goBin     string
)

func getGo() error {
	if _, err := os.Stat(goBin); err == nil {
		return nil
	} else {
		if !os.IsNotExist(err) {
			return fmt.Errorf("could not stat %v: %v", goBin, err)
		}
	}
	archiveFile := "go" + goVersion + ".linux-amd64.tar.gz"
	res, err := http.Get("https://storage.googleapis.com/golang/" + archiveFile)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return err
	}
	tempDir, err := ioutil.TempDir("", "go-distribution")
	if err != nil {
		return err
	}
	archivePath := filepath.Join(tempDir, archiveFile)
	if err := ioutil.WriteFile(archivePath, data, 0700); err != nil {
		return err
	}
	return exec.Command("tar", "xzf", archivePath).Run()
}

func getPkg(pkg string) error {
	out, err :=  exec.Command(goBin, "get", "-u", pkg).CombinedOutput()
	if err != nil {
		fmt.Printf("%v\n", string(out))
	}
	return err
}

func testPkg(pkg string) error {
	out, err :=  exec.Command(goBin, "test", pkg).CombinedOutput()
	fmt.Printf("%v\n", string(out))
	return err
}

func main() {
	if !metadata.OnGCE() {
		bucket := os.Getenv("GCSBUCKET")
		if bucket == "" {
			log.Fatal("You need to set the GCSBUCKET env var to specify the Google Cloud Storage bucket to serve from.")
		}
		projectID := os.Getenv("GCEPROJECTID")
		if projectID == "" {
			log.Fatal("You need to set the GCEPROJECTID env var to specify the Google Cloud project where the instance will run.")
		}
		(&cloudlaunch.Config{
			Name:         "testsoncloud",
			BinaryBucket: bucket,
			GCEProjectID: projectID,
			Scopes: []string{
				storageapi.DevstorageFullControlScope,
				compute.ComputeScope,
			},
		}).MaybeDeploy()
		return
	}

	home := "/tmp"
	if err := os.Chdir(home); err != nil {
		log.Fatalf("Could not change to %q directory: %v", home, err)
	}
	goBin = filepath.Join(home, "go/bin/go")
	if err := os.Setenv("GOROOT", filepath.Join(home, "go")); err != nil {
		log.Fatal(err)
	}
	if err := os.Setenv("GOPATH", home); err != nil {
		log.Fatal(err)
	}

	if err := getGo(); err != nil {
		log.Fatalf("Could not get go: %v", err)
	}
	pkg := testedPkg
	if err := getPkg(pkg); err != nil {
		log.Fatalf("Could not get or update pkg %v: %v", pkg, err)
	}
	if err := testPkg(pkg); err != nil {
		log.Fatalf("Testing package %v failed: %v", pkg, err)
	}
}
