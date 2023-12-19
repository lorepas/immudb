/*
Copyright 2024 Codenotary Inc. All rights reserved.

SPDX-License-Identifier: BUSL-1.1
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://mariadb.com/bsl11/

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sservice

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

const ManPath = "/usr/share/man/man1/"

type ManpageService interface {
	InstallManPages(dir string, serviceName string, cmd *cobra.Command) error
	UninstallManPages(dir string, serviceName string) error
}

type manpageService struct{}

func NewManpageService() manpageService {
	return manpageService{}
}

// InstallManPages installs man pages
func (ms manpageService) InstallManPages(dir string, serviceName string, cmd *cobra.Command) (err error) {

	header := &doc.GenManHeader{
		Title:   serviceName + " service",
		Section: "1",
		Source:  fmt.Sprintf("Generated by %s service installer", serviceName),
	}
	_ = os.Mkdir(dir, os.ModePerm)
	return doc.GenManTree(cmd, header, dir)
}

// UninstallManPages uninstalls man pages
func (ms manpageService) UninstallManPages(dir string, serviceName string) error {
	err1 := os.Remove(filepath.Join(dir, serviceName+"-version.1"))
	err2 := os.Remove(filepath.Join(dir, serviceName+".1"))
	switch {
	case err1 != nil:
		return err1
	case err2 != nil:
		return err2
	default:
		return nil
	}
}
