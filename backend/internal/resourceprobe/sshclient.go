package resourceprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient is a minimal SSH transport wrapper owned by this package. It keeps
// the surface used by the resource probes (and by callers that need a live
// session, such as build adapters) without pulling in an external helper
// module.
type SSHClient struct {
	client *ssh.Client
}

// RawClient exposes the underlying transport so callers can inspect negotiated
// parameters such as the remote server version.
func (c *SSHClient) RawClient() *ssh.Client {
	if c == nil {
		return nil
	}
	return c.client
}

// Close releases the transport. It is safe to call on a nil or closed client.
func (c *SSHClient) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	err := c.client.Close()
	c.client = nil
	return err
}

// RunCmdTimeout runs one command in a fresh session and returns its combined
// output. The session is closed when the timeout expires so a hung remote
// command cannot pin the caller forever.
func (c *SSHClient) RunCmdTimeout(command string, timeout time.Duration) ([]byte, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("ssh: client is closed")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	session, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh: open session: %w", err)
	}
	defer session.Close()

	type outcome struct {
		output []byte
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- outcome{output: output, err: runErr}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.output, result.err
	case <-timer.C:
		return nil, fmt.Errorf("ssh: command timed out after %s", timeout)
	}
}

// SecureLoginOptions carries the connection material for one login attempt.
// Secrets stay inside this struct and are never logged by the package.
type SecureLoginOptions struct {
	Addr            string
	User            string
	Auth            string
	IsPasswd        bool
	Passphrase      string
	Timeout         time.Duration
	HostKeyCallback ssh.HostKeyCallback
	KeyboardAuth    bool
}

// SecureLogin dials and authenticates an SSH connection. Password and private
// key material are selected from IsPasswd; keyboard-interactive is answered
// with the same password when the server asks for it.
func SecureLogin(ctx context.Context, opts SecureLoginOptions) (*SSHClient, error) {
	if strings.TrimSpace(opts.Addr) == "" {
		return nil, errors.New("ssh: address is required")
	}
	if strings.TrimSpace(opts.User) == "" {
		return nil, errors.New("ssh: user is required")
	}
	if strings.TrimSpace(opts.Auth) == "" {
		return nil, errors.New("ssh: credential is required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	var methods []ssh.AuthMethod
	if opts.IsPasswd {
		methods = append(methods, ssh.Password(opts.Auth))
	} else {
		signer, err := parsePrivateKey(opts.Auth, opts.Passphrase)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if opts.KeyboardAuth {
		password := opts.Auth
		methods = append(methods, ssh.KeyboardInteractive(func(_ string, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				if opts.IsPasswd {
					answers[i] = password
					continue
				}
				answers[i] = opts.Passphrase
			}
			return answers, nil
		}))
	}

	cfg := &ssh.ClientConfig{
		User:            opts.User,
		Auth:            methods,
		HostKeyCallback: opts.HostKeyCallback,
		Timeout:         timeout,
	}
	if cfg.HostKeyCallback == nil {
		cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	dialer := &net.Dialer{Timeout: timeout}
	dialCtx := ctx
	if dialCtx == nil {
		dialCtx = context.Background()
	}
	conn, err := dialer.DialContext(dialCtx, "tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := dialCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	transport, chans, reqs, err := ssh.NewClientConn(conn, opts.Addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &SSHClient{client: ssh.NewClient(transport, chans, reqs)}, nil
}

func parsePrivateKey(material, passphrase string) (ssh.Signer, error) {
	key := []byte(material)
	if strings.TrimSpace(passphrase) != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("ssh: parse private key with passphrase: %w", err)
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		if strings.Contains(err.Error(), "encrypted") {
			return nil, errors.New("ssh: private key is encrypted, a passphrase is required")
		}
		return nil, fmt.Errorf("ssh: parse private key: %w", err)
	}
	return signer, nil
}
