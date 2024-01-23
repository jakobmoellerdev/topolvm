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

var Containerized = false

// callLVM calls lvm sub-commands and prints the output to the log.
func callLVM(ctx context.Context, args ...string) error {
	return callLVMInto(ctx, nil, args...)
}

// callLVMInto calls lvm sub-commands and decodes the output via JSON into the provided struct pointer.
// if the struct pointer is nil, the output will be printed to the log instead.
func callLVMInto(ctx context.Context, into any, args ...string) error {
	output, err := callLVMMStreamed(ctx, args...)
	defer func() {
		// this will wait for the process to be released.
		// If the process gets interrupted, or has a bad exit status we will log this here.
		// the decode process can still finish normally.
		// This is safe because assuming that the process errors with exit != 0,
		// the decode process will always fail as well.
		// The logs will then first show the decode error and then the exit error.
		if err := output.Close(); err != nil {
			log.FromContext(ctx).Error(err, "failed to run command")
		}
	}()
	if err != nil {
		return fmt.Errorf("failed to execute command: %v", err)
	}

	// if we don't decode the output into a struct, we can still log the command results from stdout.
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
// The caller is responsible for closing the ReadCloser, which will cause the command to complete.
// Not calling close on this method will result in a resource leak.
func callLVMMStreamed(ctx context.Context, args ...string) (io.ReadCloser, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithCallDepth(1))
	cmd := wrapExecCommand(lvm, args...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	return runCommand(ctx, cmd)
}

// wrapExecCommand calls cmd with args but wrapped to run on the host with nsenter if Containerized is true.
func wrapExecCommand(cmd string, args ...string) *exec.Cmd {
	if Containerized {
		args = append([]string{"-m", "-u", "-i", "-n", "-p", "-t", "1", cmd}, args...)
		cmd = nsenter
	}
	c := exec.Command(cmd, args...)
	return c
}

func runCommand(ctx context.Context, cmd *exec.Cmd) (io.ReadCloser, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	log.FromContext(ctx).Info("invoking command", "args", cmd.Args)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Return a read closer that will wait for the command to finish when closed to release all resources.
	return commandReadCloser{cmd: cmd, ReadCloser: stdout}, nil
}

// commandReadCloser is a ReadCloser that calls the Wait function of the command when Close is called.
// This is used to wait for the command the pipe before waiting for the command to finish.
type commandReadCloser struct {
	cmd *exec.Cmd
	io.ReadCloser
}

func (p commandReadCloser) Close() error {
	if err := p.ReadCloser.Close(); err != nil {
		return err
	}
	if err := p.cmd.Wait(); err != nil {
		// wait can result in an exit code error
		return lvmErr(err)
	}
	return nil
}

// lvmErr converts an error if the error is an exec.ExitError.
// it will then return the stderr output together with the exit code.
// this is because the actual error will then not contain any data.
func lvmErr(err error) error {
	var errType *exec.ExitError
	// nolint:SA4006 this is a false positive of never used, some LVM commands run exit code 5.
	// in these cases we can return the stderr output of lvm as it will be filled with the exit message.
	if errors.As(err, &errType) {
		out := errType.String()
		if errType.Stderr != nil {
			out += fmt.Sprintf(": %s", errType.Stderr)
		}
		return errors.New(out)
	}
	return err
}
