package command

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// wrapExecCommand calls cmd with args but wrapped to run
// on the host
func wrapExecCommand(cmd string, args ...string) *exec.Cmd {
	if Containerized {
		args = append([]string{"-m", "-u", "-i", "-n", "-p", "-t", "1", cmd}, args...)
		cmd = nsenter
	}
	c := exec.Command(cmd, args...)
	return c
}

// callLVM calls lvm sub-commands and prints the output to the log.
func callLVM(ctx context.Context, args ...string) error {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithCallDepth(1))
	return callLVMInto(ctx, nil, args...)
}

// callLVMInto calls lvm sub-commands and decodes the output via JSON into the provided struct pointer.
func callLVMInto(ctx context.Context, into any, args ...string) error {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithCallDepth(1))
	output, err := callLVMMStreamed(ctx, args...)
	defer func() {
		// this will wait for the process to be released.
		if err := output.Close(); err != nil {
			log.FromContext(ctx).Error(err, "failed to properly close command")
		}
	}()
	if err != nil {
		return fmt.Errorf("failed to execute command: %v", err)
	}

	// if we dont decode the output into a struct, we can still log the command results from stdout.
	if into == nil {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			log.FromContext(ctx).Info(strings.TrimSpace(scanner.Text()))
		}
		return scanner.Err()
	}

	return json.NewDecoder(output).Decode(&into)
}

// callLVMMStreamed calls lvm sub-commands and returns the output as a ReadCloser.
// The caller is responsible for closing the ReadCloser.
// When the ReadCloser is closed, the command will be waited for completion.
func callLVMMStreamed(ctx context.Context, args ...string) (io.ReadCloser, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithCallDepth(1))
	cmd := wrapExecCommand(lvm, args...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "LC_ALL=C")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return io.NopCloser(strings.NewReader(lvmErrToString(err))), err
	}
	log.FromContext(ctx).Info("invoking command", "args", cmd.Args)
	if err := cmd.Start(); err != nil {
		return io.NopCloser(strings.NewReader(lvmErrToString(err))), err
	}

	return pipeClosingReadCloser{pipeclose: func() error {
		// after the stdout has been read from, we can safely close out the command.
		// this will wait for the process exit.
		return cmd.Wait()
	}, ReadCloser: stdout}, nil
}

// pipeClosingReadCloser is a ReadCloser that calls the pipeclose function when Close is called.
// This is used to wait for the command the pipe before waiting for the command to finish.
type pipeClosingReadCloser struct {
	pipeclose func() error
	io.ReadCloser
}

func (p pipeClosingReadCloser) Close() error {
	if err := p.ReadCloser.Close(); err != nil {
		return err
	}
	if p.pipeclose != nil {
		if err := p.pipeclose(); err != nil {
			return errors.New(lvmErrToString(err))
		}
	}
	return nil
}

// lvmErrToString converts an error to a string, if the error is an exec.ExitError, it will return the stderr output.
// this is because the actual error will then not contain any data.
func lvmErrToString(err error) string {
	var errType *exec.ExitError
	// nolint:SA4006 this is a false positive of never used, some LVM commands run exit code 5.
	// in these cases we can return the stderr output of lvm as it will be filled with the exit message.
	if errors.As(err, &errType) {
		out := errType.String()
		if errType.Stderr != nil {
			out += fmt.Sprintf(": %s", errType.Stderr)
		}
		return out
	}
	return err.Error()
}
