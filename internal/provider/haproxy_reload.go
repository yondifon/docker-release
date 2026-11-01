package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (p *HAProxyProvider) reloadBundled(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	before, err := haproxyWorker(ctx)
	if err != nil {
		return err
	}
	response, err := haproxyCommand(ctx, "/run/haproxy/master.sock", "reload")
	if err != nil {
		return fmt.Errorf("reloading haproxy: %w", err)
	}
	if !strings.HasPrefix(response, "Success=1\n") {
		return fmt.Errorf("haproxy rejected reload: %s", response)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		pid, err := haproxyWorker(ctx)
		if err == nil && pid != before {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("haproxy did not accept the reload; check the proxy logs: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (p *HAProxyProvider) validateBundledConfig(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"-c", "-f", "/usr/local/etc/haproxy/haproxy.cfg", "-f", p.configDir}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DR_PROXY_SKIP_CONFIG"))) {
	case "1", "true", "yes", "on":
	default:
		args = append(args, "-f", filepath.Join(p.configDir, "routes"))
	}
	if output, err := exec.CommandContext(ctx, "haproxy", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("haproxy rejected config: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func haproxyWorker(ctx context.Context) (string, error) {
	response, err := haproxyCommand(ctx, "/run/haproxy/admin.sock", "show info")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(response, "\n") {
		if pid, ok := strings.CutPrefix(line, "Pid: "); ok && pid != "" {
			return pid, nil
		}
	}
	return "", fmt.Errorf("haproxy did not report a worker PID")
}

func haproxyCommand(ctx context.Context, socket, command string) (string, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return "", err
		}
	}
	if _, err := io.WriteString(conn, command+"\n"); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(conn, 1<<20))
	return string(data), err
}
