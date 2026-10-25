package processapishell

import (
	"bytes"
	"os"
	"os/exec"
)

type Command struct {
	Name string
	Args []string
}

type Result struct {
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
}

type PipelineResult struct {
	Left   Result
	Right  Result
	Stdout string
	Stderr string
}

func Run(name string, args ...string) (Result, error) {
	cmd := exec.Command(name, args...)
	return startAndWait(cmd, true)
}

func RunRedirect(name string, args []string, stdoutPath string) (Result, error) {
	file, err := os.Create(stdoutPath)
	if err != nil {
		return Result{Command: commandString(name, args), ExitCode: -1}, err
	}

	cmd := exec.Command(name, args...)
	cmd.Stdout = file

	result, runErr := startAndWait(cmd, false)
	closeErr := file.Close()
	if runErr != nil {
		return result, runErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	return result, nil
}

func RunPipeline(left Command, right Command) (PipelineResult, error) {
	leftCmd := exec.Command(left.Name, left.Args...)
	rightCmd := exec.Command(right.Name, right.Args...)

	leftStdout, err := leftCmd.StdoutPipe()
	if err != nil {
		return PipelineResult{
			Left:  Result{Command: leftCmd.String(), ExitCode: -1},
			Right: Result{Command: rightCmd.String(), ExitCode: -1},
		}, err
	}

	var leftStderr bytes.Buffer
	var rightStdout bytes.Buffer
	var rightStderr bytes.Buffer

	leftCmd.Stderr = &leftStderr
	rightCmd.Stdin = leftStdout
	rightCmd.Stdout = &rightStdout
	rightCmd.Stderr = &rightStderr

	if err := rightCmd.Start(); err != nil {
		return PipelineResult{
			Left:   Result{Command: leftCmd.String(), ExitCode: -1},
			Right:  Result{Command: rightCmd.String(), ExitCode: -1, Stderr: rightStderr.String()},
			Stderr: rightStderr.String(),
		}, err
	}

	if err := leftCmd.Start(); err != nil {
		_ = rightCmd.Process.Kill()
		_ = rightCmd.Wait()
		return PipelineResult{
			Left:   Result{Command: leftCmd.String(), ExitCode: -1, Stderr: leftStderr.String()},
			Right:  resultFromCommand(rightCmd, rightStdout.String(), rightStderr.String()),
			Stdout: rightStdout.String(),
			Stderr: leftStderr.String() + rightStderr.String(),
		}, err
	}

	rightErr := rightCmd.Wait()
	leftErr := leftCmd.Wait()

	result := PipelineResult{
		Left:   resultFromCommand(leftCmd, "", leftStderr.String()),
		Right:  resultFromCommand(rightCmd, rightStdout.String(), rightStderr.String()),
		Stdout: rightStdout.String(),
		Stderr: leftStderr.String() + rightStderr.String(),
	}

	if leftErr != nil {
		return result, leftErr
	}
	if rightErr != nil {
		return result, rightErr
	}
	return result, nil
}

func startAndWait(cmd *exec.Cmd, captureStdout bool) (Result, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if captureStdout && cmd.Stdout == nil {
		cmd.Stdout = &stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = &stderr
	}

	if err := cmd.Start(); err != nil {
		return Result{Command: cmd.String(), ExitCode: -1}, err
	}

	err := cmd.Wait()
	return resultFromCommand(cmd, stdout.String(), stderr.String()), err
}

func resultFromCommand(cmd *exec.Cmd, stdout string, stderr string) Result {
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	return Result{
		Command:  cmd.String(),
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}
}

func commandString(name string, args []string) string {
	return exec.Command(name, args...).String()
}
