package sleep

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const lidGuardianArgument = "--lid-guardian"
const lidGuardianTimeout = 3 * time.Second

// RunLidGuardian must run before application startup. The pipe is the parent's
// lifetime: EOF triggers cleanup, without resetting any unrelated startup state.
func RunLidGuardian(args []string) (bool, error) {
	if len(args) == 0 || args[0] != lidGuardianArgument {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("lid guardian takes no additional arguments")
	}
	if !nativeLidAvailable() {
		return true, errUnsupported
	}
	prepareNativeGuardian()
	return true, runLidGuardian(os.Stdin, os.Stdout, nativeSetLid)
}

// A close error after a successful scalar call is still an applied request.
// Remember application for EOF cleanup and report it independently of the error.
func runLidGuardian(input io.Reader, output io.Writer, set func(bool) (applied bool, err error)) (result error) {
	enabled := false
	defer func() {
		if enabled {
			applied, err := set(false)
			if !applied && err == nil {
				err = errors.New("lid reset was not applied")
			}
			result = errors.Join(result, err)
		}
	}()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 256), 256)
	for scanner.Scan() {
		command := scanner.Text()
		if command != "on" && command != "off" {
			return fmt.Errorf("invalid lid guardian command %q", command)
		}
		want := command == "on"
		var err error
		applied := true // An already-released request needs no native write.
		if want || enabled {
			applied, err = set(want)
			if !applied && err == nil {
				err = errors.New("lid request was not applied")
			}
			if applied {
				enabled = want
			}
		}
		reply := "ok"
		if err != nil {
			// Keep each reply bounded and on one line, including native errors.
			message := strings.NewReplacer("\n", " ", "\r", " ").Replace(err.Error())
			if len(message) > 2048 {
				message = message[:2048]
			}
			reply = "error " + message
			if applied {
				reply = "applied-" + reply
			}
		}
		if _, err := io.WriteString(output, reply+"\n"); err != nil {
			return fmt.Errorf("write lid guardian reply: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read lid guardian command: %w", err)
	}
	return nil
}

type lidGuardian struct {
	cmd        *exec.Cmd
	input      io.WriteCloser
	output     io.ReadCloser
	scanner    *bufio.Scanner
	done       chan struct{}
	waitErr    error // published by closing done
	timeout    time.Duration
	closeInput sync.Once
	broken     bool // serialized with exchange; a lost ACK makes this pipe unusable
}

func startLidGuardian() (*lidGuardian, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find lid guardian executable: %w", err)
	}
	return startLidGuardianCommand(exec.Command(executable, lidGuardianArgument))
}

// Command injection allows tests to use only a harmless protocol fixture.
func startLidGuardianCommand(cmd *exec.Cmd) (*lidGuardian, error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open lid guardian input: %w", err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("open lid guardian output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("start lid guardian: %w", err)
	}
	g := &lidGuardian{cmd: cmd, input: input, output: output, done: make(chan struct{}), timeout: lidGuardianTimeout}
	g.scanner = bufio.NewScanner(output)
	g.scanner.Buffer(make([]byte, 256), 4096)
	go func() { g.waitErr = cmd.Wait(); close(g.done) }()
	return g, nil
}

func (g *lidGuardian) alive() bool {
	select {
	case <-g.done:
		return false
	default:
		return true
	}
}

// Only the inhibitor's serialized reconciliation calls exchange/stop.
func (g *lidGuardian) exchange(enabled bool) (bool, error) {
	if !g.alive() || g.broken {
		return false, errors.New("lid guardian is unavailable")
	}
	type acknowledgement struct {
		applied  bool
		received bool
		err      error
	}
	result := make(chan acknowledgement, 1)
	go func() {
		ack := acknowledgement{}
		defer func() { result <- ack }()
		command := "off\n"
		if enabled {
			command = "on\n"
		}
		if _, err := io.WriteString(g.input, command); err != nil {
			ack.err = fmt.Errorf("write lid guardian command: %w", err)
			return
		}
		if !g.scanner.Scan() {
			err := g.scanner.Err()
			if err == nil {
				err = io.EOF
			}
			ack.err = fmt.Errorf("read lid guardian acknowledgement: %w", err)
			return
		}
		reply := g.scanner.Text()
		switch {
		case reply == "ok":
			ack.applied, ack.received = true, true
		case strings.HasPrefix(reply, "applied-error "):
			ack.applied, ack.received = true, true
			ack.err = errors.New("lid guardian: " + strings.TrimPrefix(reply, "applied-error "))
		case strings.HasPrefix(reply, "error "):
			ack.received = true
			ack.err = errors.New("lid guardian: " + strings.TrimPrefix(reply, "error "))
		default:
			ack.err = fmt.Errorf("invalid lid guardian acknowledgement %q", reply)
		}
	}()
	timer := time.NewTimer(g.timeout)
	defer timer.Stop()
	select {
	case ack := <-result:
		g.broken = !ack.received
		return ack.applied, ack.err
	case <-timer.C:
		g.broken = true
		g.closeInput.Do(func() { _ = g.input.Close() })
		_ = g.output.Close()
		return false, errors.New("lid guardian acknowledgement timed out")
	}
}

func (g *lidGuardian) stop() error {
	g.closeInput.Do(func() { _ = g.input.Close() })
	timer := time.NewTimer(g.timeout)
	defer timer.Stop()
	select {
	case <-g.done:
		_ = g.output.Close()
		if g.waitErr != nil {
			return fmt.Errorf("lid guardian exit: %w", g.waitErr)
		}
		return nil
	case <-timer.C:
		killErr := g.cmd.Process.Kill()
		_ = g.output.Close()
		deadline := time.NewTimer(g.timeout)
		defer deadline.Stop()
		select {
		case <-g.done:
		case <-deadline.C:
		}
		return errors.Join(errors.New("lid guardian cleanup timed out"), killErr)
	}
}
