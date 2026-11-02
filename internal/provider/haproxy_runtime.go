package provider

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type haproxyServer struct {
	name   string
	addr   string
	weight string
	state  string
	args   []string
}

func haproxyBackendOptions(data []byte) string {
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "server ") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// updateHAProxyBackend applies the servers of every backend in data.
func updateHAProxyBackend(ctx context.Context, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, section := range splitHAProxyBackends(data) {
		if err := updateHAProxyBackendServers(ctx, section); err != nil {
			return err
		}
	}
	return nil
}

// splitHAProxyBackends cuts data into one chunk per `backend` line.
func splitHAProxyBackends(data []byte) [][]byte {
	var sections [][]byte
	var current []string
	flush := func() {
		if len(current) > 0 {
			sections = append(sections, []byte(strings.Join(current, "\n")))
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "backend" {
			flush()
			current = nil
		}
		current = append(current, line)
	}
	flush()
	return sections
}

func updateHAProxyBackendServers(ctx context.Context, data []byte) error {
	backend, servers := haproxyBackendServers(data)
	if backend == "" {
		return nil
	}
	existing, err := haproxyServerStates(ctx, backend)
	if err != nil {
		return err
	}
	for _, server := range servers {
		_, found := existing[server.name]
		if err := server.apply(ctx, backend, found); err != nil {
			return err
		}
		delete(existing, server.name)
	}
	for name := range existing {
		if err := setHAProxyServer(ctx, backend+"/"+name, "state maint"); err != nil {
			return err
		}
	}
	applied, err := haproxyServerStates(ctx, backend)
	if err != nil {
		return err
	}
	for _, server := range servers {
		actual := applied[server.name]
		host, port, _ := net.SplitHostPort(server.addr)
		admin, _ := strconv.Atoi(actual["srv_admin_state"])
		if actual["srv_addr"] != host || actual["srv_port"] != port || actual["srv_uweight"] != server.weight || (admin != 0) != (server.state == "maint") {
			return fmt.Errorf("haproxy did not apply server %s/%s; check its control socket", backend, server.name)
		}
	}
	return nil
}

func haproxyBackendServers(data []byte) (string, []haproxyServer) {
	var backend string
	var servers []haproxyServer
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "backend" {
			backend = fields[1]
		}
		if fields[0] != "server" {
			continue
		}
		server := haproxyServer{name: fields[1], addr: fields[2], args: fields[2:], weight: "1", state: "ready"}
		for i := 3; i < len(fields); i++ {
			switch fields[i] {
			case "weight":
				i++
				server.weight = fields[i]
			case "disabled":
				server.state = "maint"
			}
		}
		servers = append(servers, server)
	}
	return backend, servers
}

func (server haproxyServer) apply(ctx context.Context, backend string, exists bool) error {
	id := backend + "/" + server.name
	if !exists {
		response, err := haproxyCommand(ctx, "/run/haproxy/admin.sock", "add server "+id+" "+strings.Join(server.args, " "))
		if err != nil {
			return err
		}
		if !strings.Contains(response, "New server registered") {
			return fmt.Errorf("adding HAProxy server %s: %s", id, response)
		}
	}
	host, port, err := net.SplitHostPort(server.addr)
	if err != nil {
		return err
	}
	if _, err := haproxyCommand(ctx, "/run/haproxy/admin.sock", "set server "+id+" addr "+host+" port "+port); err != nil {
		return err
	}
	if err := setHAProxyServer(ctx, id, "weight "+server.weight); err != nil {
		return err
	}
	return setHAProxyServer(ctx, id, "state "+server.state)
}

func setHAProxyServer(ctx context.Context, id, setting string) error {
	response, err := haproxyCommand(ctx, "/run/haproxy/admin.sock", "set server "+id+" "+setting)
	if err != nil {
		return err
	}
	if strings.TrimSpace(response) != "" {
		return fmt.Errorf("updating HAProxy server %s: %s", id, response)
	}
	return nil
}

func haproxyServerStates(ctx context.Context, backend string) (map[string]map[string]string, error) {
	response, err := haproxyCommand(ctx, "/run/haproxy/admin.sock", "show servers state "+backend)
	if err != nil {
		return nil, err
	}
	servers := make(map[string]map[string]string)
	var header []string
	for _, line := range strings.Split(response, "\n") {
		if strings.HasPrefix(line, "# ") {
			header = strings.Fields(strings.TrimPrefix(line, "# "))
			continue
		}
		fields := strings.Fields(line)
		if len(header) == 0 || len(fields) != len(header) {
			continue
		}
		values := make(map[string]string, len(header))
		for i, key := range header {
			values[key] = fields[i]
		}
		servers[values["srv_name"]] = values
	}
	if len(header) == 0 {
		return nil, fmt.Errorf("haproxy did not report backend %s: %s", backend, response)
	}
	return servers, nil
}
