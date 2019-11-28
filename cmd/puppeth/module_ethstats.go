// Copyright 2017 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/cpexc/go-cpx/log"
)

// cpxstatsDockerfile is the Dockerfile required to build an  backend
// and associated monitoring site.
var cpxstatsDockerfile = `
FROM cpublic/cpxstats:latest

RUN echo 'module.exports = {trusted: [{{.Trusted}}], banned: [{{.Banned}}], reserved: ["yournode"]};' > lib/utils/config.js
`

// cpxstatsComposefile is the docker-compose.yml file required to deploy and
// maintain an  monitoring site.
/*
var cpxstatsComposefile = `
version: '2'
services:
  cpxstats:
    build: .
    image: {{.Network}}/cpxstats
    container_name: {{.Network}}_cpxstats_1{{if not .VHost}}
    ports:
      - "{{.Port}}:3000"{{end}}
    environment:
      - WS_SECRET={{.Secret}}{{if .VHost}}
      - VIRTUAL_HOST={{.VHost}}{{end}}{{if .Banned}}
      - BANNED={{.Banned}}{{end}}
    privileged: true
    logging:
      driver: "json-file"
      options:
        max-size: "1m"
        max-file: "10"
    restart: always
`
*/
var cpxstatsComposefile = `
version: '3'

networks:
    backend:

services:
  cpxstats-1:
    build: .
    image: {{.Network}}/cpxstats
    container_name: {{.Network}}_cpxstats_1
    ports:
      - 3000:3000
    environment:
      - WS_SECRET={{.Secret}}{{if .VHost}}
      - VIRTUAL_HOST={{.VHost}}{{end}}{{if .Banned}}
      - BANNED={{.Banned}}{{end}}
    privileged: true
    logging:
      driver: "json-file"
      options:
        max-size: "1m"
        max-file: "10"
    restart: always
    networks:
    - backend

  nginx:
    container_name: {{.Network}}_nginx_1
    image: cpublic/nginxssl:latest
    links:
      - cpxstats-1:cpxstats-1
    ports:
      - 80:80
      - 443:443
    volumes:
      - /home/cpxadmin/encryption:/opt/ssl
    privileged: true
    logging:
      driver: "json-file"
      options:
        max-size: "1m"
        max-file: "10"
    depends_on:
      - cpxstats-1
    restart: always
    networks:
      - backend
`

// deployCpxstats deploys a new cpxstats container to a remote machine via SSH,
// docker and docker-compose. If an instance with the specified network name
// already exists there, it will be overwritten!
func deployCpxstats(client *sshClient, network string, port int, secret string, vhost string, trusted []string, banned []string, nocache bool) ([]byte, error) {
	// Generate the content to upload to the server
	workdir := fmt.Sprintf("%d", rand.Int63())
	files := make(map[string][]byte)

	trustedLabels := make([]string, len(trusted))
	for i, address := range trusted {
		trustedLabels[i] = fmt.Sprintf("\"%s\"", address)
	}
	bannedLabels := make([]string, len(banned))
	for i, address := range banned {
		bannedLabels[i] = fmt.Sprintf("\"%s\"", address)
	}

	dockerfile := new(bytes.Buffer)
	template.Must(template.New("").Parse(cpxstatsDockerfile)).Execute(dockerfile, map[string]interface{}{
		"Trusted": strings.Join(trustedLabels, ", "),
		"Banned":  strings.Join(bannedLabels, ", "),
	})
	files[filepath.Join(workdir, "Dockerfile")] = dockerfile.Bytes()

	composefile := new(bytes.Buffer)
	template.Must(template.New("").Parse(cpxstatsComposefile)).Execute(composefile, map[string]interface{}{
		"Network": network,
		//"Port":    port,
		"Secret": secret,
		"VHost":  vhost,
		"Banned": strings.Join(banned, ","),
	})
	files[filepath.Join(workdir, "docker-compose.yaml")] = composefile.Bytes()

	// Upload the deployment files to the remote server (and clean up afterwards)
	if out, err := client.Upload(files); err != nil {
		return out, err
	}
	defer client.Run("rm -rf " + workdir)

	// Build and deploy the cpxstats service
	if nocache {
		return nil, client.Stream(fmt.Sprintf("cd %s && docker-compose -p %s build --pull --no-cache && docker-compose -p %s up -d --force-recreate --timeout 60", workdir, network, network))
	}
	return nil, client.Stream(fmt.Sprintf("cd %s && docker-compose -p %s up -d --build --force-recreate --timeout 60", workdir, network))
}

// cpxstatsInfos is returned from an cpxstats status check to allow reporting
// various configuration parameters.
type cpxstatsInfos struct {
	host   string
	port   int
	secret string
	config string
	banned []string
}

// Report converts the typed struct into a plain string->string map, containing
// most - but not all - fields for reporting to the user.
func (info *cpxstatsInfos) Report() map[string]string {
	return map[string]string{
		"Website address":       info.host,
		"Website listener port": strconv.Itoa(info.port),
		"Login secret":          info.secret,
		"Banned addresses":      strings.Join(info.banned, "\n"),
	}
}

// checkCpxstats does a health-check against an cpxstats server to verify whether
// it's running, and if yes, gathering a collection of useful infos about it.
func checkCpxstats(client *sshClient, network string) (*cpxstatsInfos, error) {
	// Inspect a possible cpxstats container on the host
	infos, err := inspectContainer(client, fmt.Sprintf("%s_cpxstats_1", network))
	if err != nil {
		return nil, err
	}
	if !infos.running {
		return nil, ErrServiceOffline
	}
	// Resolve the port from the host, or the reverse proxy
	port := infos.portmap["3000/tcp"]
	if port == 0 {
		if proxy, _ := checkNginx(client, network); proxy != nil {
			port = proxy.port
		}
	}
	if port == 0 {
		return nil, ErrNotExposed
	}
	// Resolve the host from the reverse-proxy and configure the connection string
	host := infos.envvars["VIRTUAL_HOST"]
	if host == "" {
		host = client.server
	}
	secret := infos.envvars["WS_SECRET"]
	config := fmt.Sprintf("%s@%s", secret, host)
	if port != 80 && port != 443 {
		config += fmt.Sprintf(":%d", port)
	}
	// Retrieve the IP blacklist
	banned := strings.Split(infos.envvars["BANNED"], ",")

	// Run a sanity check to see if the port is reachable
	if err = checkPort(host, port); err != nil {
		log.Warn("cpxstats service seems unreachable", "server", host, "port", port, "err", err)
	}
	// Container available, assemble and return the useful infos
	return &cpxstatsInfos{
		host:   host,
		port:   port,
		secret: secret,
		config: config,
		banned: banned,
	}, nil
}
