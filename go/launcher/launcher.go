// Copyright 2016 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Binary launcher is used to manage the envrionment for web tests and start the underlying test.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"

	"github.com/bazelbuild/rules_webtesting/go/launcher/cmdhelper"
	"github.com/bazelbuild/rules_webtesting/go/launcher/diagnostics"
	"github.com/bazelbuild/rules_webtesting/go/launcher/environments/environment"
	"github.com/bazelbuild/rules_webtesting/go/launcher/proxy/proxy"
	"github.com/bazelbuild/rules_webtesting/go/metadata/metadata"
	"github.com/bazelbuild/rules_webtesting/go/util/bazel"
)

type envProvider func(m *metadata.Metadata, d diagnostics.Diagnostics) (environment.Env, error)

var (
	test             = flag.String("test", "", "Test script to launch")
	metadataFileFlag = flag.String("metadata", "", "metadata file")
	envProviders     = map[string]envProvider{}
)

func main() {
	flag.Parse()

	d := diagnostics.NoOP()

	status := Run(d)

	d.Close()
	os.Exit(status)
}

// RegisterEnvProviderFunc adds a new env provider.
func RegisterEnvProviderFunc(name string, p envProvider) {
	envProviders[name] = p
}

// Run runs the test.
func Run(d diagnostics.Diagnostics) int {
	metadataFile, err := bazel.Runfile(*metadataFileFlag)
	if err != nil {
		log.Printf("Error locating metadata file: %v", err)
		return 127
	}

	m, err := metadata.FromFile(metadataFile, nil)
	if err != nil {
		log.Printf("Error reading metadata file: %v", err)
		return 127
	}

	env, err := buildEnv(m, d)
	if err != nil {
		log.Printf("Error building environment: %v", err)
		return 127
	}

	if err := env.SetUp(context.Background()); err != nil {
		log.Printf("Error setting up environment: %v", err)
		return 127
	}

	defer func() {
		if err := env.TearDown(context.Background()); err != nil {
			log.Printf("Error tearing down environment: %v", err)
		}
	}()

	p, err := proxy.New(env, m, d)
	if err != nil {
		log.Printf("Error creating proxy: %v", err)
		return 127
	}

	if err := p.Start(context.Background()); err != nil {
		log.Printf("Error starting proxy: %v", err)
		return 127
	}

	testExe, err := bazel.Runfile(*test)
	if err != nil {
		log.Printf("unable to find %s", *test)
		return 127
	}

	// Temporary directory where WEB_TEST infrastructure writes it tmp files.
	tmpDir, err := bazel.NewTmpDir("test")
	if err != nil {
		log.Printf("Unable to create new temp dir for test: %v", err)
		return -1
	}

	testCmd := exec.Command(testExe, flag.Args()...)
	testCmd.Env = cmdhelper.BulkUpdateEnv(os.Environ(), map[string]string{
		"WEB_TEST_WEBDRIVER_SERVER": fmt.Sprintf("http://%s/wd/hub", p.Address),
		"TEST_TMPDIR":               tmpDir,
		"WEB_TEST_TMPDIR":           bazel.TestTmpDir(),
		"WEB_TEST_TARGET":           *test,
	})
	testCmd.Stdout = os.Stdout
	testCmd.Stderr = os.Stderr
	testCmd.Stdin = os.Stdin

	if status := testCmd.Run(); status != nil {
		log.Printf("test failed %v", status)
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
				return ws.ExitStatus()
			}
		}
		return 1
	}
	return 0
}

func buildEnv(m *metadata.Metadata, d diagnostics.Diagnostics) (environment.Env, error) {
	p, ok := envProviders[m.Environment]
	if !ok {
		return nil, fmt.Errorf("unknown environment: %s", m.Environment)
	}
	return p(m, d)
}
