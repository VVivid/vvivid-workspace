# Process API Shell Pattern in Go

This case study demonstrates the process pattern behind shell commands using Go's standard library.

Related post: [The Process API Behind Every Shell Command](https://vvivid.dev/posts/process_api_behind_every_shell_command/).

The original UNIX idea is usually described as:

- create a child process
- replace the child with another program
- wait for that program to finish

In normal Go application code, the practical API is `os/exec`. It hides the raw system calls but keeps the same shape: build an `exec.Cmd`, configure its input/output, start it, and wait for the result.

## What This Example Shows

- `Run`: starts a command, waits for it, and captures stdout/stderr.
- `RunRedirect`: points a command's stdout at a file before starting it.
- `RunPipeline`: connects one command's stdout to another command's stdin, similar to `grep "error" app.log | wc -l`.

The goal is not to build a full shell. The goal is to make the process lifecycle visible in useful Go code.

## Example

```go
result, err := processapishell.Run("printf", "hello\n")
if err != nil {
    // Non-zero exits are returned as *exec.ExitError.
}

fmt.Print(result.Stdout)
```

```go
result, err := processapishell.RunPipeline(
    processapishell.Command{Name: "grep", Args: []string{"error", "app.log"}},
    processapishell.Command{Name: "wc", Args: []string{"-l"}},
)
if err != nil {
    // Inspect result.Left, result.Right, and result.Stderr.
}

fmt.Print(result.Stdout)
```

## Run

From the module root (`examples/go-lang-case-study`):

```sh
go test ./...
```

The tests use common Unix commands such as `printf`, `grep`, and `wc`. They skip themselves when a command is not available on `PATH`.
